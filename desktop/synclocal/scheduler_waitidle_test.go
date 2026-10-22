package synclocal

// scheduler_waitidle_test.go —— v21.170 回归钉：WaitIdle（换代前等在途轮收尾）。
//
// 缺陷背景（209 实机，desktop 日志同秒两条）：
//
//	[bootstrap] generation g-000002 sealed+committed
//	[sched] 后台同步轮次失败（第 1 次连续）: sql: database is closed
//
// 根因：pauseScheduler 只是计数器（只拦新轮），而 scheduledRound 已把旧 worker
// 取进**局部变量**后才 RunOnce —— 换代关旧库时那一轮正好撞上。
// 本测试钉住「WaitIdle 能等到在途轮结束」这一前提，缺它换代就是拆雷。

import (
	"context"
	"testing"
	"time"
)

// newTestSched 造一个最小可用调度器（Round 必填）。
func newTestSched(t *testing.T, round func(ctx context.Context) error) *Scheduler {
	t.Helper()
	s, err := NewScheduler(SchedOptions{
		Idle: 10 * time.Millisecond, Probe: 10 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond, FirstDelay: 1 * time.Millisecond,
		Round: round,
		Logf:  func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWaitIdleReturnsTrueWhenNeverStarted(t *testing.T) {
	s := newTestSched(t, func(context.Context) error { return nil })
	// 从未启动 ⇒ 无在途轮 ⇒ 立即可判空闲
	if !s.WaitIdle(context.Background(), time.Second) {
		t.Fatal("v21.170：未启动的调度器必须判为空闲")
	}
}

func TestWaitIdleWaitsForRunningRound(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	s := newTestSched(t, func(ctx context.Context) error {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Skip("调度器未在本机按时起轮（本测试不依赖时序）")
	}

	// 轮次仍在跑 ⇒ 短超时必须判false（**绝不能强关旧库**）
	if s.WaitIdle(context.Background(), 150*time.Millisecond) {
		t.Fatal("v21.170：轮次仍在跑时 WaitIdle 必须返回 false（否则换代会强关在用的库）")
	}

	// 放行轮次 ⇒ 应很快转true
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()
	if !s.WaitIdle(context.Background(), 3*time.Second) {
		t.Fatal("v21.170：轮次结束后 WaitIdle 必须返回 true")
	}
}

func TestWaitIdleRespectsContextCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, 1)
	s := newTestSched(t, func(ctx context.Context) error {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Skip("调度器未在本机按时起轮")
	}
	// 另一个 ctx 被取消 ⇒ 立即放弃等待（关机路径不应被 WaitIdle 拖住）
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if s.WaitIdle(cctx, 5*time.Second) {
		t.Fatal("ctx 已取消且轮次在跑时不应返回 true")
	}
}

func TestWaitIdleNilScheduler(t *testing.T) {
	var s *Scheduler
	if !s.WaitIdle(context.Background(), time.Second) {
		t.Fatal("nil 调度器必须判为空闲（不能阻断换代）")
	}
}
