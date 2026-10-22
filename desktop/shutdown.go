package main

// shutdown.go —— v21.203 批次 3-3：关机顺序收口。
//
// 定罪（审计 C 项 + 真实危害）：
//
//  ① **关机不通知在途工作**：`OnShutdown` 只调 `stopScheduler()`，而
//     `Scheduler.Stop()` 最多等 35 秒就放弃 —— 放弃之后**调度 goroutine 与
//     正在跑的 bootstrap goroutine 仍在运行**，而 `defer database.GetDB().Close()`
//     与日志关闭已经执行。于是：
//     - 写进已关闭 DB → "sql: database is closed"（用户实机日志里确实有这一行）；
//     - 写进已关闭日志 → **故障证据丢失**（最难查的一类：出问题时日志正好没了）。
//
//  ② **窗口内被杀 = SEALED 崩溃态**：换代后置步骤期间收到关机信号，`defer`
//     不保证执行（进程直接结束）⇒ 下次启动 Recover 按「后置未确认」回退。
//     这本身是批次 1 修好的（安全回退），但意味着**每次正常关机都可能丢一次换代**
//     —— 修法不该是"接受回退"，而是"让换代有机会跑完收尾"。
//
// 收口设计（三步，顺序不可换）：
//
//	① cancelShutdown() —— 广播取消：调度轮与换代链的每个可阻塞点立刻返回；
//	   在途 HTTP 请求被 ctx 断掉，不会继续占用 socket 与磁盘写。
//	② stopScheduler()  —— 等在途轮次真正收尾（此时它们已经拿到 ctx，会很快返回）。
//	③ 关 DB / 日志     —— **必须最后**，因为前两步仍可能写日志与 DB。
//
// 为什么不用 `sync.WaitGroup` 全局等所有 goroutine：那会与"取消"重复，
// 且第三方/未来新增的长任务不该被无界阻塞关机。取消 + 有界等待是正解。

import (
	"context"
	"log"
	"sync"
	"time"

	"stickynote-desktop/synclocal"
)

var (
	shutdownMu     sync.Mutex
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
)

// appCtx 返回进程级上下文（首次调用时创建）。
//
// 语义：**进程存活期间永不取消**；只有 `cancelShutdown` 会取消它。
// 所有长任务都用它做根 ctx（并各自派生带超时的子 ctx）。
func appCtx() context.Context {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	if shutdownCtx == nil {
		shutdownCtx, shutdownCancel = context.WithCancel(context.Background())
	}
	return shutdownCtx
}

// cancelShutdown 广播取消（幂等）。关机第一步调用。
func cancelShutdown() {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	if shutdownCancel != nil {
		shutdownCancel()
	}
}

// isShuttingDown 供绑定层在入口处快速拒绝新工作（避免关机过程中又起一轮换代）。
func isShuttingDown() bool {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	return shutdownCtx != nil && shutdownCtx.Err() != nil
}

// resetAppCtxForTest 重置进程级 ctx（**仅测试用**）。
//
// 为什么需要：`shutdownCtx` 是**包级单例**，而 `cancelShutdown()` 是**一次性**的
// —— 任何调用过它的测试之后，`appCtx()` 恒返回已取消的 ctx，
// 于是后续 `Start(appCtx())` 的调度器**一轮都不会跑**（loop 首件事就是
// `select ctx.Done() → return`）。表现为"与调度相关的用例莫名超时/状态不推进"，
// 且**与被测代码无关** —— 典型的跨测试污染。
//
// 生产不调用它：进程一生只关机一次，永久取消正是所需语义。
func resetAppCtxForTest() {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	shutdownCtx, shutdownCancel = nil, nil
}

// gracefulShutdown 关机收口序列。main 的 OnShutdown **只应调它**。
//
// 步骤与理由见文件头。返回时 DB 与日志仍然可写 —— 它们的关闭由调用方在
// 本函数返回后进行（顺序：gracefulShutdown() → 清理 WebView2 → 关 DB → 关日志）。
func gracefulShutdown() {
	// ① 先广播取消：让所有可取消的长任务尽快退出（不等它们 35s）。
	cancelShutdown()

	// ①.8 v21.217c：关闭换代遗留的旧代句柄（此刻前端已死、无并发读者，安全窗口）。
	if db := leftoverOldGen.Swap(nil); db != nil {
		_ = db.Close()
		log.Printf("[shutdown] 换代遗留旧代句柄已关闭")
	}

	// ② 停调度器：此时在途轮次已收到 ctx 取消，会很快返回。
	//    有界等待（Scheduler.Stop 内部 60s 兜底）—— 真超时也只是"日志里多一行
	//    未在预期时限内退出"，不会卡死关机。
	// ①.5 ⭐ v21.215（211d 实机铁证·第二次启动必 AUTH_REVOKED 的真正源头）：
	//
	// 定罪（211d 日志三行）：
	//
	//	13:54:37 [auth] device session restored        ← 第2次启动，首轮换票开始（网络慢一直在途）
	//	13:55:18 [sched] 调度器未在预期时限内退出      ← 用户关窗，8s 兜底**放弃等待** → 进程退出
	//	13:55:21 [auth] 凭据被服务端拒绝（AUTH_REVOKED）← 第3次启动 2 秒内必死
	//
	// 机制：refresh 是「**一次一换**」——换票请求一旦抵达服务端，服务端**已轮转
	// 并吊销旧票**。v21.213 给换票加了 `WithoutCancel`（不被 ctx 取消），方向正确，
	// 但**进程退出不等 goroutine**：Stop 的 8s 兜底一到，main 返回、进程终止，
	// **正在途的换票 goroutine 被拦腰杀死** —— 服务端已作废旧票、新票随进程
	// 死亡丢失 ⇒ 本地封存票必然失效 ⇒ 下次启动必 401。
	// （第1次关窗没死，是因为那会儿在途的是 RunOnce 的 Pull——ctx 可取消，
	//  被正常取消返回了；换票早在登录时就完成落盘。第2次恰好撞上换票在途。）
	//
	// 修法：**关机必须等在途换票跑完再退**。换票全程持 exchangeMu，且
	// `SaveRefreshCAS`（新票落盘）在锁内执行 —— **锁空闲 = 要么没换票，要么
	// 新票已安全落盘**。故用「取到锁再立即释放」作为"换票已收尾"的判定。
	// 上限 16s = Refresh 15s 超时 + 落盘余量；正常路径（无换票在途）零等待。
	//
	// 代价：关窗最多多等 15s（仅当换票恰好在途）。正确性优先——一次必然掉登录
	// 比一次 15s 的关机延迟严重得多（v21.213 已确立该取舍）。
	waitExchangeDrain(16 * time.Second)

	stopScheduler()

	// ③ 显式敲一次日志：证明日志通道此刻仍可用（若这一步写不进去，
	//    说明关闭顺序仍有问题，是一道自检）。
	log.Printf("[shutdown] 取消已广播、换票已收尾、调度器已收敛（预算 %s）；DB 与日志留待调用方关闭",
		shutdownGraceTimeout)
}

// awaitInFlightGrace 在关机时给在途换代链的有界收尾窗口。
//
// 为什么需要：换代（BootstrapNow）由 UI 同步调用或 goroutine 触发，**不受
// scheduler.Stop 管辖**（它有自己的 bootstrapMu）。关机时若换代正在写代库，
// 我们必须等它把「后置步骤 + SealCommitted」跑完 —— 否则就是批次 1 里那个
// SEALED 崩溃态（安全回退，但用户白等一次换代）。
//
// 用法：关机路径上在 cancelShutdown 之后、关 DB 之前调用。
// 返回 true = 换代已收尾（窗口内拿到锁并确认空闲）；false = 仍在跑/超时，
// 交由 SEALED 崩溃态语义兜底（安全，不会丢数据）。
func awaitInFlightGrace() bool {
	deadline := time.Now().Add(shutdownGraceTimeout)
	for time.Now().Before(deadline) {
		if bootstrapMu.TryLock() {
			// 拿到锁 = 此刻无换代在途（TryLock 语义：持锁即独占）
			bootstrapMu.Unlock()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// shutdownGraceTimeout 关机时给在途工作的总预算。
//
// ⭐ v21.212：5s → 3s（对齐「点 X 后 3 秒内进程消失」的产品要求）。
// 211 的 5s 仍嫌宽，因为 211 实机证明在途轮**没有**真秒级收敛——
// 当时的两个前提虽已修（绑 appCtx + 四个 *Ctx 变体），但还有两处阻塞：
//
//	① 在途轮用 `w := workerPtr()` 局部变量跑完整轮，摘指针无效（本轮加让路信号根治）；
//	② `exchangeMu` 是裸 sync.Mutex，排队者无法响应 ctx 取消（本轮改 channel 语义锁）。
//
// 两处修完后在途轮已是**真·秒级**收敛，3s 留足余量。
// 且它是**上限**而非固定开销 —— 正常路径都在百毫秒级。
const shutdownGraceTimeout = 3 * time.Second

// NewTestScheduler 构造一个节奏极短的调度器（**仅测试用**）。
//
// 放在非测试文件里的理由：desktop 包的 shutdown_test.go 需要跨包驱动
// 「Stop → 再 Start」的确定性回归（done channel 复用会 panic），
// 而  里的导出对其他包不可见。
func NewTestScheduler(round func(context.Context) error) (*synclocal.Scheduler, error) {
	return synclocal.NewScheduler(synclocal.SchedOptions{
		Idle: 20 * time.Millisecond, Probe: 20 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond, FirstDelay: 5 * time.Millisecond,
		Round: round, Allow: func() bool { return true },
	})
}

// waitExchangeDrain 等换票锁回到空闲（= 无在途换票，或其新票已落盘）。
// 有界：超时放行（与既有 Stop 兜底同一哲学——不让关机卡死，残余风险交给
// 「AUTH_REVOKED 不清票 + 重登自愈」兜底）。
func waitExchangeDrain(max time.Duration) {
	deadline := time.Now().Add(max)
	for {
		// ⚠ 必须用**带超时的探测**：`lockExchange(context.Background())` 在锁被
		// 长期持有时会**永久阻塞**（channel 发送无超时分支），max 上限形同虚设
		// —— 本函数第一次实现就栽在这里（测试实测卡死 >4 分钟才暴露）。
		probe, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		got := lockExchange(probe)
		cancel() // 每条路径都释放（vet: possible context leak）
		if got {
			unlockExchange()
			return // 锁空闲：换票已收尾（或从未开始）
		}
		if time.Now().After(deadline) {
			// ⭐ v21.177 文案纠偏（216 实机证伪旧文案的暗示）：真凶是「某同步/DB 操作卡住并
			// 持锁」（如连接池死锁），与凭据无关——旧文案"下次启动若 401 请重新登录"把用户
			// 引向反复重登、制造恐慌。改为事实描述 + 明确凭据不受影响。
			log.Printf("[shutdown] 换票锁被占用超过 %s（通常=有同步或数据库操作被长阻塞），已放弃等待；本机登录凭据未受影响", max)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
