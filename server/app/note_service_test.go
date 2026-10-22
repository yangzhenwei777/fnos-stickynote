package app

import (
	"path/filepath"
	"testing"

	"stickynote/database"
)

// setupDB 真库工装（临时目录 + seed 本地用户行，满足 notes.user_id FK）。
func setupDB(t *testing.T) {
	t.Helper()
	if err := database.Init(filepath.Join(t.TempDir(), "app.db"), "99.0.0"); err != nil {
		t.Fatalf("database.Init: %v", err)
	}
	t.Cleanup(func() {
		if database.DB != nil {
			database.DB.Close()
			database.DB = nil
		}
	})
	if _, err := database.DB.Exec(
		`INSERT OR IGNORE INTO users (id, username, password_hash, role) VALUES (1, 'local', '!', 'user')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func TestNoteServiceCRUDAndCAS(t *testing.T) {
	setupDB(t)
	// E4-4：CAS+bump 是**服务端**语义（Web 路径），改用 NewServerNoteService 构造——
	// 桌面构造器（NewNoteService）Update 不 bump 不 CAS（P0-1 不变量，2026-09-23 修正）。
	svc := NewServerNoteService(100)

	// Create → 权威行带 ID/Version=1。
	n, err := svc.CreateNote(1, "标题A", "<p>正文</p>", "标签1,标签2", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if n.ID == 0 || n.Version != 1 {
		t.Fatalf("create 返回行不符: id=%d version=%d", n.ID, n.Version)
	}

	// Get 往返。
	got, err := svc.GetNote(n.ID, 1)
	if err != nil || got.Title != "标题A" {
		t.Fatalf("GetNote: %v %+v", err, got)
	}

	// Update：正确 baseVersion → 成功且 version 前进；错误 baseVersion → ErrVersionConflict。
	up, err := svc.UpdateNote(n.ID, 1, "标题B", "<p>新</p>", "", 1)
	if err != nil || up.Version != 2 || up.Title != "标题B" {
		t.Fatalf("UpdateNote 权威回读不符: err=%v ver=%+v", err, up)
	}
	if _, err := svc.UpdateNote(n.ID, 1, "stale", "", "", 1); err == nil || !isConflict(err) {
		t.Fatalf("过期 baseVersion 应 VERSION_CONFLICT，得 %v", err)
	}

	// List（分页 + 搜索走服务端同源 FTS/LIKE 路径）。
	lst, err := svc.ListNotes(1, 1, 10, "", "", "", nil)
	if err != nil || lst.Total != 1 {
		t.Fatalf("ListNotes: %v %+v", err, lst)
	}

	// Delete → 软删后 Get 不可见（ErrNotFound 语义）。
	if err := svc.DeleteNote(n.ID, 1); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if _, err := svc.GetNote(n.ID, 1); err == nil {
		t.Fatal("软删后 GetNote 应失败")
	}
}

func TestErrorCodeMapping(t *testing.T) {
	setupDB(t)
	// E4-4：VERSION_CONFLICT 映射是服务端 CAS 语义，用 NewServerNoteService。
	svc := NewServerNoteService(100)
	n, err := svc.CreateNote(1, "t", "c", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateNote(n.ID, 1, "x", "", "", 999)
	if code := ErrorCodeFor(err); code != "VERSION_CONFLICT" {
		t.Fatalf("错误码映射失败: %v → %q", err, code)
	}
	if code := ErrorCodeFor(nil); code != "" {
		t.Fatalf("nil 错误码应为空串，得 %q", code)
	}
	if code := ErrorCodeFor(ErrInvalidFolder); code != "INVALID_FOLDER" {
		t.Fatalf("INVALID_FOLDER 映射失败: %q", code)
	}
}

func isConflict(err error) bool { return ErrorCodeFor(err) == "VERSION_CONFLICT" }
