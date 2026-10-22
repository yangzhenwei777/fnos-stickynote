package synclocal

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"stickynote/syncspec"
)

// 批次 6 走查坐实真缺陷钉测：他机图片笔记首次 pull 时，note_uploads 引用的 uploads
// 父行必须被自动补桩（生产 schema v21.71 起 note_uploads→uploads 有真 FK；批次 3 集成
// 工装未开 PRAGMA foreign_keys 掩盖了缺口——修复前此测试 FK 红）。
func TestPullImageNoteCreatesUploadsParentStub(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fk-pull.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT)`,
		`CREATE TABLE notes (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, title TEXT,
			content TEXT, content_text TEXT, tags TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME, folder_id INTEGER,
			is_encrypted INTEGER DEFAULT 0, encrypt_password_hash TEXT DEFAULT '',
			encrypted_content TEXT DEFAULT '', version INTEGER DEFAULT 0, sync_id TEXT,
			trash_upload_bytes INTEGER DEFAULT 0)`,
		`CREATE TABLE note_folders (id INTEGER PRIMARY KEY, user_id INTEGER, name TEXT,
			sort_order INTEGER, sync_id TEXT, version INTEGER DEFAULT 0)`,
		`CREATE TABLE uploads (id INTEGER PRIMARY KEY AUTOINCREMENT, filename TEXT NOT NULL UNIQUE,
			user_id INTEGER, size INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			status TEXT, status_changed_at DATETIME, sha256 TEXT DEFAULT '')`,
		`CREATE TABLE note_uploads (note_id INTEGER NOT NULL, filename TEXT NOT NULL,
			FOREIGN KEY (filename) REFERENCES uploads(filename))`,
		`CREATE TABLE note_tags (id INTEGER PRIMARY KEY AUTOINCREMENT, note_id INTEGER, tag TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureTables(db); err != nil {
		t.Fatal(err)
	}
	// FK 生效自证：直接插孤立 note_uploads 必须失败
	if _, err := db.Exec(`INSERT INTO note_uploads (note_id, filename) VALUES (1,'ghost.png')`); err == nil ||
		!strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("test schema must enforce FK, got %v", err)
	}

	const fname = "11111111-2222-3333-4444-555555555555.png"
	ch := syncspec.ChangeDTO{ChangeSeq: 1, Operation: "CREATE", ServerVersion: 1,
		Payload: json.RawMessage(`{"sync_id":"s-fk-1","title":"带图","content":"<img src=\"/uploads/` + fname + `\"/>",
			"attachment_refs":[{"filename":"` + fname + `","sha256":"aa","size":42}]}`)}
	w := &Worker{db: db, AttachDir: t.TempDir()}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.applyNoteChange(tx, ch); err != nil {
		t.Fatalf("apply image note under real FK must succeed after parent stub fix: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var sha, status string
	var size int64
	if err := db.QueryRow(`SELECT sha256, status, size FROM uploads WHERE filename=?`, fname).
		Scan(&sha, &status, &size); err != nil {
		t.Fatalf("uploads parent stub missing: %v", err)
	}
	if sha != "aa" || status != "attached" || size != 42 {
		t.Fatalf("stub fields wrong: %q %q %d", sha, status, size)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM note_uploads WHERE filename=?`, fname).Scan(&n); err != nil || n != 1 {
		t.Fatalf("note_uploads link: n=%d err=%v", n, err)
	}
	var qStatus string
	if err := db.QueryRow(`SELECT status FROM sync_attachments WHERE filename=?`, fname).Scan(&qStatus); err != nil || qStatus != "PENDING" {
		t.Fatalf("download queue must be PENDING (file not local): %q %v", qStatus, err)
	}
}
