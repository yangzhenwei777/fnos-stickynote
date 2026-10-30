package model

import (
	"errors"
	"testing"

	"stickynote/database"
)

// v2.3.0 文件夹访问锁：模型层过滤与锁定态持久化。

func lockFolderForTest(t *testing.T, id, uid int64, hash string) {
	t.Helper()
	tx, err := database.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := SetFolderEncryptedTx(tx, id, uid, hash); err != nil {
		tx.Rollback()
		t.Fatalf("lock folder: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestFolderLockSetClear(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()
	uid := createUserForTest(t, "lock_set_clear")
	f, err := CreateFolder(uid, "秘密")
	if err != nil {
		t.Fatal(err)
	}

	lockFolderForTest(t, *f.ID, uid, "hash_abc")
	name, encrypted, hash, version, err := GetFolderEncryptInfo(*f.ID, uid)
	if err != nil {
		t.Fatal(err)
	}
	if name != "秘密" || !encrypted || hash != "hash_abc" || version != 1 {
		t.Fatalf("锁定态不符: name=%s enc=%v hash=%s ver=%d", name, encrypted, hash, version)
	}
	// 锁定不应由 model 推进 version（服务端语义在 app 层补齐）
	if _, _, _, after, _ := GetFolderEncryptInfo(*f.ID, uid); after != 1 {
		t.Fatalf("model 不应 bump version: got %d", after)
	}

	tx, _ := database.DB.Begin()
	if err := SetFolderEncryptedTx(tx, *f.ID, uid, "another"); !errors.Is(err, ErrFolderAlreadyEncrypted) {
		tx.Rollback()
		t.Fatalf("重复锁定应报 ErrFolderAlreadyEncrypted, got %v", err)
	}
	tx.Rollback()

	tx, _ = database.DB.Begin()
	if err := ClearFolderEncryptedTx(tx, *f.ID, uid); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	tx.Commit()
	_, encrypted, hash, _, _ = GetFolderEncryptInfo(*f.ID, uid)
	if encrypted || hash != "" {
		t.Fatalf("解密后应清空: enc=%v hash=%s", encrypted, hash)
	}
}

func TestFolderLockHidesNotesEverywhere(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()
	uid := createUserForTest(t, "lock_filter")
	f, err := CreateFolder(uid, "私密")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateNote(uid, "公开笔记", "<p>open</p>", "openTag", 0); err != nil {
		t.Fatal(err)
	}
	fid := *f.ID
	secret, err := CreateNoteInFolder(uid, "秘密笔记", "<p>secret</p>", "secretTag", 0, &fid)
	if err != nil {
		t.Fatal(err)
	}
	lockFolderForTest(t, *f.ID, uid, "hash_filter")

	// 列表：未解锁仅剩默认文件夹笔记
	res, err := ListNotes(uid, 1, 20, "", "", "updated_at", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Notes) != 1 || res.Notes[0].Title != "公开笔记" {
		t.Fatalf("未解锁列表应只含公开笔记: total=%d notes=%+v", res.Total, res.Notes)
	}
	// 指定锁定文件夹：空
	res, err = ListNotes(uid, 1, 20, "", "", "updated_at", &fid)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 {
		t.Fatalf("指定锁定文件夹应返回空, got %d", res.Total)
	}
	// 搜索命中隐藏标题：无结果
	res, _ = ListNotes(uid, 1, 20, "秘密", "", "updated_at", nil)
	if res.Total != 0 {
		t.Fatalf("搜索不应命中锁定笔记, got %d", res.Total)
	}

	// 加授权后全部可见
	grant := map[int64]struct{}{*f.ID: {}}
	res, err = ListNotes(uid, 1, 20, "", "", "updated_at", nil, grant)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("授权后应 2 条, got %d", res.Total)
	}

	// 标签聚合
	tags, err := GetAllTags(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0] != "openTag" {
		t.Fatalf("未解锁标签应只含 openTag, got %v", tags)
	}
	tags, _ = GetAllTags(uid, grant)
	if len(tags) != 2 {
		t.Fatalf("授权后标签应 2 个, got %v", tags)
	}

	// 单篇详情判定
	locked, err := FolderLockedForNote(secret.ID, uid, nil)
	if err != nil || !locked {
		t.Fatalf("锁定文件夹内笔记应判定 locked: locked=%v err=%v", locked, err)
	}
	locked, _ = FolderLockedForNote(secret.ID, uid, grant)
	if locked {
		t.Fatal("授权后不应判定 locked")
	}

	// 文件夹计数：未解锁 0，授权后真实
	folders, _ := ListFolders(uid)
	for _, item := range folders {
		if item.ID != nil && *item.ID == *f.ID {
			if item.NoteCount != 0 || !item.IsEncrypted {
				t.Fatalf("锁定文件夹计数应为 0 且标记加密: %+v", item)
			}
		}
	}
	folders, _ = ListFolders(uid, grant)
	for _, item := range folders {
		if item.ID != nil && *item.ID == *f.ID && item.NoteCount != 1 {
			t.Fatalf("授权后计数应为 1: %+v", item)
		}
	}

	// 导出计数（在移入回收站前统计正常笔记）
	if n, err := LockedNoteCount(uid, nil); err != nil || n != 1 {
		t.Fatalf("LockedNoteCount 未解锁应 1, got %d err=%v", n, err)
	}
	if n, _ := LockedNoteCount(uid, grant); n != 0 {
		t.Fatalf("授权后 LockedNoteCount 应 0, got %d", n)
	}

	// 回收站过滤
	if err := DeleteNote(secret.ID, uid); err != nil {
		t.Fatal(err)
	}
	trash, err := ListTrashedNotes(uid, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if trash.Total != 0 {
		t.Fatalf("回收站不应显示锁定文件夹笔记, got %d", trash.Total)
	}
	trash, _ = ListTrashedNotes(uid, 1, 20, grant)
	if trash.Total != 1 {
		t.Fatalf("授权后回收站应 1 条, got %d", trash.Total)
	}
}
