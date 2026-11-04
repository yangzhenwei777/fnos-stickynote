package model

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"stickynote/database"
)

var (
	ErrShareNotFound  = errors.New("share not found")
	ErrShareEncrypted = errors.New("encrypted note cannot be shared")
	ErrShareDisabled  = errors.New("share link disabled")
)

const (
	DefaultShareDays = 7
	MaxShareDays     = 30
)

type NoteShare struct {
	ID           int64      `json:"id"`
	NoteID       int64      `json:"note_id"`
	ExpiresAt    time.Time  `json:"expires_at"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	LastAccessAt *time.Time `json:"last_access_at,omitempty"`
	AccessCount  int64      `json:"access_count"`
}

type SharedNote struct {
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Tags      string    `json:"tags,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func shareTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func ShareLinksEnabled() bool {
	v, err := GetConfig("share_enabled")
	if err != nil {
		return true
	}
	return v != "false"
}

func CreateNoteShare(userID, noteID int64, days int) (*NoteShare, string, error) {
	if !ShareLinksEnabled() {
		return nil, "", ErrShareDisabled
	}
	if days < 1 || days > MaxShareDays {
		days = DefaultShareDays
	}
	var encrypted int
	err := database.DB.QueryRow(
		"SELECT is_encrypted FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		noteID, userID,
	).Scan(&encrypted)
	if err != nil {
		return nil, "", err
	}
	if encrypted == 1 {
		return nil, "", ErrShareEncrypted
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
	res, err := database.DB.Exec(
		"INSERT INTO note_shares (note_id, user_id, token_hash, expires_at) VALUES (?, ?, ?, ?)",
		noteID, userID, shareTokenHash(token), expires.Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	return &NoteShare{ID: id, NoteID: noteID, ExpiresAt: expires, CreatedAt: time.Now()}, token, nil
}

func ListNoteShares(userID, noteID int64) ([]NoteShare, error) {
	rows, err := database.DB.Query(
		"SELECT id, note_id, expires_at, created_at, revoked_at, last_access_at, access_count "+
			"FROM note_shares WHERE user_id = ? AND note_id = ? ORDER BY created_at DESC, id DESC",
		userID, noteID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []NoteShare{}
	for rows.Next() {
		var (
			s        NoteShare
			revoked  sql.NullTime
			lastSeen sql.NullTime
		)
		if err := rows.Scan(&s.ID, &s.NoteID, &s.ExpiresAt, &s.CreatedAt, &revoked, &lastSeen, &s.AccessCount); err != nil {
			return nil, err
		}
		if revoked.Valid {
			t := revoked.Time
			s.RevokedAt = &t
		}
		if lastSeen.Valid {
			t := lastSeen.Time
			s.LastAccessAt = &t
		}
		items = append(items, s)
	}
	return items, nil
}

func RevokeNoteShare(userID, noteID, shareID int64) error {
	res, err := database.DB.Exec(
		"UPDATE note_shares SET revoked_at = CURRENT_TIMESTAMP WHERE id = ? AND note_id = ? AND user_id = ? AND revoked_at IS NULL",
		shareID, noteID, userID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func ResolveShareToken(token string) (*SharedNote, error) {
	if !ShareLinksEnabled() {
		return nil, ErrShareNotFound
	}
	if len(token) < 20 || len(token) > 100 {
		return nil, ErrShareNotFound
	}
	var (
		note    SharedNote
		shareID int64
	)
	err := database.DB.QueryRow(
		"SELECT s.id, n.title, n.content, n.tags, n.updated_at "+
			"FROM note_shares s JOIN notes n ON n.id = s.note_id "+
			"WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > CURRENT_TIMESTAMP "+
			"AND n.deleted_at IS NULL AND n.is_encrypted = 0",
		shareTokenHash(token),
	).Scan(&shareID, &note.Title, &note.Content, &note.Tags, &note.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrShareNotFound
	}
	if err != nil {
		return nil, err
	}
	_, _ = database.DB.Exec(
		"UPDATE note_shares SET last_access_at = CURRENT_TIMESTAMP, access_count = access_count + 1 WHERE id = ?",
		shareID,
	)
	return &note, nil
}
