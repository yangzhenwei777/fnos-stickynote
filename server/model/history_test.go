package model

import (
	"database/sql"
	"testing"

	"stickynote/database"
)

func historyCount(t *testing.T, noteID int64) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM note_history WHERE note_id = ?", noteID).Scan(&n); err != nil {
		t.Fatalf("count history: %v", err)
	}
	return n
}

func TestCaptureNoteHistoryDedup(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "history_dedup")
	note, err := CreateNote(uid, "历史笔记", "<p>v1</p>", "标签", 0)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser)
	if err != nil || !ok {
		t.Fatalf("first capture: ok=%v err=%v", ok, err)
	}
	ok, err = CaptureNoteHistory(note.ID, uid, HistorySourceAuto)
	if err != nil || ok {
		t.Fatalf("same content should dedup: ok=%v err=%v", ok, err)
	}
	if got := historyCount(t, note.ID); got != 1 {
		t.Fatalf("history count=%d want 1", got)
	}
}

func TestCaptureNoteHistorySkipsEncrypted(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "history_enc")
	note, err := CreateNote(uid, "加密笔记", "<p>secret</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetEncrypted(note.ID, uid, "hash", "cipher"); err != nil {
		t.Fatal(err)
	}
	ok, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser)
	if err != nil || ok {
		t.Fatalf("encrypted note should be skipped: ok=%v err=%v", ok, err)
	}
	if got := historyCount(t, note.ID); got != 0 {
		t.Fatalf("history count=%d want 0", got)
	}
}

func TestListNoteHistoryAndDetail(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "history_list")
	note, err := CreateNote(uid, "列表笔记", "<p>v1</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 3; i++ {
		if err := UpdateNote(note.ID, uid, "列表笔记", "<p>v"+string(rune('0'+i))+"</p>", "", int64(i-1)); err != nil {
			t.Fatalf("update v%d: %v", i, err)
		}
		if _, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := ListNoteHistory(uid, note.ID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 || len(resp.History) != 2 {
		t.Fatalf("total=%d len=%d want 3/2", resp.Total, len(resp.History))
	}
	detail, err := GetNoteHistory(uid, note.ID, resp.History[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Content == "" {
		t.Fatal("detail content empty")
	}

	other := createUserForTest(t, "history_other")
	if _, err := GetNoteHistory(other, note.ID, resp.History[0].ID); err != sql.ErrNoRows {
		t.Fatalf("cross-user history should be not found, got %v", err)
	}
}

func TestSnapshotRecentNotesOnlyChanged(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "history_snapshot")
	note, err := CreateNote(uid, "快照笔记", "<p>v1</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser); err != nil {
		t.Fatal(err)
	}
	if err := UpdateNote(note.ID, uid, "快照笔记", "<p>v2</p>", "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec("UPDATE note_history SET created_at = '2020-01-01 00:00:00' WHERE note_id = ?", note.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec("UPDATE notes SET updated_at = '2026-11-04 12:00:00' WHERE id = ?", note.ID); err != nil {
		t.Fatal(err)
	}
	n, err := SnapshotRecentNotes(200)
	if err != nil || n != 1 {
		t.Fatalf("first snapshot n=%d err=%v", n, err)
	}
	n, err = SnapshotRecentNotes(200)
	if err != nil || n != 0 {
		t.Fatalf("second snapshot should dedup n=%d err=%v", n, err)
	}
}

func TestCleanupNoteHistoryLimits(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "history_cleanup")
	note, err := CreateNote(uid, "清理笔记", "<p>v1</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := database.DB.Exec(
			"INSERT INTO note_history (note_id, user_id, title, content, tags, content_hash, size_bytes, source) VALUES (?, ?, 't', ?, '', ?, 10, 'user')",
			note.ID, uid, "<p>"+string(rune('a'+i))+"</p>", "hash-"+string(rune('a'+i)),
		); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := CleanupNoteHistory(2, 0, 0)
	if err != nil || removed != 3 {
		t.Fatalf("removed=%d err=%v want 3", removed, err)
	}
	if got := historyCount(t, note.ID); got != 2 {
		t.Fatalf("after max-per-note count=%d want 2", got)
	}

	if _, err := database.DB.Exec("UPDATE note_history SET created_at = '2020-01-01 00:00:00' WHERE note_id = ?", note.ID); err != nil {
		t.Fatal(err)
	}
	removed, err = CleanupNoteHistory(0, 1, 0)
	if err != nil || removed != 2 {
		t.Fatalf("age cleanup removed=%d err=%v want 2", removed, err)
	}
}

func TestPermanentDeleteRemovesHistory(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "history_purge")
	note, err := CreateNote(uid, "彻底删除", "<p>x</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser); err != nil {
		t.Fatal(err)
	}
	if err := DeleteNote(note.ID, uid); err != nil {
		t.Fatal(err)
	}
	if err := PermanentDeleteNote(note.ID, uid); err != nil {
		t.Fatal(err)
	}
	if got := historyCount(t, note.ID); got != 0 {
		t.Fatalf("history should be removed with note, got %d", got)
	}
}

func TestGetHistoryConfigDefaultsAndOverride(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	maxPerNote, days, totalBytes := GetHistoryConfig()
	if maxPerNote != DefaultHistoryMaxPerNote || days != DefaultHistoryRetentionDays || totalBytes != int64(DefaultHistoryMaxTotalMB)*1024*1024 {
		t.Fatalf("defaults = %d/%d/%d", maxPerNote, days, totalBytes)
	}
	for key, value := range map[string]string{
		"history_max_per_note":   "10",
		"history_retention_days": "30",
		"history_max_total_mb":   "128",
	} {
		if err := SetConfig(key, value); err != nil {
			t.Fatal(err)
		}
	}
	maxPerNote, days, totalBytes = GetHistoryConfig()
	if maxPerNote != 10 || days != 30 || totalBytes != 128*1024*1024 {
		t.Fatalf("override = %d/%d/%d", maxPerNote, days, totalBytes)
	}
	if err := SetConfig("history_max_per_note", "0"); err != nil {
		t.Fatal(err)
	}
	if maxPerNote, _, _ = GetHistoryConfig(); maxPerNote != 0 {
		t.Fatalf("zero should be preserved, got %d", maxPerNote)
	}
}

func TestGetHistoryConfigInvalidFallback(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	for key, value := range map[string]string{
		"history_max_per_note":   "abc",
		"history_retention_days": "0",
		"history_max_total_mb":   "-1",
	} {
		if err := SetConfig(key, value); err != nil {
			t.Fatal(err)
		}
	}
	maxPerNote, days, totalBytes := GetHistoryConfig()
	if maxPerNote != DefaultHistoryMaxPerNote || days != DefaultHistoryRetentionDays || totalBytes != int64(DefaultHistoryMaxTotalMB)*1024*1024 {
		t.Fatalf("invalid values should fall back to defaults, got %d/%d/%d", maxPerNote, days, totalBytes)
	}
}

func TestCaptureNoteHistoryDisabledWhenZero(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "history_zero")
	note, err := CreateNote(uid, "关闭历史", "<p>v1</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetConfig("history_max_per_note", "0"); err != nil {
		t.Fatal(err)
	}
	ok, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser)
	if err != nil || ok {
		t.Fatalf("capture with max=0: ok=%v err=%v", ok, err)
	}
	if got := historyCount(t, note.ID); got != 0 {
		t.Fatalf("history count=%d want 0", got)
	}
}

func TestCaptureNoteHistoryPrunesPerNote(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "history_prune")
	note, err := CreateNote(uid, "按篇裁剪", "<p>v1</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetConfig("history_max_per_note", "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser); err != nil {
		t.Fatal(err)
	}
	for version, content := range []string{"<p>v2</p>", "<p>v3</p>"} {
		if err := UpdateNote(note.ID, uid, "按篇裁剪", content, "", int64(version+1)); err != nil {
			t.Fatalf("update v%d: %v", version+2, err)
		}
		if _, err := CaptureNoteHistory(note.ID, uid, HistorySourceUser); err != nil {
			t.Fatal(err)
		}
	}
	if got := historyCount(t, note.ID); got != 2 {
		t.Fatalf("history count=%d want 2", got)
	}
	rows, err := database.DB.Query("SELECT content FROM note_history WHERE note_id = ? ORDER BY id", note.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatal(err)
		}
		got = append(got, content)
	}
	if len(got) != 2 || got[0] != "<p>v2</p>" || got[1] != "<p>v3</p>" {
		t.Fatalf("remaining history = %v, want [v2 v3]", got)
	}
}
