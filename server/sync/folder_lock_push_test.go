package sync

import (
	"testing"

	"stickynote/database"
	"stickynote/syncspec"
)

// v2.3.0：FOLDER 同步锁字段 —— 旧载荷缺省必须保留现状，显式 false 才清锁。
func TestFolderPushLockFieldsPreserveOnMissing(t *testing.T) {
	r := setupSyncDB(t)
	doPush(t, r, "devA", folderCreateOp("lf-1", "lfs-1", "锁夹"))

	enc := true
	hash := "hash_1"
	lockPayload := syncspec.FolderPayload{SyncID: "lfs-1", Name: "锁夹", IsEncrypted: &enc, EncryptPasswordHash: &hash}
	base1 := int64(1)
	res := doPush(t, r, "devA", PushOp{
		OpID: "lf-2", EntityType: syncspec.EntityTypeFolder, EntitySyncID: "lfs-1",
		Operation: "UPDATE", BaseVersion: &base1, Payload: mustJSON(lockPayload),
	})
	if res[0].Code != CodeAcked || res[0].ServerVersion != 2 {
		t.Fatalf("锁定更新应 ACKED v2: %+v", res)
	}
	var encInt int
	var gotHash string
	if err := database.DB.QueryRow(`SELECT is_encrypted, encrypt_password_hash FROM note_folders WHERE sync_id = 'lfs-1'`).
		Scan(&encInt, &gotHash); err != nil {
		t.Fatal(err)
	}
	if encInt != 1 || gotHash != "hash_1" {
		t.Fatalf("锁定字段未落库: %d %q", encInt, gotHash)
	}

	// 旧客户端重命名：payload 不带锁字段 → 锁定态必须保留
	base2 := int64(2)
	res = doPush(t, r, "devA", PushOp{
		OpID: "lf-3", EntityType: syncspec.EntityTypeFolder, EntitySyncID: "lfs-1",
		Operation: "UPDATE", BaseVersion: &base2,
		Payload: mustJSON(syncspec.FolderPayload{SyncID: "lfs-1", Name: "锁夹改名"}),
	})
	if res[0].Code != CodeAcked {
		t.Fatalf("重命名应 ACKED: %+v", res)
	}
	if err := database.DB.QueryRow(`SELECT is_encrypted, encrypt_password_hash FROM note_folders WHERE sync_id = 'lfs-1'`).
		Scan(&encInt, &gotHash); err != nil {
		t.Fatal(err)
	}
	if encInt != 1 || gotHash != "hash_1" {
		t.Fatalf("缺省字段不应清锁: %d %q", encInt, gotHash)
	}

	// 显式解锁：is_encrypted=false + 空哈希 → 清锁
	base3 := int64(3)
	dec := false
	empty := ""
	res = doPush(t, r, "devA", PushOp{
		OpID: "lf-4", EntityType: syncspec.EntityTypeFolder, EntitySyncID: "lfs-1",
		Operation: "UPDATE", BaseVersion: &base3,
		Payload: mustJSON(syncspec.FolderPayload{SyncID: "lfs-1", Name: "锁夹改名", IsEncrypted: &dec, EncryptPasswordHash: &empty}),
	})
	if res[0].Code != CodeAcked {
		t.Fatalf("解锁应 ACKED: %+v", res)
	}
	if err := database.DB.QueryRow(`SELECT is_encrypted, encrypt_password_hash FROM note_folders WHERE sync_id = 'lfs-1'`).
		Scan(&encInt, &gotHash); err != nil {
		t.Fatal(err)
	}
	if encInt != 0 || gotHash != "" {
		t.Fatalf("显式解锁未清锁: %d %q", encInt, gotHash)
	}

	// CREATE 带锁字段（新设备首次同步锁定夹）
	res = doPush(t, r, "devB", PushOp{
		OpID: "lf-5", EntityType: syncspec.EntityTypeFolder, EntitySyncID: "lfs-2",
		Operation: "CREATE", BaseVersion: new(int64),
		Payload: mustJSON(syncspec.FolderPayload{SyncID: "lfs-2", Name: "新建锁夹", IsEncrypted: &enc, EncryptPasswordHash: &hash}),
	})
	if res[0].Code != CodeAcked {
		t.Fatalf("带锁 CREATE 应 ACKED: %+v", res)
	}
	if err := database.DB.QueryRow(`SELECT is_encrypted, encrypt_password_hash FROM note_folders WHERE sync_id = 'lfs-2'`).
		Scan(&encInt, &gotHash); err != nil {
		t.Fatal(err)
	}
	if encInt != 1 || gotHash != "hash_1" {
		t.Fatalf("CREATE 锁字段未落库: %d %q", encInt, gotHash)
	}
}
