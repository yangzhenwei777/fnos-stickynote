package database_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"stickynote/database"
	"stickynote/model"
)

func setupDB(t *testing.T) {
	t.Helper()
	if err := database.Init(filepath.Join(t.TempDir(), "test.db"), "1.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() {
		database.DB.Close()
		database.DB = nil
	})
}

func setRetention(t *testing.T, days string) {
	t.Helper()
	if _, err := database.DB.Exec("UPDATE configs SET value = ? WHERE key = 'trash_retention_days'", days); err != nil {
		t.Fatalf("set retention: %v", err)
	}
}

// 回收站过期清理（P2-6/P2-4）：trash_retention_days>0 时物理删除超期笔记，
// 未超期笔记不受影响。
func TestCleanExpiredTrash(t *testing.T) {
	setupDB(t)
	setRetention(t, "1")

	u, err := model.RegisterUser("trashuser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// 正常笔记
	n1, err := model.CreateNote(u.ID, "active", "<p>active</p>", "", 0)
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	// 回收站中但未超期
	n2, err := model.CreateNote(u.ID, "recent", "<p>recent</p>", "", 0)
	if err != nil {
		t.Fatalf("create recent: %v", err)
	}
	if err := model.DeleteNote(n2.ID, u.ID); err != nil {
		t.Fatalf("soft delete recent: %v", err)
	}
	// 回收站中且已超期（deleted_at 拨到 3 天前）
	n3, err := model.CreateNote(u.ID, "expired", "<p>expired</p>", "", 0)
	if err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if err := model.DeleteNote(n3.ID, u.ID); err != nil {
		t.Fatalf("soft delete expired: %v", err)
	}
	if _, err := database.DB.Exec("UPDATE notes SET deleted_at = ? WHERE id = ?", time.Now().Add(-72*time.Hour), n3.ID); err != nil {
		t.Fatalf("backdate deleted_at: %v", err)
	}

	database.CleanExpiredTrash(t.TempDir())

	// 超期笔记已物理删除
	var cnt int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM notes WHERE id = ?", n3.ID).Scan(&cnt); err != nil {
		t.Fatalf("count expired: %v", err)
	}
	if cnt != 0 {
		t.Errorf("超期笔记应被物理删除, got %d rows", cnt)
	}
	// 未超期回收站笔记保留
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM notes WHERE id = ?", n2.ID).Scan(&cnt); err != nil {
		t.Fatalf("count recent: %v", err)
	}
	if cnt != 1 {
		t.Errorf("未超期回收站笔记应保留, got %d rows", cnt)
	}
	// 正常笔记不受影响
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM notes WHERE id = ?", n1.ID).Scan(&cnt); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if cnt != 1 {
		t.Errorf("正常笔记不应被误删, got %d rows", cnt)
	}
}

// 默认 retention=0：永久保留语义，不清理任何回收站笔记
func TestCleanExpiredTrashZeroRetentionKeepsAll(t *testing.T) {
	setupDB(t)

	u, err := model.RegisterUser("keepuser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	n, err := model.CreateNote(u.ID, "old", "<p>old</p>", "", 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := model.DeleteNote(n.ID, u.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if _, err := database.DB.Exec("UPDATE notes SET deleted_at = ? WHERE id = ?", time.Now().Add(-30*24*time.Hour), n.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	database.CleanExpiredTrash(t.TempDir())

	var cnt int
	database.DB.QueryRow("SELECT COUNT(*) FROM notes WHERE id = ?", n.ID).Scan(&cnt)
	if cnt != 1 {
		t.Errorf("retention=0 时应永久保留（不清理）, got %d rows", cnt)
	}
}

// delete_pending 状态的物理文件被 CleanPendingUploads 清理（v21.70 状态机统一）。
func TestCleanPendingUploads_DeletePending(t *testing.T) {
	setupDB(t)

	uploadDir := t.TempDir()
	u, err := model.RegisterUser("dpuser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	fname := "dp-file.png"
	if err := os.WriteFile(filepath.Join(uploadDir, fname), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(
		"INSERT INTO uploads (filename, user_id, size, status, created_at) VALUES (?, ?, 1, 'delete_pending', CURRENT_TIMESTAMP)",
		fname, u.ID,
	); err != nil {
		t.Fatal(err)
	}

	if cleaned := database.CleanPendingUploads(uploadDir); cleaned != 1 {
		t.Fatalf("应清理 1 个 delete_pending 文件, got %d", cleaned)
	}
	if _, err := os.Stat(filepath.Join(uploadDir, fname)); !os.IsNotExist(err) {
		t.Errorf("物理文件应被删除")
	}
	var cnt int
	_ = database.DB.QueryRow("SELECT COUNT(*) FROM uploads WHERE filename = ?", fname).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("uploads 记录应被删除, cnt=%d", cnt)
	}
}

// note_uploads 的 FK 约束：插入 filename 不在 uploads 的孤儿关系应被拒绝（v21.71 P2-02）。
func TestNoteUploadsFKConstraint(t *testing.T) {
	setupDB(t)

	u, err := model.RegisterUser("fkuser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	n, err := model.CreateNote(u.ID, "n", "<p>x</p>", "", 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := database.DB.Exec("INSERT INTO note_uploads (note_id, filename) VALUES (?, 'nonexistent.png')", n.ID); err == nil {
		t.Errorf("FK 约束应拒绝孤儿关系（filename 不在 uploads）")
	}
}
