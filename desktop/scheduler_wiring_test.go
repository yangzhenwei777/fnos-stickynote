package main

// scheduler_wiring_test.go —— 调度器接线的门与生命周期（批次 8-Alt 刀 3）。
// 这里只测"接线是否正确"（Allow 门、暂停窗、启停幂等、状态回传形状）；
// 节奏与状态机本身在 synclocal/scheduler_test.go 已逐条钉过，不重复。

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"stickynote-desktop/synclocal"
	"stickynote/database"
)

// 每个用例进出都还原包级全局，避免污染同包其它测试（syncWorker/syncScheduler/syncPauseDepth 都是共享态）。
func withWiringReset(t *testing.T) {
	t.Helper()
	oldWorker, oldSched := workerPtr(), syncScheduler
	oldPauseDepth := syncPauseDepth.Load()
	t.Cleanup(func() {
		if syncScheduler != nil && syncScheduler != oldSched {
			syncScheduler.Stop()
		}
		syncWorkerPtr.Store(oldWorker)
		syncScheduler = oldSched
		syncPauseDepth.Store(oldPauseDepth)
	})
}

func tokenWorker() *synclocal.Worker {
	w := synclocal.NewWorker(nil, synclocal.NewClient("http://127.0.0.1:1", "tok", "devT"))
	return w
}

func TestAllowScheduledRoundGate(t *testing.T) {
	withWiringReset(t)
	setWorker(nil)
	syncPauseDepth.Store(0)
	if allowScheduledRound() {
		t.Fatal("未挂载 worker 时不许跑（离线单机态是正常态，不该产生任何网络行为）")
	}
	setWorker(synclocal.NewWorker(nil, synclocal.NewClient("http://x", "", "devT")))
	if allowScheduledRound() {
		t.Fatal("空票占位工不许跑：没票的 RunOnce 只会拿 401 污染退避计数")
	}
	setWorker(tokenWorker())
	if !allowScheduledRound() {
		t.Fatal("持票应允许")
	}
	syncPauseDepth.Store(1)
	if allowScheduledRound() {
		t.Fatal("换库窗口内必须避让")
	}
	syncPauseDepth.Store(0)
	if !allowScheduledRound() {
		t.Fatal("解除暂停后应恢复")
	}
}

func TestPauseResumeRoundTripWakesScheduler(t *testing.T) {
	withWiringReset(t)
	syncPauseDepth.Store(0)
	var runs int32
	// Allow 用真门：不持票 → 不跑；持票 + Wake → 跑
	s, err := synclocal.NewScheduler(synclocal.SchedOptions{
		Idle: time.Hour, Probe: time.Hour, MaxBackoff: time.Hour, FirstDelay: time.Hour,
		Round: func(context.Context) error { atomic.AddInt32(&runs, 1); return nil },
		Allow: allowScheduledRound,
	})
	if err != nil {
		t.Fatal(err)
	}
	syncScheduler = s
	ls := &LocalService{uploadDir: filepath.Join(t.TempDir(), "uploads")}
	startScheduler(ls)
	setWorker(nil)
	s.Wake()
	time.Sleep(60 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Fatalf("未登录时 Wake 也不该跑，跑了 %d 轮", got)
	}

	setWorker(tokenWorker())
	pauseScheduler()
	resumeScheduler() // 解除暂停自带一次唤醒
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&runs) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&runs) == 0 {
		t.Fatal("resumeScheduler 必须自带一次唤醒（否则换库后的挂账要等下个 Idle 才动）")
	}
}

// TestPauseSchedulerNesting —— v21.203：暂停窗口是**会嵌套**的
// （scheduledRound 的 auto-bootstrap 先 pause，再调 BootstrapNow，后者内部又
// pause/resume）。bool 语义下内层 resume 会把外层窗口一并解除 ⇒ 换代还在收尾
// 时调度器已被放行，那一轮对着正在被 Close 的旧句柄跑 push。
// 这里钉住「只有深度归零才真正解除」。
func TestPauseSchedulerNesting(t *testing.T) {
	withWiringReset(t)
	setWorker(tokenWorker())
	syncPauseDepth.Store(0)

	pauseScheduler() // 外层：auto-bootstrap
	pauseScheduler() // 内层：BootstrapNow
	if syncPauseDepth.Load() != 2 {
		t.Fatalf("嵌套后深度应为 2，实得 %d", syncPauseDepth.Load())
	}
	resumeScheduler() // 内层解除
	if syncPauseDepth.Load() != 1 {
		t.Fatalf("内层解除后外层窗口必须仍在（深度 1），实得 %d", syncPauseDepth.Load())
	}
	if !syncPausedForTest() {
		t.Fatal("外层窗口未解除时仍应判「暂停中」")
	}
	resumeScheduler() // 外层解除
	if syncPauseDepth.Load() != 0 {
		t.Fatalf("外层解除后深度应为 0，实得 %d", syncPauseDepth.Load())
	}
	if syncPausedForTest() {
		t.Fatal("全部解除后不应再判暂停")
	}
	// 多 resume 夹紧：不得让深度变负（否则后续 pause 会被误判成 resume）
	resumeScheduler()
	resumeScheduler()
	if syncPauseDepth.Load() < 0 {
		t.Fatalf("深度不得为负，实得 %d", syncPauseDepth.Load())
	}
	pauseScheduler()
	if !syncPausedForTest() {
		t.Fatal("负深度后的一次 pause 必须仍是「暂停中」")
	}
	syncPauseDepth.Store(0)
}

// syncPausedForTest 读绑定面同口径（SyncPaused 绑定返回的布尔值）。
func syncPausedForTest() bool {
	if lsInstance != nil {
		return lsInstance.SyncPaused()
	}
	return syncPauseDepth.Load() > 0
}

func TestStartStopIdempotentAndStatusShape(t *testing.T) {
	withWiringReset(t)
	ls := &LocalService{uploadDir: filepath.Join(t.TempDir(), "uploads")}

	st := ls.SyncScheduleStatus()
	if st["enabled"] != false {
		t.Fatalf("未启动时须报 enabled=false（前端据此显示自动同步未启用）：%+v", st)
	}
	if _, ok := st["reason"]; !ok {
		t.Fatal("未启动必须给原因，不能只给一个空壳")
	}

	startScheduler(ls)
	first := syncScheduler
	if first == nil {
		t.Fatal("startScheduler 未建出调度器")
	}
	startScheduler(ls) // 二次启动必须复用同一实例（否则会起两个循环 = 双份流量）
	if syncScheduler != first {
		t.Fatal("startScheduler 重复调用换了实例")
	}
	st2 := ls.SyncScheduleStatus()
	if st2["enabled"] != true || st2["started"] != true {
		t.Fatalf("启动后状态形状不对: %+v", st2)
	}

	r := ls.NotifyResume()
	if r["ok"] != true {
		t.Fatalf("NotifyResume 应回执 ok: %+v", r)
	}
	stopScheduler()
	stopScheduler() // 幂等
	if st := first.State(); st.Started {
		t.Fatal("Stop 后 started 必须为假")
	}
}

func TestScheduledRoundNoProfileIsNotSilentSuccess(t *testing.T) {
	withWiringReset(t)
	dir := t.TempDir() // 先建目录：t.Cleanup 后注册者先执行，关库必须排在 TempDir 删除之前（Windows 文件锁）
	oldDB := database.DB
	t.Cleanup(func() {
		if database.DB != nil {
			_ = database.DB.Close()
		}
		database.SetDB(oldDB)
	})
	if err := database.Init(filepath.Join(dir, "sched.db"), "99.0.0"); err != nil {
		t.Fatal(err)
	}

	// 从未登录（无 client_profile）：ensureAccess 按离线单机语义返回 (false, nil)，
	// 此时 scheduledRound **必须**报错而不是返回 nil——返回 nil 会被调度器记成"这一轮很健康"，
	// 于是退避计数归零、UI 显示最近成功时间，实际什么都没同步（最坏的一类假绿）。
	setWorker(nil)
	ls := &LocalService{uploadDir: filepath.Join(dir, "uploads")}
	if err := ls.scheduledRound(context.Background()); err == nil {
		t.Fatal("无档案/无 worker 时不得静默成功")
	} else if !errors.Is(err, ErrScheduleNoWorker) {
		t.Fatalf("应以未挂载收尾，得 %v", err)
	}
}
