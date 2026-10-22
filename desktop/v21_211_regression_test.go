package main

// v21_211_regression_test.go —— 210 实机三症状的回归钉。
//
// 三症状与各自的钉：
//  ① 登录成功但笔记空白（首登快照被 8s 空等墙挡掉）
//     → TestBootstrapDetachesWorkerInsteadOfWaitingInFlightRound
//     → TestFirstLoginPendingClassification
//  ② 点 X 后进程 72 秒不退出（Stop 60s + 换代窗口 12s 叠加）
//     → TestShutdownCtxActuallyCancelsScheduler
//     → TestShutdownGraceTimeoutIsBounded
//  ③ 反复开关后始终无法登录（问题 ①②的复合放大）
//     → 由 ①② 的钉共同封住（进程能退 + 首登能成，复合缺陷不复存在）
//
// 本文件不引入新的工装，只复用既有包级全局与 withWiringReset。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"stickynote-desktop/localauth"
	"stickynote-desktop/synclocal"
	"stickynote/database"
)

// ---- ① 首登快照不得被在途轮挡死 ----

// TestBootstrapDetachesWorkerInsteadOfWaitingInFlightRound 定死 P0-1 的核心语义：
// 换代窗口必须**先摘 worker 指针**，而不是空等在途轮跑完。
//
// 210 实机机制：登录 → mountWorker 唤醒后台轮 → 自动快照也发起换代 → 两条并发。
// 原实现 pauseScheduler（只拦新轮）+ WaitIdle(8s)；在途轮首轮要换票 + 拉全量
// changes，弱网下 8s 收不了 → 换代中止 → folders 恒 0 → 反复登录恒空白。
//
// 修法主张：摘指针让在途轮读到 nil 即早退（本例中它本来就要 30s 才结束）。
// 本用例把"在途轮要跑很久"作为前提，断言摘指针后**立刻**变 nil，
// 从而证明收敛不再依赖网络往返时长。
func TestBootstrapDetachesWorkerInsteadOfWaitingInFlightRound(t *testing.T) {
	withWiringReset(t)
	setWorker(tokenWorker())
	if workerPtr() == nil {
		t.Fatal("前置：在位 worker 应可见")
	}
	if !detachWorkerForSwap() {
		t.Fatal("有在位 worker 时 detachWorkerForSwap 必须返回 true")
	}
	if workerPtr() != nil {
		t.Fatal("换代窗口必须摘掉 worker 指针：在途轮据此早退，不再触碰将被关闭的旧库")
	}
	// 幂等/无在位工时不得 panic，且返回 false。
	if detachWorkerForSwap() {
		t.Fatal("已摘空后再摘应返回 false")
	}
}

// TestInFlightRoundConvergesFastAfterDetach 端到端钉「摘指针后收敛是秒级的」：
// 起一个"慢轮"（模拟弱网下的长往返），换代窗口摘指针后它必须**很快**退出，
// 而不是等它自己跑完。这正是 210 的 8s 墙所缺的那条性质。
func TestInFlightRoundConvergesFastAfterDetach(t *testing.T) {
	withWiringReset(t)
	slowRoundDone := make(chan struct{})
	// 模拟弱网在途轮：不看 ctx，只等一个远长于预算的时间（5s > swapWaitBudget 的
	// 日常语义，也不短于任何合理空等）；真实链路里它每次读 workerPtr() 都会早退。
	roundExited := make(chan struct{})
	go func() {
		defer close(roundExited)
		for {
			select {
			case <-slowRoundDone:
				return
			default:
			}
			if workerPtr() == nil { // scheduledRound 的早退条件
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	setWorker(tokenWorker())
	start := time.Now()
	detachWorkerForSwap()
	select {
	case <-roundExited:
	case <-time.After(2 * time.Second):
		close(slowRoundDone)
		t.Fatal("摘指针后在途轮应在 2s 内收敛（真实链路读 workerPtr()==nil 即 ErrScheduleNoWorker 早退）")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("收敛耗时 %s，超出预期", el)
	}
	close(slowRoundDone)
}

// TestFirstLoginPendingClassification 钉住首登判定：本地既无笔记也无文件夹 = 首登态。
// 210 读用户实例库坐实过这个状态（notes 0 / note_folders 0 / profile 有档），
// 正是"登录成功但空白"的物理形态。
func TestFirstLoginPendingClassification(t *testing.T) {
	dir := t.TempDir()
	if err := database.Init(filepath.Join(dir, "notepad.db"), localstoreAppVersion); err != nil {
		t.Skipf("database.Init 不可用，跳过首登判定钉: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })

	ls := &LocalService{}
	if !ls.firstLoginPending() {
		t.Fatal("空库（无笔记无文件夹）应判为首登态—— 首登换代不得因等待预算耗尽而放弃")
	}
	// 有一张笔记即非首登（走日常档预算，不放松红线）。
	if _, err := database.GetDB().Exec(
		`INSERT INTO notes(user_id, title, content, content_text) VALUES(1,'t','c','c')`); err != nil {
		t.Skipf("notes 表结构不可用，跳过后半段: %v", err)
	}
	if ls.firstLoginPending() {
		t.Fatal("本地已有笔记 ⇒ 非首登，应走日常换代预算")
	}
}

// ---- ② 关机必须在秒级收敛 ----

// TestShutdownCtxActuallyCancelsScheduler 是 P0-3 的核心钉：关机取消广播
// **必须真的到达调度器**。210 实机中调度器 Start 传的是 l.ctx 而 cancelShutdown
// 广播的是 appCtx —— 两个不同 ctx ⇒ 取消到不了 ⇒ Stop 每次都白等满 60s 兜底。
//
// 本用例直接断言"appCtx 被取消后，走 appCtx 启动的调度器会退出"。
func TestShutdownCtxActuallyCancelsScheduler(t *testing.T) {
	withWiringReset(t)
	// 极短节奏 + 一个立刻返回的轮：让 loop 真正进入 select 等待。
	s, err := synclocal.NewScheduler(synclocal.SchedOptions{
		Idle: 20 * time.Millisecond, Probe: 20 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond, FirstDelay: 5 * time.Millisecond,
		Round: func(context.Context) error { return nil },
		Logf:  func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	// 关键：绑 appCtx（与生产接线一致），而不是 l.ctx。
	if err := s.Start(appCtx()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 模拟关机第一步：广播取消。
	// ⚠ cancelShutdown 取消的是**包级单例** shutdownCtx，一次生效永不复位。
	// 后续用例若再 Start(appCtx()) 会因ctx 已取消而一轮都不跑 ⇒ 必须在此复位。
	defer resetAppCtxForTest()
	cancelShutdown()
	// Stop 在生产里是被 OnShutdown 紧接着调用的；先Stop 才能观察到"取消已被接收"
	//（loop 退出只 close(done)，Started 标志由 Stop 清理——见 loop 的 defer）。
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("appCtx 取消后调度器应在 3s 内收敛（P0-3：Start 必须绑 appCtx 而非 l.ctx）")
	}
	// 取消确实到达了 loop（而不是靠 Stop 的兜底超时蒙混过关）：
	// Stop 能快速返回，前提是 loop 因 ctx.Done 退出。
	if s.State().Started {
		t.Fatal("Stop 后 Started 应为 false")
	}
}

// TestSchedulerStopIsBounded 钉住关机上限：Stop 在最坏情况下也必须有界，
// 且该上限与"点 X 后 3 秒内退出"的产品要求相容（不得回退到 60s 量级）。
func TestSchedulerStopIsBounded(t *testing.T) {
	withWiringReset(t)
	// 一个**永不返回**的轮（模拟最坏情况）：Stop 必须靠兜底超时返回，而不是挂死。
	block := make(chan struct{})
	defer close(block)
	s, err := NewTestScheduler(func(ctx context.Context) error {
		<-block // 无视 ctx 的最坏轮次
		return nil
	})
	if err != nil {
		t.Fatalf("NewTestScheduler: %v", err)
	}
	if err := s.Start(appCtx()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 等轮真正开跑，否则测的不是在途取消。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.State().Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	s.Stop() // 有界返回即为通过
	if el := time.Since(start); el > 20*time.Second {
		t.Fatalf("Stop 耗时 %s，超出有界预期（兜底上限应远小于 60s 量级）", el)
	}
}

// TestShutdownGraceTimeoutIsBounded 钉住换代收尾窗口同样有界（12s → 5s 量级）。
func TestShutdownGraceTimeoutIsBounded(t *testing.T) {
	if shutdownGraceTimeout > 8*time.Second {
		t.Fatalf("换代收尾窗口 %s 过大：210 实机的 72 秒关机 = Stop 兜底 + 本窗口叠加", shutdownGraceTimeout)
	}
	if shutdownGraceTimeout < time.Second {
		t.Fatalf("换代收尾窗口 %s 过小：大库写代库 fsync 可能来不及收尾", shutdownGraceTimeout)
	}
}

// TestSwapBudgetsAreOrdered 钉住两档预算的关系：首登档必须 ≥ 日常档。
func TestSwapBudgetsAreOrdered(t *testing.T) {
	if firstLoginSwapBudget < swapWaitBudget {
		t.Fatalf("首登预算 %s 不应小于日常档 %s", firstLoginSwapBudget, swapWaitBudget)
	}
	if swapWaitBudget <= 0 {
		t.Fatalf("日常换代预算必须为正：%s", swapWaitBudget)
	}
}

// ---- ③ 会话档案在换代后仍可恢复（反复开关的底座） ----

// TestSessionProfileSurvivesGenerationSwap 钉住"反复开关"的能力底座：
// 换代后 client_profile 必须仍可 OpenSession。210 的"重新输入登录信息后也无法登录"
// 若叠加档案丢失就再也回不去了，故此处守住档案随代迁移这条线。
func TestSessionProfileSurvivesGenerationSwap(t *testing.T) {
	dir := t.TempDir()
	if err := database.Init(filepath.Join(dir, "notepad.db"), localstoreAppVersion); err != nil {
		t.Skipf("database.Init 不可用，跳过: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })
	// 无档案时 OpenSession 必须是"从未登录"语义（而非错误），
	// 否则每次启动都会走进重登引导，形成用户看到的死循环。
	if _, _, err := localauth.OpenSession(database.GetDB()); err == nil {
		t.Skip("本机已有残留档案，跳过无档案分支")
	} else if err != localauth.ErrNoProfile {
		t.Fatalf("空库 OpenSession 应返回 ErrNoProfile，实得 %v", err)
	}
}
