package synclocal

import (
	"encoding/json"
	"testing"

	"stickynote/syncspec"
)

// v2.3.0：桌面拉取锁定字段；旧载荷缺省必须保留本地锁定态。
func TestApplyFolderChangeLockFields(t *testing.T) {
	db := newStore(t)
	if _, err := db.Exec(`INSERT INTO note_folders (id, user_id, name, sort_order, sync_id, version, is_encrypted, encrypt_password_hash)
		VALUES (1, 1, '锁夹', 1, 'fs-lock', 1, 1, 'hash_keep')`); err != nil {
		t.Fatal(err)
	}
	w := &Worker{db: db}
	apply := func(p syncspec.FolderPayload, ver int64) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(p)
		if err := w.applyFolderChange(tx, syncspec.ChangeDTO{
			ChangeSeq: 1, EntityType: syncspec.EntityTypeFolder, Operation: "UPDATE",
			ServerVersion: ver, Payload: raw,
		}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	// 旧载荷：不带锁字段 → 保留锁定
	apply(syncspec.FolderPayload{SyncID: "fs-lock", Name: "改名", SortOrder: 1}, 2)
	var enc int
	var hash string
	if err := db.QueryRow(`SELECT is_encrypted, encrypt_password_hash FROM note_folders WHERE sync_id = 'fs-lock'`).
		Scan(&enc, &hash); err != nil {
		t.Fatal(err)
	}
	if enc != 1 || hash != "hash_keep" {
		t.Fatalf("缺省字段不应清锁: %d %q", enc, hash)
	}

	// 显式解锁 → 清锁
	dec := false
	empty := ""
	apply(syncspec.FolderPayload{SyncID: "fs-lock", Name: "改名", SortOrder: 1, IsEncrypted: &dec, EncryptPasswordHash: &empty}, 3)
	if err := db.QueryRow(`SELECT is_encrypted, encrypt_password_hash FROM note_folders WHERE sync_id = 'fs-lock'`).
		Scan(&enc, &hash); err != nil {
		t.Fatal(err)
	}
	if enc != 0 || hash != "" {
		t.Fatalf("显式解锁未清锁: %d %q", enc, hash)
	}

	// CREATE 带锁字段
	encTrue := true
	h := "hash_new"
	applyCreate := func() {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(syncspec.FolderPayload{SyncID: "fs-new", Name: "新锁夹", SortOrder: 2, IsEncrypted: &encTrue, EncryptPasswordHash: &h})
		if err := w.applyFolderChange(tx, syncspec.ChangeDTO{
			ChangeSeq: 2, EntityType: syncspec.EntityTypeFolder, Operation: "CREATE",
			ServerVersion: 1, Payload: raw,
		}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	applyCreate()
	if err := db.QueryRow(`SELECT is_encrypted, encrypt_password_hash FROM note_folders WHERE sync_id = 'fs-new'`).
		Scan(&enc, &hash); err != nil {
		t.Fatal(err)
	}
	if enc != 1 || hash != "hash_new" {
		t.Fatalf("CREATE 锁字段未落库: %d %q", enc, hash)
	}
}
