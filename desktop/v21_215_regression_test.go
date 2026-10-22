package main

// v21_215_regression_test.go —— 211d 实机「第二次启动必 AUTH_REVOKED」的回归钉。
//
// 211d 铁证（同实例日志三行）：
//
//	13:54:37 [auth] device session restored         ← 第2次启动，首轮换票开始（网络慢一直在途）
//	13:55:18 [sched] 调度器未在预期时限内退出       ← 用户关窗，8s 兜底放弃 → 进程退出
//	13:55:21 [auth] 凭据被服务端拒绝（AUTH_REVOKED）← 第3次启动 2 秒内必死
//
// 根因：v21.213 的 WithoutCancel 让换票「不被 ctx 取消」，但**进程退出不等
// goroutine** —— Stop 8s 兜底一到进程终止，在途换票被拦腰杀死；服务端已轮转
// 作废旧票、新票随进程死亡丢失 ⇒ 下次启动必 401。
//
// 修法（v21.215）：gracefulShutdown 在取消广播后调 waitExchangeDrain ——
// 换票全程持 exchangeMu 且 SaveRefreshCAS 在锁内，**锁空闲 = 新票已落盘**。

import (
	"context"
	"strings"
	"testing"
	"time"
)

// indexOf：strings.Index 的薄封装（仅为与 readSourceForTest 配套的可读性）。
func indexOf(s, sub string) int { return strings.Index(s, sub) }

// TestWaitExchangeDrainWaitsForInFlightExchange 核心钉：换票在途时，
// waitExchangeDrain 必须等到锁释放才返回（而不是立即放行）。
//
// 复现 211d 的形态：持锁者模拟一次慢换票（80ms），关机侧必须在它完成后才继续。
func TestWaitExchangeDrainWaitsForInFlightExchange(t *testing.T) {
	if !lockExchange(context.Background()) {
		t.Fatal("前置：锁空闲应能拿到")
	}
	released := false
	defer func() {
		if !released {
			unlockExchange()
		}
	}()

	done := make(chan struct{})
	go func() {
		waitExchangeDrain(5 * time.Second) // 上限远大于持锁时长
		close(done)
	}()

	// drain 不应在持锁期间返回（给足它"错误地立即返回"的时间窗）
	select {
	case <-done:
		t.Fatal("换票锁仍被持有时，waitExchangeDrain 不得放行——" +
			"211d 正是关机不等在途换票、进程退出杀死 goroutine 导致新票丢失")
	case <-time.After(60 * time.Millisecond):
	}

	// 释放锁 ⇒ drain 应在百毫秒级内返回
	unlockExchange()
	released = true
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("锁释放后 waitExchangeDrain 应 promptly 返回")
	}
}

// TestWaitExchangeDrainTimesOut 放弃路径：换票卡死超过上限时必须放行
// （关机不能被无限阻塞——与 Stop 兜底同一哲学）。
func TestWaitExchangeDrainTimesOut(t *testing.T) {
	if !lockExchange(context.Background()) {
		t.Fatal("前置：锁空闲应能拿到")
	}
	released := false
	defer func() {
		if !released {
			unlockExchange()
		}
	}()

	start := time.Now()
	waitExchangeDrain(200 * time.Millisecond) // 上限远小于本测试总时长
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Fatalf("持锁未释放时应等到上限，实得 %s", el)
	}
	// 能返回本身就是通过（不卡死关机）
}

// TestWaitExchangeDrainIdleNoWait 空闲零等待：无换票在途时关机不得被拖慢
// （这是绝大多数关机路径，不能为罕见场景牺牲常态体验）。
func TestWaitExchangeDrainIdleNoWait(t *testing.T) {
	start := time.Now()
	waitExchangeDrain(16 * time.Second)
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("锁空闲时 waitExchangeDrain 应近乎零等待，实得 %s", el)
	}
}

// TestGracefulShutdownIncludesExchangeDrain 结构钉：gracefulShutdown 必须
// 在 stopScheduler 之前等换票收尾（顺序不可换——先等换票落盘，再收调度器）。
// 用源码断言防"有人日后把这一步挪走/删掉"。
func TestGracefulShutdownIncludesExchangeDrain(t *testing.T) {
	src := readSourceForTest(t, "shutdown.go")
	drainIdx := indexOf(src, "waitExchangeDrain(16 * time.Second)")
	if drainIdx < 0 {
		t.Fatal("gracefulShutdown 缺 waitExchangeDrain 调用（v21.215 关机等换票收尾）")
	}
	// ⚠ 用 LastIndex：stopScheduler() 首次出现在文件头**注释**里（v21.203 的定罪描述），
	// 用 FirstIndex 会把注释当调用点，造成假失败。
	stopIdx := strings.LastIndex(src, "stopScheduler()")
	if stopIdx < 0 || drainIdx > stopIdx {
		t.Fatal("waitExchangeDrain 必须在 stopScheduler 之前（先等换票落盘，再收调度器）")
	}
	// 顺序完整链：cancelShutdown → waitExchangeDrain → stopScheduler
	cancelIdx := strings.LastIndex(src, "cancelShutdown()")
	if cancelIdx > drainIdx {
		t.Fatal("waitExchangeDrain 必须在取消广播之后（先停普通轮，再等换票收尾）")
	}
}
