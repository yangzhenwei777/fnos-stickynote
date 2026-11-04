package model

import (
	"database/sql"
	"errors"
	"testing"

	"stickynote/database"
)

func TestCreateAndResolveNoteShare(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "share_user")
	note, err := CreateNote(uid, "分享笔记", "<p>公开内容</p>", "标签", 0)
	if err != nil {
		t.Fatal(err)
	}
	share, token, err := CreateNoteShare(uid, note.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || len(token) < 40 {
		t.Fatalf("token suspicious: %q", token)
	}
	var stored string
	if err := database.DB.QueryRow("SELECT token_hash FROM note_shares WHERE id = ?", share.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("raw token must not be stored")
	}
	got, err := ResolveShareToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "分享笔记" || got.Content != "<p>公开内容</p>" {
		t.Fatalf("resolved note mismatch: %+v", got)
	}
	items, err := ListNoteShares(uid, note.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("list shares len=%d err=%v", len(items), err)
	}
	if err := RevokeNoteShare(uid, note.ID, share.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveShareToken(token); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("revoked token should be not found, got %v", err)
	}
}

func TestShareExpiredAndBoundaries(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "share_bound")
	note, err := CreateNote(uid, "过期分享", "<p>x</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := CreateNoteShare(uid, note.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec("UPDATE note_shares SET expires_at = '2020-01-01 00:00:00' WHERE note_id = ?", note.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveShareToken(token); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("expired token should be not found, got %v", err)
	}

	enc, err := CreateNote(uid, "加密分享", "<p>secret</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetEncrypted(enc.ID, uid, "hash", "cipher"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateNoteShare(uid, enc.ID, 7); !errors.Is(err, ErrShareEncrypted) {
		t.Fatalf("encrypted note should be rejected, got %v", err)
	}
}

func TestShareDisabledAndCleanup(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "share_switch")
	note, err := CreateNote(uid, "开关分享", "<p>y</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := CreateNoteShare(uid, note.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetConfig("share_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateNoteShare(uid, note.ID, 7); !errors.Is(err, ErrShareDisabled) {
		t.Fatalf("disabled share should reject create, got %v", err)
	}
	if _, err := ResolveShareToken(token); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("disabled share should not resolve, got %v", err)
	}
	if err := SetConfig("share_enabled", "true"); err != nil {
		t.Fatal(err)
	}

	if err := DeleteNote(note.ID, uid); err != nil {
		t.Fatal(err)
	}
	if err := PermanentDeleteNote(note.ID, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM note_shares WHERE note_id = ?", note.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("shares should be removed with note, got %d", n)
	}
}

func TestRevokeShareNotFound(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "share_revoke404")
	note, err := CreateNote(uid, "撤销404", "<p>z</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := RevokeNoteShare(uid, note.ID, 999999); err != sql.ErrNoRows {
		t.Fatalf("revoke missing share should return ErrNoRows, got %v", err)
	}
}
