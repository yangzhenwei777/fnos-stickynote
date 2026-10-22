//go:build windows

package main

// sync_gate_windows_test.go —— 后台同步门（allowScheduledRound）的解死锁断言（v21.142）。
//
// ⭐ 这是本轮修复的核心不变量，必须钉死：
// 冷启动恢复的设备会话是**空票挂工**（access 只存内存 §87），若门条件是裸的
// `workerHasToken(syncWorker)`，则每一轮后台同步都被挡掉 → `scheduledRound` 里的
// `ensureAccess`（惰性换票）永远不执行 → **永不换票 → 永不同步**，界面永远"离线"，
// 只有用户手点【立即同步】才活。真机现象：登录成功但默认仍是离线状态 + 点登录一直转圈。
//
// 因此：空票但**会话可恢复**时必须放行一轮；真正无档案（离线单机）仍必须挡住。

import (
	"testing"

	"stickynote-desktop/synclocal"
	"stickynote/database"
)

func TestAllowScheduledRoundUnlockOnRestorableSession(t *testing.T) {
	_, _ = bootWithLogin(t) // 备好本地库（NewWorker 需要 db 句柄）

	savedW, savedL := workerPtr(), lsInstance
	defer func() { setWorker(savedW); lsInstance = savedL }()

	// ① 无 worker → 不放行（离线单机起步态）
	setWorker(nil)
	lsInstance = &LocalService{sessionRestored: true}
	if allowScheduledRound() {
		t.Fatal("worker 为 nil 时必须 false")
	}

	// ② 空票 worker + 会话可恢复 → **必须放行**（放行后该轮会先 ensureAccess 换票）
	setWorker(synclocal.NewWorker(database.DB, synclocal.NewClient("", "", "")))
	lsInstance = &LocalService{sessionRestored: true}
	if !allowScheduledRound() {
		t.Fatal("空票但设备会话可恢复时必须放行 —— 否则冷启动后永不换票、永不同步（v21.142 修的死锁）")
	}

	// ③ 空票 worker + 会话不可恢复（无档案/离线单机）→ 挡住，不空转
	lsInstance = &LocalService{}
	if allowScheduledRound() {
		t.Fatal("无档案的离线单机态不应放行（避免空转与无谓失败计数）")
	}

	// ④ 全局实例缺失时不 panic（防御）
	lsInstance = nil
	if allowScheduledRound() {
		t.Fatal("lsInstance==nil 时必须 false（防御性）")
	}
}

func TestSessionRestorableGuard(t *testing.T) {
	saved := lsInstance
	defer func() { lsInstance = saved }()

	lsInstance = nil
	if sessionRestorable() {
		t.Fatal("lsInstance==nil 时 sessionRestorable 必须 false（不得 panic）")
	}
	lsInstance = &LocalService{sessionRestored: true}
	if !sessionRestorable() {
		t.Fatal("sessionRestored=true 时应为 true")
	}
	lsInstance = &LocalService{}
	if sessionRestorable() {
		t.Fatal("sessionRestored=false 时应为 false")
	}
}

func TestFirstSyncWaitIsBounded(t *testing.T) {
	// 登录即等首轮同步必须有界且不夸张（真机"一直转圈"教训：60s 太长）
	if FirstSyncWait <= 0 {
		t.Fatal("FirstSyncWait 必须为正")
	}
	if FirstSyncWait > 12*1e9 { // 12s（v21.143：8s 上限的更严断言）
		t.Fatalf("FirstSyncWait=%v 过长，登录会长时间转圈", FirstSyncWait)
	}
}
