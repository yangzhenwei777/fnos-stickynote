package backup

import (
	"strings"
	"testing"
	"time"

	"stickynote/database"
	"stickynote/model"
)

// 本文件覆盖 v21.129 两个新能力：
//   - DeleteSnapshot：手动删除一份备份（所有目标远端 .snbak+.index.json + 本地快照行级联）
//   - PruneTarget：保留份数改小后立即轮转超量旧档（含孤儿快照行清理 + 失败保留 placement 重试）

// snapshotExists 报告某 backup_id 的本地快照行是否还在。
func snapshotExists(t *testing.T, backupID string) bool {
	t.Helper()
	ex, err := model.SnapshotExists(backupID)
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// remoteExists 报告某目标远端 .snbak / .index.json 是否还在。
func remoteExists(t *testing.T, m *Manager, tid int64, backupID string) (arc bool, idx bool) {
	t.Helper()
	c, err := m.clientFor(mustGetTarget(t, tid))
	if err != nil {
		t.Fatal(err)
	}
	_, arc, err = c.Head("/backups/" + backupID + ".snbak")
	if err != nil {
		t.Fatalf("head snbak: %v", err)
	}
	_, idx, err = c.Head("/backups/" + backupID + ".index.json")
	if err != nil {
		t.Fatalf("head index: %v", err)
	}
	return arc, idx
}

// runOneBackup 跑一次完整备份，返回 backup_id。
func runOneBackup(t *testing.T, m *Manager, tid int64) string {
	t.Helper()
	jobID, bid, err := m.BeginJob(tid, "manual")
	if err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	if err := m.runJob(tid, "manual", jobID, bid); err != nil {
		t.Fatalf("runJob: %v", err)
	}
	return bid
}

// TestDeleteSnapshotSingleTarget：单目标删除——远端两对象消失、本地快照行+placement 清空，
// 且**不波及**同目标上的其它备份。
func TestDeleteSnapshotSingleTarget(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.169", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)

	bid1 := runOneBackup(t, m, tid)
	bid2 := runOneBackup(t, m, tid)

	// 前置：两份都在远端 + 本地
	if arc, idx := remoteExists(t, m, tid, bid1); !arc || !idx {
		t.Fatalf("前置：bid1 远端不全 arc=%v idx=%v", arc, idx)
	}

	deleted, warns, err := m.DeleteSnapshot(bid1)
	if err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d want 1", deleted)
	}
	if len(warns) != 0 {
		t.Fatalf("warns=%v want empty", warns)
	}
	// bid1 远端两对象都没了
	if arc, idx := remoteExists(t, m, tid, bid1); arc || idx {
		t.Fatalf("bid1 远端应清空 arc=%v idx=%v", arc, idx)
	}
	// bid1 本地快照行 + placement 都没了
	if snapshotExists(t, bid1) {
		t.Fatal("bid1 本地快照行应删除")
	}
	if placementStatus(t, bid1, tid) != "" {
		t.Fatal("bid1 placement 应随快照行级联删除")
	}
	// bid2 完全不受影响
	if arc, idx := remoteExists(t, m, tid, bid2); !arc || !idx {
		t.Fatalf("bid2 远端不应被波及 arc=%v idx=%v", arc, idx)
	}
	if !snapshotExists(t, bid2) {
		t.Fatal("bid2 本地快照行不应被波及")
	}
	if placementStatus(t, bid2, tid) != "VERIFIED" {
		t.Fatal("bid2 placement 应仍 VERIFIED")
	}
}

// TestDeleteSnapshotMultiTarget：同一 backup_id 放在两个目标上，删除须把**两个目标**的
// 远端副本都清掉（用户明确要求"全部目标的远端文件都删"），本地快照行删除、两条 placement 级联清空。
func TestDeleteSnapshotMultiTarget(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srvA := newFakeWebDAV(t)
	srvB := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.169", 1)
	tidA := makeTarget(t, m, strings.TrimRight(srvA.URL, "/")+"/dav/", true)
	tidB := makeTarget(t, m, strings.TrimRight(srvB.URL, "/")+"/dav/", true)

	// 在 A 上正常备份一份
	bid := runOneBackup(t, m, tidA)

	// 把同一 backup_id 也"放"到 B：远端写两对象 + 登记 placement VERIFIED
	cB, err := m.clientFor(mustGetTarget(t, tidB))
	if err != nil {
		t.Fatal(err)
	}
	if err := cB.PutBytes("/backups/"+bid+".snbak", []byte("dummy-archive-on-B")); err != nil {
		t.Fatalf("put snbak B: %v", err)
	}
	if err := cB.PutBytes("/backups/"+bid+".index.json", []byte(`{"backup_id":"`+bid+`"}`)); err != nil {
		t.Fatalf("put index B: %v", err)
	}
	if err := model.UpsertBackupPlacement(bid, tidB, "VERIFIED", "/backups/"+bid+".snbak", ""); err != nil {
		t.Fatalf("upsert placement B: %v", err)
	}

	deleted, warns, err := m.DeleteSnapshot(bid)
	if err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d want 2（两个目标各一份）", deleted)
	}
	if len(warns) != 0 {
		t.Fatalf("warns=%v want empty", warns)
	}
	// A、B 远端都清空
	if arc, idx := remoteExists(t, m, tidA, bid); arc || idx {
		t.Fatalf("A 远端应清空 arc=%v idx=%v", arc, idx)
	}
	if arc, idx := remoteExists(t, m, tidB, bid); arc || idx {
		t.Fatalf("B 远端应清空 arc=%v idx=%v", arc, idx)
	}
	// 本地快照行 + 两条 placement 都没了
	if snapshotExists(t, bid) {
		t.Fatal("本地快照行应删除")
	}
	if placementStatus(t, bid, tidA) != "" {
		t.Fatal("A placement 应级联删除")
	}
	if placementStatus(t, bid, tidB) != "" {
		t.Fatal("B placement 应级联删除")
	}
}

// TestDeleteSnapshotMutex：备份运行中删除应被互斥拒（ErrAlreadyRunning）。
func TestDeleteSnapshotMutex(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.169", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)
	bid := runOneBackup(t, m, tid)

	// 占住运行锁
	if _, _, err := m.BeginJob(tid, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.DeleteSnapshot(bid); err != ErrAlreadyRunning {
		t.Fatalf("运行中删除 err=%v want ErrAlreadyRunning", err)
	}
	m.end()
	// 释放后可删
	if _, _, err := m.DeleteSnapshot(bid); err != nil {
		t.Fatalf("释放后删除失败: %v", err)
	}
}

// TestPruneTargetOnRetentionLowered：3 份备份 + 保留份数调到 1 → PruneTarget 立即删 2 份最旧
// （远端 + placement + 孤儿快照行），最新那份绝不动（never-to-zero）。
func TestPruneTargetOnRetentionLowered(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.169", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		ids[i] = runOneBackup(t, m, tid)
	}
	// 保留份数调到 1（模拟用户在「备份目标」编辑保存）
	if err := model.SetTargetRetention(tid, 1); err != nil {
		t.Fatal(err)
	}

	deleted, warns, err := m.PruneTarget(tid)
	if err != nil {
		t.Fatalf("PruneTarget: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("warns=%v want empty", warns)
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d want 2", deleted)
	}

	oldest, middle, newest := ids[0], ids[1], ids[2]
	// 最新保留：远端在、placement VERIFIED、快照行在
	if arc, idx := remoteExists(t, m, tid, newest); !arc || !idx {
		t.Fatalf("最新归档不应被轮转 arc=%v idx=%v", arc, idx)
	}
	if placementStatus(t, newest, tid) != "VERIFIED" {
		t.Fatal("最新 placement 应 VERIFIED")
	}
	if !snapshotExists(t, newest) {
		t.Fatal("最新快照行应保留")
	}
	// 两份最旧：远端没了、placement 没了、孤儿快照行也清了
	for _, old := range []string{oldest, middle} {
		if arc, idx := remoteExists(t, m, tid, old); arc || idx {
			t.Fatalf("旧档 %s 远端应清空 arc=%v idx=%v", old[:8], arc, idx)
		}
		if placementStatus(t, old, tid) != "" {
			t.Fatalf("旧档 %s placement 应删除", old[:8])
		}
		if snapshotExists(t, old) {
			t.Fatalf("旧档 %s 孤儿快照行应清理（否则列表残留死记录）", old[:8])
		}
	}
	// 快照列表最终只剩最新那份
	snaps := snapsOf(t)
	if len(snaps) != 1 || snaps[0].BackupID != newest {
		t.Fatalf("快照列表应只剩最新 1 份，得 %d 份", len(snaps))
	}
}

// TestPruneTargetKeepsSnapshotWhenOtherTargetHasCopy：轮转只删本目标副本；若同一快照在
// **别的目标**还有放置，则快照行必须保留（不能误删成孤儿）。
func TestPruneTargetKeepsSnapshotWhenOtherTargetHasCopy(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srvA := newFakeWebDAV(t)
	srvB := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.169", 1)
	tidA := makeTarget(t, m, strings.TrimRight(srvA.URL, "/")+"/dav/", true)
	tidB := makeTarget(t, m, strings.TrimRight(srvB.URL, "/")+"/dav/", true)

	// A 上跑两份
	bid1 := runOneBackup(t, m, tidA)
	bid2 := runOneBackup(t, m, tidA)
	// 把 bid1 也放到 B（模拟多目标冗余）
	cB, err := m.clientFor(mustGetTarget(t, tidB))
	if err != nil {
		t.Fatal(err)
	}
	if err := cB.PutBytes("/backups/"+bid1+".snbak", []byte("copy-on-B")); err != nil {
		t.Fatal(err)
	}
	if err := cB.PutBytes("/backups/"+bid1+".index.json", []byte(`{"backup_id":"`+bid1+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := model.UpsertBackupPlacement(bid1, tidB, "VERIFIED", "/backups/"+bid1+".snbak", ""); err != nil {
		t.Fatal(err)
	}

	// A 保留份数调到 1 → 轮转应删 A 上的 bid1 副本，但 bid1 在 B 还有 → 快照行保留
	if err := model.SetTargetRetention(tidA, 1); err != nil {
		t.Fatal(err)
	}
	deleted, warns, err := m.PruneTarget(tidA)
	if err != nil {
		t.Fatalf("PruneTarget: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("warns=%v want empty", warns)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d want 1（只轮转 A 上的 bid1）", deleted)
	}
	// A 上 bid1 远端没了、A placement 没了
	if arc, idx := remoteExists(t, m, tidA, bid1); arc || idx {
		t.Fatalf("A 上 bid1 远端应清空 arc=%v idx=%v", arc, idx)
	}
	if placementStatus(t, bid1, tidA) != "" {
		t.Fatal("A 上 bid1 placement 应删除")
	}
	// 但 B 上 bid1 仍在、B placement 仍在、快照行仍在（非孤儿）
	if arc, idx := remoteExists(t, m, tidB, bid1); !arc || !idx {
		t.Fatalf("B 上 bid1 不应被波及 arc=%v idx=%v", arc, idx)
	}
	if placementStatus(t, bid1, tidB) != "VERIFIED" {
		t.Fatal("B 上 bid1 placement 应仍 VERIFIED")
	}
	if !snapshotExists(t, bid1) {
		t.Fatal("bid1 在 B 仍有副本 → 快照行必须保留（不能误删）")
	}
	// bid2（A 最新）不受影响
	if !snapshotExists(t, bid2) || placementStatus(t, bid2, tidA) != "VERIFIED" {
		t.Fatal("bid2 应完整保留")
	}
	_ = database.DB
}

// TestDownloadArchiveNoVerifiedCopyErrors：仅失败放置（无 VERIFIED 副本）的快照下载应返回
// 明确"可下载副本"缺失错误。前端据 has_remote 禁用按钮，即便被点也应给出具体原因而非笼统失败。
func TestDownloadArchiveNoVerifiedCopyErrors(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.170", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)

	bid := "no-copy-snapshot-0001"
	if err := model.InsertBackupSnapshot(model.BackupSnapshot{
		BackupID: bid, CreatedAt: time.Now().UTC(), Trigger: "manual",
		ArchiveSha256: "x", Status: "LOCAL_VERIFIED",
	}); err != nil {
		t.Fatal(err)
	}
	if err := model.UpsertBackupPlacement(bid, tid, "FAILED", "/backups/"+bid+".snbak", "upload failed"); err != nil {
		t.Fatal(err)
	}

	_, _, err := m.DownloadArchive(bid)
	if err == nil {
		t.Fatal("无 VERIFIED 副本应下载失败")
	}
	if !strings.Contains(err.Error(), "可下载副本") {
		t.Fatalf("错误应含明确'无可用副本'文案，得: %v", err)
	}
}
