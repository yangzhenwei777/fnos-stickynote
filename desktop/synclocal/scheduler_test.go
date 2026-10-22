// scheduler_test.go —— 后台同步调度器（批次 8-Alt 刀 3）。
//
// 这里测的是"节奏与状态机"，不是网络：Round 全部注入假实现，时长用毫秒级 + FirstDelay 缩短，
// 保证 CI 上稳定。要钉的四件事：单飞不并行、Wake 合并不叠加、三类失败走三种节奏、
// 终态停手后只有用户动作能再试（否则就是周期性打服务端）。
package synclocal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func waitFor(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待超时: " + msg)
}

func TestSchedulerRejectsMissingRound(t *testing.T) {
	if _, err := NewScheduler(SchedOptions{}); err == nil {
		t.Fatal("Round 缺失必须建不出来（不允许静默什么都不做）")
	}
	if _, err := NewScheduler(SchedOptions{Round: func(context.Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerFirstRoundThenIdleTicks(t *testing.T) {
	var rounds int32
	s, err := NewScheduler(SchedOptions{
		Idle: 25 * time.Millisecond, Probe: 5 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond, FirstDelay: 5 * time.Millisecond,
		Round: func(context.Context) error { atomic.AddInt32(&rounds, 1); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&rounds) >= 3 }, 3*time.Second, "定时轮次未推进")
	st := s.State()
	if st.Rounds < 3 || st.ConsecutiveFailures != 0 || st.NeedAttention || st.Offline {
		t.Fatalf("成功轮次不该留任何失败痕迹: %+v", st)
	}
	if st.LastOKAt.IsZero() || st.NextAt.IsZero() {
		t.Fatalf("成功轮次须记 LastOKAt 并排下一次: %+v", st)
	}
	s.Stop()
	// Stop 后不得再起新轮
	n := atomic.LoadInt32(&rounds)
	time.Sleep(80 * time.Millisecond)
	if m := atomic.LoadInt32(&rounds); m != n {
		t.Fatalf("Stop 之后仍在跑轮次：%d → %d", n, m)
	}
	if s.State().Started {
		t.Fatal("Stop 后 started 必须为假")
	}
	s.Stop() // 幂等
}

func TestSchedulerWakeCoalescesWhileRunning(t *testing.T) {
	gate := make(chan struct{})
	var entered, rounds int32
	s, _ := NewScheduler(SchedOptions{
		Idle: time.Hour, Probe: time.Hour, MaxBackoff: time.Hour, FirstDelay: time.Hour,
		Round: func(context.Context) error {
			atomic.AddInt32(&entered, 1)
			atomic.AddInt32(&rounds, 1)
			<-gate // 卡在途
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// 用 Wake 起第一轮（FirstDelay 设成 1h，定时器不会自己响）
	for i := 0; i < 5; i++ {
		s.Wake()
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&entered) == 1 }, 2*time.Second, "首轮未进入")
	if s.State().Running != true {
		t.Fatal("在途标记应为真")
	}
	close(gate)
	waitFor(t, func() bool { return !s.State().Running }, 2*time.Second, "首轮未结束")
	time.Sleep(120 * time.Millisecond)
	// 结构性单飞：5 次 Wake 至多换来"当前这轮 + 一轮合并补跑"，绝不并行、绝不 5 轮
	if got := atomic.LoadInt32(&rounds); got > 2 {
		t.Fatalf("Wake 未合并，跑了 %d 轮", got)
	}
}

func TestSchedulerTerminalStopsAutoAndWakeRetries(t *testing.T) {
	var rounds int32
	s, _ := NewScheduler(SchedOptions{
		Idle: 10 * time.Millisecond, Probe: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
		FirstDelay: time.Millisecond,
		Round: func(context.Context) error {
			atomic.AddInt32(&rounds, 1)
			return fmt.Errorf("wrap: %w", ErrEpochChanged)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.State().NeedAttention }, 2*time.Second, "未进入停手态")
	s.Stop()
	held := atomic.LoadInt32(&rounds)
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&rounds); got != held {
		t.Fatalf("协议终态后仍在自动重试：%d → %d（会周期性打服务端）", held, got)
	}
	st := s.State()
	if st.LastError == "" {
		t.Fatal("停手原因必须可见（前端据此提示用户）")
	}
	if !st.NeedAttention {
		t.Fatal("停手标记必须在 Stop 后仍保留（重启前不该假装一切正常）")
	}
}

func TestSchedulerAllowFalseIsNotFailure(t *testing.T) {
	var rounds int32
	s, _ := NewScheduler(SchedOptions{
		Idle: 8 * time.Millisecond, FirstDelay: time.Millisecond,
		Round: func(context.Context) error { atomic.AddInt32(&rounds, 1); return nil },
		Allow: func() bool { return false }, // 未持票/锁屏/导入中
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if got := atomic.LoadInt32(&rounds); got != 0 {
		t.Fatalf("Allow=false 时不该执行轮次，跑了 %d 轮", got)
	}
	st := s.State()
	if st.ConsecutiveFailures != 0 || st.Rounds != 0 || st.NeedAttention {
		t.Fatalf("跳过不得计成失败: %+v", st)
	}
	// 放开闸门后必须立刻恢复节奏
	s.mu.Lock()
	s.opt.Allow = func() bool { return true }
	s.mu.Unlock()
	s.Wake()
	waitFor(t, func() bool { return atomic.LoadInt32(&rounds) >= 1 }, 2*time.Second, "放开后未恢复轮次")
}

func TestSchedulerPanicIsContainedInOneRound(t *testing.T) {
	var rounds int32
	var mu sync.Mutex
	var errs []string
	s, _ := NewScheduler(SchedOptions{
		Idle: 8 * time.Millisecond, Probe: 8 * time.Millisecond, MaxBackoff: 16 * time.Millisecond,
		FirstDelay: time.Millisecond,
		Round: func(context.Context) error {
			n := atomic.AddInt32(&rounds, 1)
			if n == 1 {
				panic("boom") // 模拟换票/入队路径踩到空句柄
			}
			return nil
		},
		// 逐轮记录（读 State 会滞后：下一轮成功就把 LastError 清了，断言必须取自回调当场）
		OnRound: func(err error, st SchedState) {
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err.Error())
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// panic 若逃逸出调度 goroutine，整个进程会挂；这里要求它只污染一轮
	waitFor(t, func() bool { return atomic.LoadInt32(&rounds) >= 2 }, 3*time.Second, "panic 后调度器未继续存活")
	s.Stop()
	mu.Lock()
	defer mu.Unlock()
	if len(errs) == 0 || !strings.Contains(errs[0], "panic") {
		t.Fatalf("panic 必须被记成该轮失败，实际 errs=%v", errs)
	}
	if st := s.State(); st.ConsecutiveFailures != 0 || st.NeedAttention {
		t.Fatalf("panic 不该把调度器打进停手态（后续已成功）: %+v", st)
	}
}

func TestSchedulerStopCancelsInFlightRound(t *testing.T) {
	entered := make(chan struct{})
	var sawCancel int32
	s, _ := NewScheduler(SchedOptions{
		Idle: time.Hour, FirstDelay: time.Millisecond,
		Round: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done() // 等在途轮次被取消
			atomic.AddInt32(&sawCancel, 1)
			return ctx.Err()
		},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop 未等在途轮次收敛")
	}
	if atomic.LoadInt32(&sawCancel) != 1 {
		t.Fatal("Stop 必须取消在途轮次的 ctx")
	}
	if st := s.State(); st.Running {
		t.Fatal("Stop 后 running 标记必须清掉")
	}
}

// ---- 纯函数面：三种节奏与错误分类（不依赖真实计时，最稳的一层）----

func TestSchedulerDelayRhythm(t *testing.T) {
	s, err := NewScheduler(SchedOptions{
		Idle: 5 * time.Minute, Probe: time.Minute, MaxBackoff: 30 * time.Minute,
		Round: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if got := s.nextDelayLocked(); got != 5*time.Minute {
		t.Fatalf("空闲节奏应为 Idle，得 %s", got)
	}
	// 传输类失败 → 短探测（联网恢复后最多 1 分钟自愈）
	s.st.Offline = true
	if got := s.nextDelayLocked(); got != time.Minute {
		t.Fatalf("离线期应用探测间隔，得 %s", got)
	}
	s.st.Offline = false
	s.st.ConsecutiveFailures = 1
	if got := s.nextDelayLocked(); got != 5*time.Minute {
		t.Fatalf("第一次退避应等于 Idle，得 %s", got)
	}
	s.st.ConsecutiveFailures = 3 // 5m → 10m → 20m
	if got := s.nextDelayLocked(); got != 20*time.Minute {
		t.Fatalf("退避未按 2^n 增长，得 %s", got)
	}
	s.st.ConsecutiveFailures = 12 // 位移早已溢出，必须被封顶而不是变负/变零
	if got := s.nextDelayLocked(); got != 30*time.Minute {
		t.Fatalf("退避未封顶在 MaxBackoff，得 %s", got)
	}
	s.st.ConsecutiveFailures = 99
	if got := s.nextDelayLocked(); got <= 0 || got > 30*time.Minute {
		t.Fatalf("退避时长越界（溢出未兜住）：%s", got)
	}
	s.mu.Unlock()
}

// ⭐ v21.221 重写（214 实机定案）：本用例原名 `TestSchedulerTerminalHoldSkipsScheduling`，
// 断言「NeedAttention ⇒ planWait 返回 hold=true」。
//
// **该断言钉住的正是 214 实机的病根**：
// hold=true ⇒ loop 里 `tick = nil 通道` ⇒ 定时器分支永不就绪
// ⇒ 自动轮次**永不再跑**，只有用户手动点按钮才 Wake。
// 214 实机症状：桌面端首次同步正常、关闭重开后**永久「恢复中」**、笔记列表空、
// 日志 `[sched] 调度已启动` 之后再无任何轮次记录。
//
// 设计错误：「需要用户注意」是**提示义务**，「停止自动尝试」是**可用性自杀**。
// 现在契约：NeedAttention **不再 hold**，只把退避拉长（terminalBackoff 阶梯）。
func TestSchedulerTerminalNoLongerHoldsScheduling(t *testing.T) {
	s, _ := NewScheduler(SchedOptions{Idle: time.Minute, Round: func(context.Context) error { return nil }})
	s.mu.Lock()
	s.st.NeedAttention = true
	s.st.ConsecutiveFailures = 1
	s.st.Rounds = 1 // 已跑过一轮，否则 planWait 走"启动首轮"分支返回 FirstDelay
	s.mu.Unlock()

	d, hold := s.planWait()
	// ⭐ 核心断言：不得 hold —— 否则自动恢复能力被彻底剥夺
	if hold {
		t.Fatal("NeedAttention 不得再导致 hold（214 实机永久「恢复中」的根因）")
	}
	// 但必须排下一次（而不是零值/不排），否则同样等于停手
	if d <= 0 {
		t.Fatalf("必须排下一次自动重试，得 d=%s", d)
	}
	if s.State().NextAt.IsZero() {
		t.Fatal("必须设置 NextAt（供 UI 展示「下次自动重试时间」）")
	}
	// 长退避阶梯第 1 档
	if d != 5*time.Minute {
		t.Fatalf("终态退避第 1 档应为 5min，得 %s", d)
	}

	// ⭐ 阶梯递增且封顶
	s.mu.Lock()
	for _, tc := range []struct {
		fail int
		want time.Duration
	}{{1, 5 * time.Minute}, {2, 15 * time.Minute}, {3, 30 * time.Minute}, {99, 30 * time.Minute}} {
		s.st.ConsecutiveFailures = tc.fail
		if got := s.nextDelayLocked(); got != tc.want {
			s.mu.Unlock()
			t.Fatalf("failures=%d 退避应为 %s，得 %s", tc.fail, tc.want, got)
		}
	}
	s.mu.Unlock()
}

func TestClassifyRoundErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want roundOutcome
	}{
		{"无错", nil, outcomeNone},
		{"代际变了→终态", ErrEpochChanged, outcomeTerminal},
		{"需重取→终态", ErrResyncNeeded, outcomeTerminal},
		// ⭐ v21.221：AUTH_REVOKED **单独成类**，不再与 epoch/resync 共用 outcomeTerminal。
		//   理由：它是"票被服务端作废了"，本地等再久也不会好，唯一出路是重新登录；
		//   而 epoch/resync 是代际不一致，退避后可自愈。二者处置必须不同。
		{"票被吊销→独立类（须重登）", ErrAuthRevoked, outcomeAuthRevoked},
		{"包一层的 AUTH_REVOKED 也须识别", fmt.Errorf("wrap: %w", ErrAuthRevoked), outcomeAuthRevoked},
		{"包一层的终态", fmt.Errorf("wrap: %w", ErrResyncNeeded), outcomeTerminal},
		{"连不上→探测", &url.Error{Op: "Get", URL: "http://x/api/sync/state", Err: errors.New("dial tcp: i/o timeout")}, outcomeTransport},
		{"DNS 失败", &net.DNSError{Err: "no such host", Name: "nas.local"}, outcomeTransport},
		{"连接被拒", syscall.ECONNREFUSED, outcomeTransport},
		{"上下文超时", context.DeadlineExceeded, outcomeTransport},
		{"纯文本连不上", errors.New("Get \"http://1.2.3.4\": connectex; Network is unreachable"), outcomeTransport},
		{"协议类错误→普通退避", errors.New("sync push rejected: BATCH_PROTOCOL_ERROR"), outcomeRetry},
		{"CAS 409→普通退避（要人看，但不是终态）", errors.New("HTTP 409 conflict"), outcomeRetry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRoundErr(tc.err); got != tc.want {
				t.Fatalf("分类错：%v → got %d want %d", tc.err, got, tc.want)
			}
		})
	}
}

// 状态回调（上层据此做 401 降级等副作用）必须在轮次结束后拿到最终状态。
func TestSchedulerOnRoundSeesFinalState(t *testing.T) {
	var mu sync.Mutex
	var seen []SchedState
	s, _ := NewScheduler(SchedOptions{
		Idle: 8 * time.Millisecond, Probe: 8 * time.Millisecond, MaxBackoff: 16 * time.Millisecond,
		FirstDelay: time.Millisecond,
		Round:      func(context.Context) error { return errors.New("SERVER_EPOCH_CHANGED? no — plain retry") },
		OnRound: func(err error, st SchedState) {
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				t.Error("本轮设计为失败，回调却收到 nil")
			}
			seen = append(seen, st)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) >= 2 }, 3*time.Second, "回调未到")
	s.Stop()
	mu.Lock()
	defer mu.Unlock()
	if seen[0].ConsecutiveFailures > seen[1].ConsecutiveFailures {
		t.Fatalf("回调里的失败计数应单调不减：%+v %+v", seen[0], seen[1])
	}
	if seen[0].Running {
		t.Fatal("回调时机应在轮次收尾之后（Running 已清）")
	}
}
