package model

import (
	"testing"
	"time"
)

// v21.129 备份手动删除/轮转新增 model 函数单测：
//   - ListBackupPlacements：列某 backup_id 在所有目标上的放置
//   - CountBackupPlacements：剩余放置计数（孤儿快照判定）
//   - DeleteBackupSnapshot：删快照行 + placement 经 FK 级联清空

// makeTargetRow 建一个备份目标，返回 id。
func makeTargetRow(t *testing.T, name string) int64 {
	t.Helper()
	id, err := CreateBackupTarget(BackupTarget{
		Name: name, Type: "webdav", BaseURL: "https://dav.example.com/dav/", Username: "u",
		SecretEncrypted: "enc", RemotePath: "/bk/", Enabled: true,
		RetentionCount: 7, ScheduleInterval: "24h",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// makeSnapshotRow 建一份快照，返回 backup_id。
func makeSnapshotRow(t *testing.T, backupID string) string {
	t.Helper()
	if err := InsertBackupSnapshot(BackupSnapshot{
		BackupID: backupID, CreatedAt: time.Now().UTC(), Trigger: "manual",
		ArchiveSha256: "abc", SizeBytes: 10, AppVersion: "1.0.169", SchemaVersion: 1,
		Status: "LOCAL_VERIFIED",
	}); err != nil {
		t.Fatal(err)
	}
	return backupID
}

func TestListBackupPlacementsAllTargets(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	tidA := makeTargetRow(t, "A")
	tidB := makeTargetRow(t, "B")
	bid := makeSnapshotRow(t, "bid-multi-0001")

	// 同一 backup 放到两个目标（含一个 FAILED，验证"不挑状态全列出"）
	if err := UpsertBackupPlacement(bid, tidA, "VERIFIED", "/bk/"+bid+".snbak", ""); err != nil {
		t.Fatal(err)
	}
	if err := UpsertBackupPlacement(bid, tidB, "FAILED", "/bk/"+bid+".snbak", "boom"); err != nil {
		t.Fatal(err)
	}

	rows, err := ListBackupPlacements(bid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应列出 2 条放置（含 FAILED），得 %d", len(rows))
	}
	seen := map[int64]string{}
	for _, r := range rows {
		seen[r.TargetID] = r.Status
	}
	if seen[tidA] != "VERIFIED" || seen[tidB] != "FAILED" {
		t.Fatalf("放置状态不符: %+v", seen)
	}

	// 不存在的 backup → 空列表、无错
	empty, err := ListBackupPlacements("no-such-backup")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("不存在 backup 应空列表，得 %d", len(empty))
	}
}

func TestCountBackupPlacements(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	tid := makeTargetRow(t, "A")
	bid := makeSnapshotRow(t, "bid-count-0001")

	n, err := CountBackupPlacements(bid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("新快照应 0 放置，得 %d", n)
	}

	if err := UpsertBackupPlacement(bid, tid, "VERIFIED", "/bk/"+bid+".snbak", ""); err != nil {
		t.Fatal(err)
	}
	n, _ = CountBackupPlacements(bid)
	if n != 1 {
		t.Fatalf("加 1 放置后应 1，得 %d", n)
	}

	// 删放置 → 归零（孤儿判定依据）
	if err := DeleteBackupPlacement(bid, tid); err != nil {
		t.Fatal(err)
	}
	n, _ = CountBackupPlacements(bid)
	if n != 0 {
		t.Fatalf("删放置后应归零，得 %d", n)
	}
}

func TestDeleteBackupSnapshotCascadesPlacements(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	tidA := makeTargetRow(t, "A")
	tidB := makeTargetRow(t, "B")
	bid := makeSnapshotRow(t, "bid-cascade-001")
	if err := UpsertBackupPlacement(bid, tidA, "VERIFIED", "/bk/"+bid+".snbak", ""); err != nil {
		t.Fatal(err)
	}
	if err := UpsertBackupPlacement(bid, tidB, "VERIFIED", "/bk/"+bid+".snbak", ""); err != nil {
		t.Fatal(err)
	}

	// 前置：快照存在 + 2 放置
	if ex, _ := SnapshotExists(bid); !ex {
		t.Fatal("前置：快照应存在")
	}
	if n, _ := CountBackupPlacements(bid); n != 2 {
		t.Fatalf("前置：应 2 放置，得 %d", n)
	}

	// 删快照行 → placement 经 FK ON DELETE CASCADE 自动清空
	if err := DeleteBackupSnapshot(bid); err != nil {
		t.Fatal(err)
	}
	if ex, _ := SnapshotExists(bid); ex {
		t.Fatal("删后快照行应不存在")
	}
	if n, _ := CountBackupPlacements(bid); n != 0 {
		t.Fatalf("删后 placement 应级联清空，得 %d", n)
	}
	rows, _ := ListBackupPlacements(bid)
	if len(rows) != 0 {
		t.Fatalf("删后 ListBackupPlacements 应空，得 %d", len(rows))
	}
}

func TestListBackupSnapshotsHasRemote(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	tid := makeTargetRow(t, "A")
	live := makeSnapshotRow(t, "snap-live-0001")
	failed := makeSnapshotRow(t, "snap-failed-001")
	orphan := makeSnapshotRow(t, "snap-orphan-001")

	if err := UpsertBackupPlacement(live, tid, "VERIFIED", "/bk/"+live+".snbak", ""); err != nil {
		t.Fatal(err)
	}
	if err := UpsertBackupPlacement(failed, tid, "FAILED", "/bk/"+failed+".snbak", "boom"); err != nil {
		t.Fatal(err)
	}
	// orphan：无任何 placement（模拟历史 prune 遗留死记录）

	got := map[string]BackupSnapshot{}
	for _, s := range mustListSnaps(t) {
		got[s.BackupID] = s
	}
	if s := got[live]; !s.HasRemote || s.VerifiedPlacements != 1 {
		t.Fatalf("live 快照应 has_remote=true/verified=1，得 %+v", s)
	}
	if s := got[failed]; s.HasRemote || s.VerifiedPlacements != 0 {
		t.Fatalf("仅 FAILED 放置的快照应 has_remote=false，得 %+v", s)
	}
	if s := got[orphan]; s.HasRemote {
		t.Fatalf("孤儿快照（无放置）应 has_remote=false，得 %+v", s)
	}
}

func TestPurgeOrphanBackupSnapshotsOnce(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	tid := makeTargetRow(t, "A")
	live := makeSnapshotRow(t, "keep-live-0001")
	orphan1 := makeSnapshotRow(t, "drop-orphan-01")
	orphan2 := makeSnapshotRow(t, "drop-orphan-02")
	if err := UpsertBackupPlacement(live, tid, "VERIFIED", "/bk/"+live+".snbak", ""); err != nil {
		t.Fatal(err)
	}
	// orphan1/orphan2 无放置 → 应被清

	n, err := PurgeOrphanBackupSnapshotsOnce()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应清理 2 条孤儿，得 %d", n)
	}
	if ex, _ := SnapshotExists(live); !ex {
		t.Fatal("有放置的活快照不应被清理")
	}
	for _, o := range []string{orphan1, orphan2} {
		if ex, _ := SnapshotExists(o); ex {
			t.Fatalf("孤儿 %s 应被清理", o)
		}
	}

	// 幂等：旗标已置位，第二次不再执行（即便再插孤儿也返回 0）
	if err := InsertBackupSnapshot(BackupSnapshot{BackupID: "new-orphan-0001",
		CreatedAt: time.Now().UTC(), Trigger: "manual", Status: "LOCAL_VERIFIED"}); err != nil {
		t.Fatal(err)
	}
	n2, err := PurgeOrphanBackupSnapshotsOnce()
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("第二次应返回 0（旗标已置位），得 %d", n2)
	}
	if ex, _ := SnapshotExists("new-orphan-0001"); !ex {
		t.Fatal("旗标置位后新孤儿不应被本函数清理（由 prune 的实时孤儿清理负责）")
	}
}

// TestPurgeOrphanKeepsAnyPlacement 验证一次性清理只删"零放置"孤儿，绝不碰有任意放置行的
// 快照——尤其是 UPLOADING（在途备份，尚未 VERIFIED）也必须保留，防升级清理误删在途备份。
func TestPurgeOrphanKeepsAnyPlacement(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	tid := makeTargetRow(t, "A")
	uploading := makeSnapshotRow(t, "keep-uploading-01")
	failed := makeSnapshotRow(t, "keep-failed-0001")
	drop := makeSnapshotRow(t, "drop-orphan-0001")
	if err := UpsertBackupPlacement(uploading, tid, "UPLOADING", "/bk/"+uploading+".snbak", ""); err != nil {
		t.Fatal(err)
	}
	if err := UpsertBackupPlacement(failed, tid, "FAILED", "/bk/"+failed+".snbak", "boom"); err != nil {
		t.Fatal(err)
	}
	// drop 无放置

	n, err := PurgeOrphanBackupSnapshotsOnce()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应只清 1 条零放置孤儿，得 %d", n)
	}
	for _, keep := range []string{uploading, failed} {
		if ex, _ := SnapshotExists(keep); !ex {
			t.Fatalf("有放置行(%s)的快照不应被清理", keep)
		}
	}
	if ex, _ := SnapshotExists(drop); ex {
		t.Fatal("零放置孤儿应被清理")
	}
}

func mustListSnaps(t *testing.T) []BackupSnapshot {
	t.Helper()
	ss, err := ListBackupSnapshots(50)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}
