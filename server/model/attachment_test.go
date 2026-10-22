package model

import (
	"os"
	"path/filepath"
	"testing"

	"stickynote/database"
)

// TestCreateNote_SyncsUploads 创建笔记同步附件关系（v21.58 F-02）。
func TestCreateNote_SyncsUploads(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_att")
	// v21.66 P1-6：附件须先在 uploads 表有归属记录，SyncNoteUploadsTx 才能建立关系
	if err := RecordUpload("aaa.png", uid, 100); err != nil {
		t.Fatal(err)
	}
	if err := RecordUpload("bbb.jpg", uid, 200); err != nil {
		t.Fatal(err)
	}
	note, err := CreateNote(uid, "含图笔记", `<p><img src="/uploads/aaa.png"><img src="/uploads/bbb.jpg"></p>`, "", 0)
	if err != nil {
		t.Fatal(err)
	}

	names, err := database.ListNoteUploads(note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("附件关系应 2 条, got %d (%v)", len(names), names)
	}
}

// TestEncryptedNoteAttachmentLifecycle 加密笔记附件生命周期（v21.58 F-02 核心）：
// 更新加密笔记（明文含附件）→ 关系写入 → 永久删除 → 级联删除 → 孤儿文件清理。
func TestEncryptedNoteAttachmentLifecycle(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uploadDir := tempInstanceDir(t)
	fname := "test-uuid.png"
	if err := os.WriteFile(filepath.Join(uploadDir, fname), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	uid := createUserForTest(t, "user_att_enc")
	nid := createEncryptedNoteForTest(t, uid, "加密含图")

	// v21.66 P1-6：附件须先在 uploads 表有归属记录
	if err := RecordUpload(fname, uid, 100); err != nil {
		t.Fatal(err)
	}

	// 加密笔记更新：明文含附件引用 → note_uploads 写入（即使 content 落库为空）
	plain := `<p><img src="/uploads/` + fname + `"></p>`
	if err := UpdateEncryptedNote(nid, uid, "t", "", "hash", "cipher", plain, 1); err != nil {
		t.Fatal(err)
	}
	names, _ := database.ListNoteUploads(nid)
	if len(names) != 1 || names[0] != fname {
		t.Fatalf("加密笔记附件关系应含 %s, got %v", fname, names)
	}

	// 软删除 → 回收站 → 永久删除（note_uploads 随 notes 级联删除）
	if err := DeleteNote(nid, uid); err != nil {
		t.Fatal(err)
	}
	if err := PermanentDeleteNote(nid, uid); err != nil {
		t.Fatal(err)
	}

	// 清理无引用孤儿文件
	cleaned := database.DeleteOrphanUploadFiles(uploadDir, []string{fname}, uid)
	if cleaned != 1 {
		t.Fatalf("应清理 1 个孤儿文件, got %d", cleaned)
	}
	if _, err := os.Stat(filepath.Join(uploadDir, fname)); !os.IsNotExist(err) {
		t.Errorf("孤儿文件应被删除")
	}
}

// TestAttachmentStillReferencedKept 附件被其他笔记引用时不删除（F-02 引用计数）。
func TestAttachmentStillReferencedKept(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uploadDir := tempInstanceDir(t)
	fname := "shared.png"
	if err := os.WriteFile(filepath.Join(uploadDir, fname), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	uid := createUserForTest(t, "user_att_shared")
	// v21.66 P1-6：附件须先在 uploads 表有归属记录
	if err := RecordUpload(fname, uid, 100); err != nil {
		t.Fatal(err)
	}
	// 两篇笔记引用同一附件
	n1, _ := CreateNote(uid, "n1", `<p><img src="/uploads/`+fname+`"></p>`, "", 0)
	n2, _ := CreateNote(uid, "n2", `<p><img src="/uploads/`+fname+`"></p>`, "", 0)

	// 删除 n1 → 附件仍被 n2 引用，不应删除
	if err := DeleteNote(n1.ID, uid); err != nil {
		t.Fatal(err)
	}
	if err := PermanentDeleteNote(n1.ID, uid); err != nil {
		t.Fatal(err)
	}
	cleaned := database.DeleteOrphanUploadFiles(uploadDir, []string{fname}, uid)
	if cleaned != 0 {
		t.Errorf("仍被 n2 引用，不应清理, got %d", cleaned)
	}
	if _, err := os.Stat(filepath.Join(uploadDir, fname)); err != nil {
		t.Errorf("共享附件应保留: %v", err)
	}
	_ = n2
}

// TestCrossUserUploadRefRejected 跨用户引用不建立附件关系（v21.66 P1-6）。
func TestCrossUserUploadRefRejected(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uidA := createUserForTest(t, "user_a")
	uidB := createUserForTest(t, "user_b")

	fname := "b-file.png"
	if err := RecordUpload(fname, uidB, 100); err != nil {
		t.Fatal(err)
	}

	note, err := CreateNote(uidA, "a-note", `<p><img src="/uploads/`+fname+`"></p>`, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	names, _ := database.ListNoteUploads(note.ID)
	if len(names) != 0 {
		t.Fatalf("跨用户引用不应建立附件关系, got %v", names)
	}
}

// TestCreateNoteVersionAndUpdateIncrement 新建笔记 version=1、连续更新递增（v21.75 修复 409 bug）。
func TestCreateNoteVersionAndUpdateIncrement(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_ver")
	note, err := CreateNote(uid, "版本测试", "<p>x</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// v21.75：新建笔记 version 必须与 DB DEFAULT 1 对齐，否则前端拿 0 首次保存就 409
	if note.Version != 1 {
		t.Fatalf("新建笔记 version 应=1, got %d", note.Version)
	}

	// 首次更新（version=1）→ 成功，version 变 2
	if err := UpdateNote(note.ID, uid, "t2", "<p>y</p>", "", note.Version); err != nil {
		t.Fatalf("首次更新应成功: %v", err)
	}
	// 旧 version=1 再更新 → 应冲突
	if err := UpdateNote(note.ID, uid, "t3", "<p>z</p>", "", 1); err != ErrVersionConflict {
		t.Fatalf("旧 version 更新应返回 ErrVersionConflict, got %v", err)
	}
	// 新 version=2 更新 → 成功
	if err := UpdateNote(note.ID, uid, "t4", "<p>w</p>", "", 2); err != nil {
		t.Fatalf("新 version 更新应成功: %v", err)
	}
}
