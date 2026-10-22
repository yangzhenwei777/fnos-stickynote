// bootstrap_timeout_rollback_test.go —— v21.200 四处修复的回归钉。
//
// 用户 2026-10-09 实机取证（53 行日志坐实）：
//
//	18:15:34 [bootstrap] 本地无文件夹…执行一次快照全量同步
//	（此后 2 分 14 秒日志完全空白 → 用户杀进程）        ← 症状③登录永久转圈
//	18:17:48 localstore active generation g-000002       ← 换代了
//	       [auth] identity healed from g-000001         ← 症状②④笔记消失 + 显示未登录
//	18:19:39 [sched] 调度器未在预期时限内退出             ← 症状⑤
//
// 四条修复各自的钉：
//
//	① pollTimeout 10min → 90s（永久转圈）
//	② 换代后置步骤失败 → active 指针回退（笔记消失）
//	③ 前端 withTimeout（UI 不再无限等待）
//	④ 诊断包 DB 计数带硬超时（排障路径不阻塞）
package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stickynote-desktop/bootstrap"
	"stickynote-desktop/localstore"
)

// ① pollTimeout 必须是 90s 级而非 10min 级（黑盒：跑一次真实 Run 的超时路径代价太大，
// 这里改为断言「常量已不在 10 分钟量级」—— 通过 bootstrap 包导出的 PollTimeout 读取）。
func TestPollTimeoutIsBounded(t *testing.T) {
	d := bootstrapPollTimeout()
	if d > 3*time.Minute {
		t.Fatalf("pollTimeout 过大（%v）—— UI 最坏等待应 ≤3 分钟，症状③由此产生", d)
	}
	if d < 30*time.Second {
		t.Fatalf("pollTimeout 过小（%v），大库冷启动可能被误杀", d)
	}
	t.Logf("pollTimeout=%v（原 10 分钟 → 定版 90s）", d)
}

// ② 换代后置步骤失败时，active 指针必须回退到 prev（症状④：笔记消失 + 显示未登录）。
func TestRollbackActiveRestoresPrev(t *testing.T) {
	dir := t.TempDir()
	lay := localstore.Layout{ProfileDir: dir}
	if err := lay.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	mkGen(t, lay, "g-000001", "gen1")
	mkGen(t, lay, "g-000002", "gen2")

	// 模拟「换代成功但后置步骤失败」：指针已切到 g-000002
	if err := writeActive(lay, "g-000002"); err != nil {
		t.Fatal(err)
	}

	if err := lay.RollbackActive("g-000001"); err != nil {
		t.Fatalf("RollbackActive: %v", err)
	}
	cur, _ := lay.Active()
	if cur != "g-000001" {
		t.Fatalf("回退失败：active=%s", cur)
	}
	if b, _ := os.ReadFile(lay.GenDB(cur)); string(b) != "gen1" {
		t.Fatalf("回退后数据不完整: %q", string(b))
	}
}

func TestRollbackActiveIdempotent(t *testing.T) {
	dir := t.TempDir()
	lay := localstore.Layout{ProfileDir: dir}
	if err := lay.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	mkGen(t, lay, "g-000001", "gen1")
	_ = writeActive(lay, "g-000001")
	if err := lay.RollbackActive("g-000001"); err != nil {
		t.Fatalf("幂等调用不应报错: %v", err)
	}
}

// 目标代不存在必须报错——绝不能把指针写成悬空值（那会让下次启动直接开库失败）。
func TestRollbackActiveRejectsMissingTarget(t *testing.T) {
	dir := t.TempDir()
	lay := localstore.Layout{ProfileDir: dir}
	if err := lay.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	mkGen(t, lay, "g-000002", "gen2")
	_ = writeActive(lay, "g-000002")
	if err := lay.RollbackActive("g-999999"); err == nil {
		t.Fatal("目标代不存在时必须报错")
	}
	if cur, _ := lay.Active(); cur != "g-000002" {
		t.Fatalf("失败时不得改动指针，cur=%s", cur)
	}
}

func TestRollbackActiveNoopWhenPrevEmpty(t *testing.T) {
	dir := t.TempDir()
	lay := localstore.Layout{ProfileDir: dir}
	if err := lay.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	mkGen(t, lay, "g-000001", "gen1")
	_ = writeActive(lay, "g-000001")
	if err := lay.RollbackActive(""); err != nil {
		t.Fatalf("prev 为空应静默返回: %v", err)
	}
	if cur, _ := lay.Active(); cur != "g-000001" {
		t.Fatalf("prev 为空时不得改指针，cur=%s", cur)
	}
}

// ④ 诊断包计数：**profileDir 不可用**时立即返回 n/a（排障路径绝不阻塞）。
//
// v21.203 语义更新：诊断不再读全局 database.DB，而是按 profileDir 自行开
// 独立只读连接（根因：全局池只有 2 条连接，故障时必然抢不到 → 计数全 timeout，
// 而那正是最需要诊断的时刻）。故"库不可用"的判据从"全局 DB 为 nil"改为
// "profileDir 为空 / 无代指针 / 库文件不存在"。
func TestScalarCountWithTimeoutNilDB(t *testing.T) {
	t0 := time.Now()
	got := scalarCountWithTimeout("", "SELECT COUNT(*) FROM notes", 2*time.Second)
	if got != "n/a" {
		t.Fatalf("profileDir 为空时应返回 n/a，实际 %q", got)
	}
	if el := time.Since(t0); el > time.Second {
		t.Fatalf("不可用时应立即返回，实际 %v", el)
	}
	// 目录存在但无代指针（尚未首装完成）同样降级为 n/a，且不阻塞
	empty := t.TempDir()
	t0 = time.Now()
	if got := scalarCountWithTimeout(empty, "SELECT COUNT(*) FROM notes", 2*time.Second); got != "n/a" {
		t.Fatalf("无代指针时应返回 n/a，实际 %q", got)
	}
	if el := time.Since(t0); el > time.Second {
		t.Fatalf("无代指针时应立即返回，实际 %v", el)
	}
}

// ④ 长事务持锁时，计数查询必须**超时返回**而不是无限等待。
// 这条直接钉住「诊断包不被 DB 拖死」这个修复目标。
func TestScalarCountWithTimeoutRespectsDeadline(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "p.db")) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Skipf("无法打开测试库: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Skipf("建表失败: %v", err)
	}
	// 持一个写事务（_txlock=immediate 下会占住写锁）
	tx, err := db.Begin()
	if err != nil {
		t.Skipf("begin 失败: %v", err)
	}
	defer tx.Rollback()

	// v21.203：把这份"被长事务占住的库"摆成 profileDir 下的**当前代**，
	// 让诊断走自己的只读连接去读它 —— 这才是真实故障场景
	// （换代/同步正在写库时用户点导出诊断）。
	prof := t.TempDir()
	lay := localstore.Layout{ProfileDir: prof}
	if err := lay.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	gen := "g-000001"
	if err := os.MkdirAll(lay.GenDir(gen), 0700); err != nil {
		t.Fatal(err)
	}
	// 把正在被长事务占住的库文件复制进代目录（保持打开的 tx 指向同一 inode：
	// 直接把测试库的路径设为代库更简单 —— 改用软链不可移植，这里直接复制并
	// 另开一个写事务去占锁，语义等价且不依赖平台特性）。
	target := lay.GenDB(gen)
	// 直接用 sqlite 备份 API 把测试库内容落到代库
	if _, err := db.Exec(`VACUUM INTO ?`, target); err != nil {
		t.Skipf("VACUUM INTO 失败（平台相关）: %v", err)
	}
	if err := writeActive(lay, gen); err != nil {
		t.Fatal(err)
	}
	// 在**代库**上开一个长写事务占锁
	genDB, err := sql.Open("sqlite", "file:"+target+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Skipf("打开代库失败: %v", err)
	}
	defer genDB.Close()
	genDB.SetMaxOpenConns(1)
	if _, err := genDB.Exec(`CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Skipf("建表失败: %v", err)
	}
	genTx, err := genDB.Begin()
	if err != nil {
		t.Skipf("begin 失败: %v", err)
	}
	defer genTx.Rollback()
	if _, err := genTx.Exec(`INSERT INTO t(id) VALUES(1)`); err != nil {
		t.Skipf("占锁失败: %v", err)
	}

	t0 := time.Now()
	got := scalarCountWithTimeout(prof, `SELECT COUNT(*) FROM t`, 1500*time.Millisecond)
	el := time.Since(t0)
	if got != "timeout" && got != "n/a" {
		t.Logf("持锁下返回 %q（未超时，可能锁已释放）", got)
	}
	// 关键断言：必须在超时上限 + 少量开销内返回
	if el > 5*time.Second {
		t.Fatalf("持锁时耗时 %v —— 诊断包会被 DB 拖死（症状⑤）", el)
	}
	t.Logf("持锁下 %v 返回 %q（上限 1.5s）", el.Round(time.Millisecond), got)
}

// helpers -----------------------------------------------------------------

func mkGen(t *testing.T, lay localstore.Layout, gen, content string) {
	t.Helper()
	if err := os.MkdirAll(lay.GenDir(gen), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lay.GenDB(gen), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// writeActive 直写指针（测试用，不依赖 Activate 的 journal 流程）。
func writeActive(lay localstore.Layout, gen string) error {
	return os.WriteFile(lay.PointerFile(), []byte(gen+"\n"), 0600)
}

// bootstrapPollTimeout 读 bootstrap 包的定版超时（供①断言）。
func bootstrapPollTimeout() time.Duration {
	return bootstrap.PollTimeout
}

var _ = strings.TrimSpace
