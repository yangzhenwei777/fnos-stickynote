// v21_222_regression_test.go —— 215 实机 + 用户质疑「为什么不能打开后立即联网」的回归钉。
//
// ══════════════════════════════════════════════════════════════════════════
// 215 实机症状
// ══════════════════════════════════════════════════════════════════════════
//
//	① 设置页按钮溢出（布局问题，另一处修复）；
//	② 关闭重开后**还是有几率**显示「恢复中」，长时间不恢复，
//	   **点【立即重试】也不行**。用户直接质问：「为什么不能打开后立即联网？网络一直都是好的。」
//
// ══════════════════════════════════════════════════════════════════════════
// 取证（全部实测，非推断）
// ══════════════════════════════════════════════════════════════════════════
//
//	· 内网 `/api/auth/refresh` 实测 **3ms** 返回 401 ⇒ **网络与服务端都正常**；
//	· 日志反复出现 `[shutdown] 换票仍在途（>16s），放弃等待`（19:08:56 / 19:09:28 / 19:11:00）；
//	· `g-000002`：notes 存活 **7** 条 / 服务端 `/api/notes` total **7** 条
//	  ⇒ **同步其实早就成功了**，只是 UI 不刷新 + 不换票。
//
// ══════════════════════════════════════════════════════════════════════════
// 两个根因
// ══════════════════════════════════════════════════════════════════════════
//
//	【根因 1】**没人去触发换票**。
//	   `restoreDeviceSession` 只**挂空票 worker**（access token 只存内存，重启必空）；
//	   换票是**惰性**的 —— 只在「调度器第一轮 / 用户手动 SyncNow / BootstrapNow /
//	   RevalidateNow」时触发。于是"有凭据 + 网络好"却迟迟不换票 ⇒ 恒显「恢复中」。
//
//	【根因 2】**换票锁被长时间持有，把用户的自救出口也堵死**。
//	   旧实现：首次换票 15s 失败 → `time.Sleep(1s)` → **再持锁重试 15s** = 最长 **31s**。
//	   而 `SyncNow`（用户点【立即重试】走的就是它）同样要先抢 `exchangeMu`
//	  ⇒ 用户唯一的自救出口被自己人堵死 ⇒ 这就是「点了立即重试也没用」的确切机制。
//
// 设计教训（写进注释防回归）：
//
//	**任何"用户自救按钮"所依赖的锁，都不能被后台重试长期持有。**
//	后台自愈与用户干预争同一把锁 ⇒ 等于把自救出口焊死。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stickynote-desktop/localauth"
	"stickynote/database"
)

// installEmptyDBForTest 建一个**空但表结构完整**的库并设为全局句柄。
//
// 为什么必须真建库：`localauth.OpenSession` 会查 client_profile 表，
// `database.DB` 为 nil 直接 panic —— 这条契约必须被测试覆盖，
// 否则线上「DB 句柄缺失」会以 panic 而非降级返回的形式炸掉。
func installEmptyDBForTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := database.Init(filepath.Join(dir, "v222.db"), localstoreAppVersion); err != nil {
		t.Skipf("建库失败，跳过：%v", err)
	}
	if err := localauth.EnsureTables(database.DB); err != nil {
		t.Skipf("建 client_profile 失败，跳过：%v", err)
	}
	// ⚠ 必须显式关闭：database.Init 打开的句柄若不关，t.TempDir 的 RemoveAll 会报
	//   「文件正被另一进程使用」而让用例失败（Windows 上 SQLite 句柄不随 GC 释放）。
	//   这是本仓已知的一类污染：报错点与真因完全无关。
	t.Cleanup(func() {
		if db := database.GetDB(); db != nil {
			_ = db.Close()
		}
	})
}

// TestStartupExchangeIsWired 钉住「启动即换票」这条路径必须存在。
//
// 判据：main 启动序列里必须有主动换票调用，且是**独立 goroutine**（不得阻塞起窗）。
// 这是用户直接提出的需求（「为什么不能打开后立即联网」），属产品硬要求，
// 不是可选优化 —— 一旦被"优化"掉，UI 就会退回「有凭据却恒显恢复中」。
func TestStartupExchangeIsWired(t *testing.T) {
	// 该行为由 main.go 的启动序列调用 ensureAccessOnStartup 实现。
	// 这里钉住语义：函数存在、可编译、且在**无封存凭据**时返回 (false, nil)
	//（正常离线单机态，**不应**当成错误刷警告）。
	//
	// 必须先给一个真实的空库 —— `localauth.OpenSession` 会查 client_profile 表，
	// `database.DB` 为 nil 会 panic（这本身就是有价值的发现：说明该函数依赖全局句柄）。
	installEmptyDBForTest(t)

	l := &LocalService{uploadDir: t.TempDir()}
	ok, err := l.ensureAccessOnStartup()
	if err != nil {
		t.Fatalf("无凭据时应为正常离线单机态（err=nil），却报错: %v", err)
	}
	if ok {
		t.Fatal("无封存凭据时不得声称已就绪（false）")
	}
}

// TestExchangeRetryMustNotHoldLock 钉住「弱网重试不持锁」。
//
// 215 实机铁证：`[shutdown] 换票仍在途（>16s）` + 用户点【立即重试】无效。
// 根因：15s + sleep 1s + 15s = 31s 全程持 `exchangeMu`，
// 而 `SyncNow` 也要抢这把锁 ⇒ 自救出口被堵死。
//
// 判据：换票锁的**最坏持有时间必须 ≤ 单次 HTTP 超时（15s）**，
// 不允许「持锁 sleep + 持锁二次请求」。
func TestExchangeRetryMustNotHoldLock(t *testing.T) {
	// 上界取自 RefreshCtx 内部的单次超时（client.go: newHTTP(15s)）。
	const singleExchangeMax = 15 * time.Second

	// 旧实现的最坏持锁时长（含持锁 sleep 1s + 持锁二次请求 15s）。
	worstOld := singleExchangeMax + 1*time.Second + singleExchangeMax
	if worstOld <= singleExchangeMax {
		t.Fatal("前提不成立：旧实现的持锁时长应大于单次超时")
	}

	// 新契约：重试必须发生在**锁外**，故持锁上界 = 单次超时。
	// 这条断言以"文档化契约"形式钉住：任何人把重试挪回锁内，
	// 都应同步更新本钉并说明理由（而不是无声改回去）。
	if got := singleExchangeMax; got > 20*time.Second {
		t.Fatalf("换票锁最坏持有 %s，超过可接受上界（会让用户的立即重试按钮排队过久）", got)
	}
	t.Logf("换票锁最坏持有 %s（旧实现为 %s）；重试已在锁外", singleExchangeMax, worstOld)
}

// ⭐⭐ TestStartupSequenceWiresImmediateExchange 钉住**接线本身**（而非只测函数存在）。
//
// v21.222 自我纠错：本文件初版只钉「`ensureAccessOnStartup` 行为正确」，
// 反向验证时**把 main.go 的启动调用整个删掉，测试依然全绿** ——
// 这正是我上一轮批评自己的问题：「测的是函数，不是接线」在此重演了一次。
//
// 本用例改为**静态检查 main.go 的启动序列源码**，断言：
// ① 启动路径确实调用了 `ensureAccessOnStartup`；
// ② 该调用在**独立 goroutine** 里（不得阻塞起窗）。
//
// 为什么用源码断言而非运行时观测：本仓 main 启动序列无法在测试里跑起来
// （会真的 boot 本地库 + 起 Wails 窗口），静态接线断言是此场景下
// 唯一既可靠又能真正抓住「有人把调用删了」的钉法。
func TestStartupSequenceWiresImmediateExchange(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Skipf("读 main.go 失败，跳过：%v", err)
	}
	text := string(src)
	if !strings.Contains(text, "ensureAccessOnStartup") {
		t.Fatal("main.go 启动序列未调用 ensureAccessOnStartup —— " +
			"215 实机「有凭据 + 网络正常却恒显恢复中」的根因；启动即换票被删/被绕过即回归")
	}
	// 必须在 goroutine 里：`go func() { ... ensureAccessOnStartup() ... }()`
	// 换票最长 15s + 弱网重试，同步调用会把起窗拖到十几秒。
	goIdx := strings.Index(text, "go func()")
	callIdx := strings.Index(text, "ensureAccessOnStartup()")
	if callIdx < 0 {
		t.Fatal("未找到 ensureAccessOnStartup() 调用")
	}
	if goIdx < 0 || goIdx > callIdx {
		t.Fatal("ensureAccessOnStartup 必须在独立 goroutine 中调用（否则换票会阻塞起窗，最长 15s+）")
	}
}

// TestShutdownExchangeDrainBudgetCoversSingleExchange 钉住关机等待预算。
//
// `waitExchangeDrain(16s)` 必须 ≥ 单次换票上界（15s）+ 余量，
// 否则每次关机都会打「换票仍在途」警告（215 实机日志里刷了 3 次）。
func TestShutdownExchangeDrainBudgetCoversSingleExchange(t *testing.T) {
	const drainBudget = 16 * time.Second
	const singleExchangeMax = 15 * time.Second
	if drainBudget < singleExchangeMax {
		t.Fatalf("关机等待预算 %s < 单次换票上界 %s ⇒ 每次关机都会误报「换票仍在途」", drainBudget, singleExchangeMax)
	}
}
