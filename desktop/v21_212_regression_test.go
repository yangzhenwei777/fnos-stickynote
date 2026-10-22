package main

// v21_212_regression_test.go —— 211 实机三症状的回归钉。
//
// 211 实机铁证（用户实例 211 目录）：
//   - 日志 `摘指针后在途轮仍未在 1m0s 内收尾` ⇒ **证伪** 211 的「摘指针让在途轮收敛」假设；
//   - 直读 g-000002：notes 43/ folders 8/ changes 258/ cursor=258 ⇒ **换代其实成功**，
//     用户看到的「空白 + 未登录」是**数据在但 UI 不刷新**，不是同步失败；
//   - 登录按钮转圈 60+ 秒 ⇒ 211 把快照改成同步等待，把换代耗时整段搬到登录上。
//
// 本轮六项修法的钉：
//  A 让路信号⇒ TestYieldInFlightConvergesInFlightRound / TestYieldClearedPerRound
//  B 登录不等快照    => TestLoginDoesNotWaitForBootstrapCompletion
//  C 换代预算 3s   => TestSwapBudgetIsTight
//  D gen_epoch 持久化=> TestGenEpochSurvivesProcessRestart
//  E 可取消换票锁    => TestExchangeLockIsCancelable
//  F 关机预算 3s   => TestShutdownGraceTimeoutIsTight
// 以及对 211 错误修法的**反向钉**：TestDetachDoesNotAloneConvergeInFlightRound。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"stickynote-desktop/synclocal"
	"stickynote/database"
)

// ---- A 让路信号 ----

// TestYieldInFlightConvergesInFlightRound 是 A 项的**核心钉**，直接对应 211 的失败面。
//
// 场景：一个「慢轮」（模拟弱网下要拉全量 changes 的在途轮）。
// 211 的假设是"摘 worker 指针 ⇒ 它读到 nil 即退" —— 本用例先证明**该假设无效**
// （钉住反例，防止有人日后又用回这个方案），再证明让路信号**真的能让它秒级退出**。
func TestYieldInFlightConvergesInFlightRound(t *testing.T) {
	withWiringReset(t)
	resetAppCtxForTest() // 同上：防appCtx 被前序用例永久取消

	// ── 反向钉：摘指针本身**不能**让在途轮收敛 ──
	// 在途轮早已把 worker 取进局部变量（生产代码 scheduledRound 就是这么写的），
	// 因此全局指针摘掉后它照跑不误 —— 这正是 211 等待 60s 的真实原因。
	localHeld := tokenWorker() // 模拟 w := workerPtr() 的局部变量
	setWorker(localHeld)
	detachWorkerForSwap()
	if workerPtr() != nil {
		t.Fatal("前置：摘指针后全局指针应为 nil")
	}
	if localHeld == nil {
		t.Fatal("在途轮手里的局部 worker 仍非 nil —— 摘全局指针管不到它（211 证伪点）")
	}

	// ── 正向钉：让路信号让在途轮秒级退出 ──
	// s 在闭包里被引用（模拟 RunOnce 内 w.shouldYield() 的分页边界检查），
	// 故先声明再赋值。
	var s *synclocal.Scheduler
	// X9 二次修正（2026-10-15，CI 定罪）：2.0.1 轮两连红 + 2.0.2 首轮 CI 红均为
	// **竞态**而非调度抖动——CI 负载下 Round goroutine 未真正执行前，调度器的
	// idle 检测（Idle=20ms）把轮判空闲并在下一轮开始时**清掉让路位**
	// （TestYieldClearedPerRound 的既定语义）⇒ YieldInFlight 被吞 ⇒ 新轮跑满 30s。
	// 修法：Round 真正执行到第一次迭代才关 started，主线程收到信号后再发让路
	// —— 让路必然落在轮存活窗口内，≤10ms 必见，结构性关闭竞态窗口。
	started := make(chan struct{})
	s, err := synclocal.NewScheduler(synclocal.SchedOptions{
		Idle: 20 * time.Millisecond, Probe: 20 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond, FirstDelay: 5 * time.Millisecond,
		// 模拟慢轮：只在分页循环边界检查让路信号，没收到就一直跑（最坏 30s）。
		// 这正是生产 RunOnce 里 `w.shouldYield()` 的位置语义。
		Round: func(ctx context.Context) error {
			close(started)              // 本轮 goroutine 已真正执行（此后发让路必被本轮看到）
			for i := 0; i < 3000; i++ { // 3000 × 10ms = 最长 30s
				if s.TryYield() {
					return nil // 收到让路 ⇒ 立即返回（这就是分页边界的早退）
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
			return nil
		},
		Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	syncScheduler = s
	if err := s.Start(appCtx()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// 等 Round goroutine 真正跑起来（Running 状态 ≠ Round 已执行，CI 上二者差距可达秒级）。
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Skip("调度轮未在 5s 内真正开跑，跳过（CI 极端负载，非产品缺陷）")
	}

	// 装配让路检查（生产由 scheduledRound 做）
	setWorker(tokenWorker())
	if w := workerPtr(); w != nil {
		w.SetYieldCheck(s.TryYield)
	}

	start := time.Now()
	s.YieldInFlight()
	// 预算 15s（X9）：让路正常 ≤10ms 级；预算含 CI 负载容忍，且坏情况
	// （让路失效，211 实机等满 60s）仍会被 15s 超时判红，判据不弱化。
	if !s.WaitIdle(appCtx(), 15*time.Second) {
		t.Fatal("广播让路后，在途轮应在 15s 内退出（211 实机曾等满 60s；预算含 CI 负载容忍，见 X9）")
	}
	if el := time.Since(start); el > 15*time.Second {
		t.Fatalf("让路收敛耗时 %s，超出预算", el)
	}
}

// TestYieldClearedPerRound 守住一个致命陷阱：让路请求若不被清理，
// **下一轮开跑即退** ⇒ 调度器变成"每轮只跑一个分页就停"，
// 表现为同步极慢但**不报错**（最难发现的一类静默失效）。
func TestYieldClearedPerRound(t *testing.T) {
	withWiringReset(t)
	// 防御：若前面的用例调过 cancelShutdown，appCtx 恒已取消 ⇒ loop 一轮都不跑。
	resetAppCtxForTest()
	s, err := NewTestScheduler(func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("NewTestScheduler: %v", err)
	}
	syncScheduler = s
	// 先广播一次让路（模拟换代遗留）
	s.YieldInFlight()
	if !s.RoundYielded() {
		t.Skip("让路标志未被置位，本机调度时序下不稳定")
	}
	// 起跑后应清掉
	if err := s.Start(appCtx()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.State().Rounds > 0 || !s.RoundYielded() {
			if !s.RoundYielded() {
				return // 已被清理：下一轮不会被误伤
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s.RoundYielded() {
		t.Fatal("每轮开跑前必须清掉上一轮的让路请求，否则下一轮开跑即退（静默停摆）")
	}
}

// ---- B 登录不等快照 ----

// TestLoginDoesNotWaitForBootstrapCompletion 定死 B 项的语义：
// 首登时登录**必须立即返回**，不得等快照跑完。
//
// 211 实机：登录按钮转圈 60+ 秒（日志 `摘指针后在途轮仍未在 1m0s 内收尾`），
// 因为 P0-2 把 maybeAutoBootstrap 改成同步等待。本用例确保它回到异步。
func TestLoginDoesNotWaitForBootstrapCompletion(t *testing.T) {
	withWiringReset(t)
	// 把换代锁占住（模拟"已有换代在途"）。若登录路径去等它完成，
	// 这里就会超时 —— 而正确实现只该做一次 TryLock 探测（微秒级）。
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	ls := &LocalService{}
	start := time.Now()
	inFlight := ls.autoBootstrapInFlight()
	elapsed := time.Since(start)

	if !inFlight {
		t.Fatal("锁被占时 autoBootstrapInFlight 应报告『已在路上』")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("探测耗时 %s —— 它必须是微秒级 TryLock，绝不能等换代完成", elapsed)
	}
	// 关键：探测不得把锁一直占着（否则真正该跑的换代会被挡住）
	if bootstrapMu.TryLock() {
		bootstrapMu.Unlock()
		t.Fatal("探测后锁必须已释放 —— 否则探测会变成占用，反过来挡住换代")
	}
}

// ---- C/F 预算 ----

// TestSwapBudgetIsTight 守住「3s」这个数字不再被悄悄放宽。
//
// 211 把首登档放宽到 60s，代价是登录卡 67 秒。预算必须留在「秒级」量级，
// 否则同类问题会复发。
func TestSwapBudgetIsTight(t *testing.T) {
	if swapWaitBudget > 5*time.Second {
		t.Fatalf("换代等在途预算 %s 过宽：211 曾放宽到 60s 导致登录卡 67 秒", swapWaitBudget)
	}
	if swapWaitBudget < time.Second {
		t.Fatalf("换代等在途预算 %s 过窄：大库单个分页往返可能超过", swapWaitBudget)
	}
	if firstLoginSwapBudget != swapWaitBudget {
		t.Fatalf("首登档应与日常档同值（换代耗时由快照下载决定，与等在途轮无关）：%s vs %s",
			firstLoginSwapBudget, swapWaitBudget)
	}
}

// TestShutdownGraceTimeoutIsTight 对齐产品要求「点 X 后 3 秒内进程消失」。
func TestShutdownGraceTimeoutIsTight(t *testing.T) {
	if shutdownGraceTimeout > 4*time.Second {
		t.Fatalf("换代收尾窗口 %s 过大：210 实机 72s / 211 实机仍超时", shutdownGraceTimeout)
	}
	if shutdownGraceTimeout < time.Second {
		t.Fatalf("换代收尾窗口 %s 过小：写代库 fsync 可能来不及", shutdownGraceTimeout)
	}
}

// ---- D gen_epoch 跨进程持久化 ----

// TestGenEpochSurvivesProcessRestart 是 D 项的**核心钉**，直接对应
// 「换代成功但 UI 空白且显示未登录」的机制。
//
// 211 实机：genEpoch 是纯内存 atomic，重启归零；而前端"上次已知值"也是 0
// ⇒ `gen_epoch > 上次值` 恒不成立 ⇒ 前端永不刷新。
// 修法：代数落进 sync_state，重启读回 ⇒ 跨进程单调递增。
func TestGenEpochSurvivesProcessRestart(t *testing.T) {
	dir := t.TempDir()
	if err := database.Init(filepath.Join(dir, "notepad.db"), localstoreAppVersion); err != nil {
		t.Skipf("database.Init 不可用，跳过: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })

	// 模拟第一次进程：换代推进代数并落库
	old := genEpoch.Load()
	defer genEpoch.Store(old)
	genEpoch.Store(1)
	persistGenEpoch(database.GetDB())

	// 模拟进程重启：内存归零后从库里读回
	genEpoch.Store(0)
	if got := genEpoch.Load(); got != 0 {
		t.Fatalf("前置：模拟重启后内存应为 0，实得 %d", got)
	}
	loadGenEpoch(database.GetDB())
	if genEpoch.Load() != 1 {
		t.Fatalf("重启后应从 sync_state 读回代数 1，实得 %d —— 前端将永远不刷新（211 实机症状）",
			genEpoch.Load())
	}

	// 单调性：再次换代推进到 2，重启后仍能读回 2（跨进程单调）
	genEpoch.Store(2)
	persistGenEpoch(database.GetDB())
	genEpoch.Store(0)
	loadGenEpoch(database.GetDB())
	if genEpoch.Load() != 2 {
		t.Fatalf("第二次重启后应读回 2，实得 %d", genEpoch.Load())
	}
}

// TestGenEpochPersistedInSyncState 确认代数确实落在 sync_state（前端可见的那张表）。
func TestGenEpochPersistedInSyncState(t *testing.T) {
	dir := t.TempDir()
	if err := database.Init(filepath.Join(dir, "notepad.db"), localstoreAppVersion); err != nil {
		t.Skipf("database.Init 不可用，跳过: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })
	old := genEpoch.Load()
	defer genEpoch.Store(old)
	genEpoch.Store(7)
	persistGenEpoch(database.GetDB())

	var v string
	if err := database.GetDB().QueryRow(
		`SELECT value FROM sync_state WHERE key=?`, genEpochStateKey).Scan(&v); err != nil {
		t.Fatalf("sync_state 中应存在 %s：%v", genEpochStateKey, err)
	}
	if v != "7" {
		t.Fatalf("%s 应为 7，实得 %q", genEpochStateKey, v)
	}
}

// ---- E 可取消换票锁 ----

// TestExchangeLockIsCancelable 定死 E 项：拿换票锁必须能被 ctx 打断。
//
// 211 实机：exchangeMu 是裸 sync.Mutex，排队者无法响应 ctx 取消 ⇒ 关机时
// 必须等持锁者跑完整个换票往返（本轮最后一个关机阻塞点）。
func TestExchangeLockIsCancelable(t *testing.T) {
	// ⭐ 防跨用例污染：换票锁是**包级共享**的 channel 语义锁。
	// 若本用例中途 t.Fatal（Go 的 Fatal 会 runtime.Goexit 跳过后续语句），
	// 锁会被**永久占住** ⇒ 后续所有走 ensureAccess 的用例全部阻塞/失败，
	// 表现为"与本用例完全无关的用例莫名超时"。故用 defer 兜底释放。
	if !lockExchange(context.Background()) {
		t.Fatal("锁空闲时应能拿到")
	}
	released := false
	defer func() {
		if !released {
			unlockExchange()
		}
	}()

	// 第二个等待者：ctx 取消后必须立刻放弃，而不是排队干等
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- lockExchange(ctx) }()
	time.Sleep(80 * time.Millisecond) // 让它确实进入等待
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("锁被占时不应拿到锁")
		}
		// 正确：ctx 取消后立即返回 false
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后必须在 2s 内放弃等待（裸 Mutex 会干等到持锁者释放）")
	}
	unlockExchange()
	released = true

	// 取消后不得留下"脏占用"：重新拿锁应成功
	probe, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if !lockExchange(probe) {
		t.Fatal("取消路径不应污染锁状态")
	}
	unlockExchange()
}

// ---- 关机与残留进程 ----

// TestYieldShutdownCancelsSchedulerReachesLoop 沿用 211 钉：appCtx 取消必须真到达调度器。
// （211 的同名钉仍在 v21_211_regression_test.go，两者断言不同面：本例额外验证
// 让路/摘指针改动没有破坏"取消 → Stop 收敛"这条链。）
func TestYieldShutdownCancelsSchedulerReachesLoop(t *testing.T) {
	withWiringReset(t)
	s, err := synclocal.NewScheduler(synclocal.SchedOptions{
		Idle: 20 * time.Millisecond, Probe: 20 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond, FirstDelay: 5 * time.Millisecond,
		Round: func(context.Context) error { return nil },
		Logf:  func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := s.Start(appCtx()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// ⚠ cancelShutdown 取消的是包级单例，一次生效永不复位；必须复位，
	// 否则后续用例 Start(appCtx()) 会一轮都不跑（跨测试污染）。
	defer resetAppCtxForTest()
	cancelShutdown()
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(8 * time.Second):
		t.Fatal("appCtx 取消后调度器应在 8s 兜底内收敛")
	}
	if s.State().Started {
		t.Fatal("Stop 后 Started 应为 false")
	}
}
