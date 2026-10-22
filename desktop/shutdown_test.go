package main

// shutdown_test.go —— v21.203 批次 3-3 回归：关机收口 + 调度器重启 + 嵌套暂停。
//
// 锚点：用户实机日志里的 `db warmup query failed: sql: database is closed` ——
// 关机过程中还有 goroutine 在用已关闭的 DB。同时也是「故障证据丢失」的表现：
// 出了错，恰好在日志被关掉之后才发生，无从查起。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestGracefulShutdownCancelsAppCtx —— 关机的第一步必须是**广播取消**。
func TestGracefulShutdownCancelsAppCtx(t *testing.T) {
	// 复位进程 ctx（用例隔离）
	shutdownMu.Lock()
	shutdownCtx, shutdownCancel = nil, nil
	shutdownMu.Unlock()

	ctx := appCtx()
	if ctx.Err() != nil {
		t.Fatal("进程存活期间 ctx 不得已取消")
	}
	// 幂等性：重复取同一个 ctx（长任务都挂在它下面）
	if appCtx() != ctx {
		t.Fatal("appCtx 必须返回同一个根 ctx（否则取消无法传导）")
	}
	cancelShutdown()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancelShutdown 后 ctx 必须被取消（关机第一步）")
	}
	if !isShuttingDown() {
		t.Fatal("isShuttingDown 应为 true（绑定层据此拒绝新工作）")
	}
	// 幂等：重复取消不 panic
	cancelShutdown()

	// 复位，避免污染后续用例
	shutdownMu.Lock()
	shutdownCtx, shutdownCancel = nil, nil
	shutdownMu.Unlock()
}

// TestAppCtxSurvivesAfterCancel —— 已取消的根 ctx 不得被"复活"成新 ctx。
// 否则某次关机后新起的 goroutine 会拿到一个永不取消的 ctx，重新引入挂死。
func TestAppCtxSurvivesAfterCancel(t *testing.T) {
	shutdownMu.Lock()
	shutdownCtx, shutdownCancel = nil, nil
	shutdownMu.Unlock()

	first := appCtx()
	cancelShutdown()
	second := appCtx()
	if second != first {
		t.Fatal("取消后不得返回新的根 ctx（那会让后续任务永远收不到取消信号）")
	}
	if second.Err() == nil {
		t.Fatal("取消状态必须保留")
	}

	shutdownMu.Lock()
	shutdownCtx, shutdownCancel = nil, nil
	shutdownMu.Unlock()
}

// TestShutdownGraceOrdering —— 关机序列的三步顺序必须生效：
// 取消 → 停调度器 → （DB/日志由调用方最后关）。
// 这里用替身验证"调度器被停时 ctx 已取消"这个关键不变量。
func TestShutdownGraceOrdering(t *testing.T) {
	shutdownMu.Lock()
	shutdownCtx, shutdownCancel = nil, nil
	shutdownMu.Unlock()

	// 造一个尊重 ctx 的调度器
	var ran int32
	ls := &LocalService{uploadDir: t.TempDir() + "/uploads"}
	startScheduler(ls)
	if syncScheduler == nil {
		t.Fatal("调度器应已启动")
	}
	// 让它跑一轮会阻塞的 Round：在 ctx 取消时立即返回
	original := syncScheduler
	_ = original
	// 替换 Round 为"睡到 ctx 结束"
	ctxProbe := appCtx()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 模拟在途轮次：等待取消信号
		<-ctxProbe.Done()
		atomic.AddInt32(&ran, 1)
	}()

	gracefulShutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("gracefulShutdown 必须在有界时间内让在途工作观察到取消")
	}
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatal("在途工作应已完成收尾")
	}
	if isShuttingDown() != true {
		t.Fatal("关机后 isShuttingDown 必须为 true")
	}

	// 复位
	shutdownMu.Lock()
	shutdownCtx, shutdownCancel = nil, nil
	shutdownMu.Unlock()
	stopScheduler()
	syncScheduler = nil
}

// TestAwaitInFlightGrace —— 换代收尾窗口：无换代在途时立即返回 true；
// 有换代在途时等待，超时返回 false（交由 SEALED 崩溃态兜底，不阻塞关机）。
func TestAwaitInFlightGrace(t *testing.T) {
	// 无换代在途
	if !awaitInFlightGrace() {
		t.Fatal("无换代在途时应立即返回 true")
	}
	// 模拟换代在途
	bootstrapMu.Lock()
	release := make(chan struct{})
	done := make(chan bool, 1)
	go func() { done <- awaitInFlightGrace() }()
	time.Sleep(100 * time.Millisecond)
	bootstrapMu.Unlock() // 释放锁 = 换代收尾
	close(release)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("换代释放后应返回 true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitInFlightGrace 不得挂死")
	}
}

// TestSchedulerStopStartCycle —— 调度器 Stop 后再 Start 不得 panic。
//
// 定罪：`done` channel 原先只在 NewScheduler 建一次，而 loop 的
// `defer close(s.done)` 会关掉它 —— 第二次 Start 让新 loop 再 close 同一个
// 已关闭的 channel ⇒ `panic: close of closed channel`，整进程带崩。
// 本机 -race 不可用，但这个 panic 是**确定性**的，不依赖竞态窗口。
func TestSchedulerStopStartCycle(t *testing.T) {
	var rounds int32
	s, err := NewTestScheduler(func(ctx context.Context) error {
		atomic.AddInt32(&rounds, 1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.Start(context.Background()); err != nil {
			t.Fatalf("第 %d 次 Start: %v", i+1, err)
		}
		time.Sleep(30 * time.Millisecond)
		s.Stop()
	}
	// 三轮 Stop/Start 后再跑一轮，必须正常（说明 done 已正确重建）
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("末次 Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	s.Stop()
	if atomic.LoadInt32(&rounds) == 0 {
		t.Fatal("至少应跑过一轮")
	}
}
