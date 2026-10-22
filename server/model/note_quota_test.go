package model

import (
	"errors"
	"path/filepath"
	"testing"

	"stickynote/database"
)

// model 层配额与回收站测试（v21.54 P0：配额/回收站口径——v18.4/v18.5/v21.7 回归点）
// 覆盖：CountActiveNotes 配额计数、回收站软删/恢复/永久删除/清空、TrashStats 统计。

func setupQuotaDB(t *testing.T) func() {
	t.Helper()
	if err := database.Init(filepath.Join(tempInstanceDir(t), "test.db"), "99.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	return func() {
		database.DB.Close()
		database.DB = nil
	}
}

// TestCountActiveNotes_ExcludesTrash 配额计数：软删（回收站）笔记不占配额。
func TestCountActiveNotes_ExcludesTrash(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "quota_user")
	for i := 0; i < 3; i++ {
		if _, err := CreateNote(uid, "n", "<p>x</p>", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	count, err := CountActiveNotes(uid)
	if err != nil || count != 3 {
		t.Fatalf("初始计数应 3, got %d err=%v", count, err)
	}

	// 软删一篇 → 计数应减为 2（回收站不占配额）
	notes, _ := ListNotes(uid, 1, 10, "", "", "created_at", nil)
	if len(notes.Notes) != 3 {
		t.Fatalf("列表应 3 篇, got %d", len(notes.Notes))
	}
	if err := DeleteNote(notes.Notes[0].ID, uid); err != nil {
		t.Fatal(err)
	}
	count, _ = CountActiveNotes(uid)
	if count != 2 {
		t.Errorf("软删后计数应 2, got %d", count)
	}
}

// TestTrashLifecycle 回收站全生命周期：软删→列表→恢复→再删→永久删除。
func TestTrashLifecycle(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "trash_user")
	n, _ := CreateNote(uid, "回收测试", "<p>内容</p>", "tag1", 0)

	// 软删进回收站
	if err := DeleteNote(n.ID, uid); err != nil {
		t.Fatal(err)
	}
	trash, err := ListTrashedNotes(uid, 1, 20)
	if err != nil || len(trash.Notes) != 1 {
		t.Fatalf("回收站应 1 篇, got %d err=%v", len(trash.Notes), err)
	}

	// 恢复
	if err := RestoreNote(n.ID, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := GetNote(n.ID, uid); err != nil {
		t.Fatal("恢复后应可读取")
	}

	// 再软删 → 永久删除
	if err := DeleteNote(n.ID, uid); err != nil {
		t.Fatal(err)
	}
	if err := PermanentDeleteNote(n.ID, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := GetNote(n.ID, uid); err == nil {
		t.Fatal("永久删除后不应存在")
	}
}

// TestEmptyTrashNotes 清空回收站：返回被清笔记列表，列表置空。
func TestEmptyTrashNotes(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "empty_user")
	for i := 0; i < 2; i++ {
		n, _ := CreateNote(uid, "t", "<p>x</p>", "", 0)
		DeleteNote(n.ID, uid)
	}

	cleared, _, err := EmptyTrashNotes(uid)
	if err != nil || len(cleared) != 2 {
		t.Fatalf("清空应返回 2 篇, got %d err=%v", len(cleared), err)
	}
	trash, _ := ListTrashedNotes(uid, 1, 20)
	if len(trash.Notes) != 0 {
		t.Errorf("清空后回收站应空, got %d", len(trash.Notes))
	}
}

// TestTrashStats 回收站统计：条数与引用图片字节（trash_upload_bytes）。
func TestTrashStats(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "stats_user")
	// v21.66 P1-4：统计已迁移到 note_uploads，附件须先在 uploads 表有归属记录
	if err := RecordUpload("abc.png", uid, 2048); err != nil {
		t.Fatal(err)
	}
	n, _ := CreateNote(uid, "含图笔记", `<p><img src="/uploads/abc.png"></p>`, "", 0)
	if err := DeleteNote(n.ID, uid); err != nil {
		t.Fatal(err)
	}

	count, uploadBytes, err := GetTrashStats(uid)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("回收站条数应 1, got %d", count)
	}
	if uploadBytes != 2048 {
		t.Errorf("引用图片字节应 2048, got %d", uploadBytes)
	}
}

// TestCreateNote_QuotaTransactional P1（v21.58）：事务内配额检查——
// 超限返回 ErrQuotaExceeded 且不产生任何笔记；未超限正常创建且标签入库。
func TestCreateNote_QuotaTransactional(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := int64(1)
	if _, err := database.DB.Exec("INSERT INTO users (id, username, password_hash) VALUES (?, 'q', 'h')", uid); err != nil {
		t.Fatal(err)
	}

	// 上限 2：创建 2 篇成功
	if _, err := CreateNote(uid, "n1", "<p>a</p>", "t1", 2); err != nil {
		t.Fatalf("第1篇应成功: %v", err)
	}
	if _, err := CreateNote(uid, "n2", "<p>b</p>", "", 2); err != nil {
		t.Fatalf("第2篇应成功: %v", err)
	}

	// 第 3 篇超限 → ErrQuotaExceeded，且不产生笔记
	_, err := CreateNote(uid, "n3", "<p>c</p>", "", 2)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("应返回 ErrQuotaExceeded, got %v", err)
	}
	var cnt int
	database.DB.QueryRow("SELECT COUNT(*) FROM notes WHERE user_id = ? AND deleted_at IS NULL", uid).Scan(&cnt)
	if cnt != 2 {
		t.Fatalf("超限请求不应插入笔记, got %d want 2", cnt)
	}

	// 标签事务：n1 的标签应入库（同事务提交）
	var tagCnt int
	database.DB.QueryRow("SELECT COUNT(*) FROM note_tags WHERE note_id = (SELECT id FROM notes WHERE user_id=? AND title='n1')", uid).Scan(&tagCnt)
	if tagCnt != 1 {
		t.Fatalf("标签应随创建事务入库, got %d", tagCnt)
	}

	// maxNotes=0 不限
	if _, err := CreateNote(uid, "n4", "<p>d</p>", "", 0); err != nil {
		t.Fatalf("maxNotes=0 应不限: %v", err)
	}
}
