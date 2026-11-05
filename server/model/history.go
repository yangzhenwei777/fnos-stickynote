package model

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"stickynote/database"
	"stickynote/logger"
)

const (
	HistorySourceUser = "user"
	HistorySourceAuto = "auto"

	DefaultHistoryMaxPerNote    = 50
	DefaultHistoryRetentionDays = 180
	DefaultHistoryMaxTotalMB    = 2048
)

type NoteHistory struct {
	ID        int64     `json:"id"`
	NoteID    int64     `json:"note_id"`
	Title     string    `json:"title"`
	Content   string    `json:"content,omitempty"`
	Tags      string    `json:"tags,omitempty"`
	Source    string    `json:"source"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type NoteHistoryListResponse struct {
	History   []NoteHistory `json:"history"`
	Total     int           `json:"total"`
	Encrypted bool          `json:"encrypted"`
}

func noteHistoryHash(title, content, tags string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + tags + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

// CaptureNoteHistoryTx 在事务内保存笔记当前状态的快照；与最近一条历史 hash 相同则跳过。
// 加密笔记不进入版本历史（v2.5.0 边界：避免密文恢复与口令变更的复杂性）。
func CaptureNoteHistoryTx(tx *sql.Tx, noteID, userID int64, source string) (bool, error) {
	var title, content, tags string
	var encrypted int
	err := tx.QueryRow(
		"SELECT title, content, tags, is_encrypted FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		noteID, userID,
	).Scan(&title, &content, &tags, &encrypted)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if encrypted == 1 {
		return false, nil
	}
	maxPerNote := historyMaxPerNoteTx(tx)
	if maxPerNote == 0 {
		return false, nil
	}
	hash := noteHistoryHash(title, content, tags)
	var lastHash string
	err = tx.QueryRow(
		"SELECT content_hash FROM note_history WHERE note_id = ? AND user_id = ? ORDER BY id DESC LIMIT 1",
		noteID, userID,
	).Scan(&lastHash)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil && lastHash == hash {
		return false, nil
	}
	if source != HistorySourceAuto {
		source = HistorySourceUser
	}
	size := int64(len(title) + len(content) + len(tags))
	if _, err := tx.Exec(
		"INSERT INTO note_history (note_id, user_id, title, content, tags, content_hash, size_bytes, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		noteID, userID, title, content, tags, hash, size, source,
	); err != nil {
		return false, err
	}
	// 每篇上限在保存时立即收敛：不像旧实现只等 24h 清理，避免高频编辑单篇
	// 在清理窗口内堆出大量历史。
	if err := pruneNoteHistoryTx(tx, noteID, userID, maxPerNote); err != nil {
		return false, err
	}
	return true, nil
}

// historyMaxPerNoteTx 在事务内读每篇上限；0 表示停止生成新历史。
func historyMaxPerNoteTx(tx *sql.Tx) int {
	var raw string
	if err := tx.QueryRow("SELECT value FROM configs WHERE key = 'history_max_per_note'").Scan(&raw); err != nil {
		return DefaultHistoryMaxPerNote
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < 0 || v > 1000 {
		return DefaultHistoryMaxPerNote
	}
	return v
}

func pruneNoteHistoryTx(tx *sql.Tx, noteID, userID int64, maxPerNote int) error {
	if maxPerNote <= 0 {
		return nil
	}
	_, err := tx.Exec(`DELETE FROM note_history WHERE id IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY note_id ORDER BY created_at DESC, id DESC) AS rn
			FROM note_history WHERE note_id = ? AND user_id = ?
		) WHERE rn > ?)`, noteID, userID, maxPerNote)
	return err
}

func CaptureNoteHistory(noteID, userID int64, source string) (bool, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	ok, err := CaptureNoteHistoryTx(tx, noteID, userID, source)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func ListNoteHistory(userID, noteID int64, page, pageSize int) (*NoteHistoryListResponse, error) {
	if page < 1 {
		page = 1
	} else if page > 100000 {
		page = 100000
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var encrypted int
	err := database.DB.QueryRow(
		"SELECT is_encrypted FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		noteID, userID,
	).Scan(&encrypted)
	if err != nil {
		return nil, err
	}
	if encrypted == 1 {
		return &NoteHistoryListResponse{History: []NoteHistory{}, Total: 0, Encrypted: true}, nil
	}

	var total int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM note_history WHERE note_id = ? AND user_id = ?",
		noteID, userID,
	).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := database.DB.Query(
		"SELECT id, note_id, title, tags, source, size_bytes, created_at FROM note_history WHERE note_id = ? AND user_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?",
		noteID, userID, pageSize, (page-1)*pageSize,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []NoteHistory{}
	for rows.Next() {
		var h NoteHistory
		if err := rows.Scan(&h.ID, &h.NoteID, &h.Title, &h.Tags, &h.Source, &h.SizeBytes, &h.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, h)
	}
	return &NoteHistoryListResponse{History: items, Total: total}, nil
}

func GetNoteHistory(userID, noteID, historyID int64) (*NoteHistory, error) {
	return getNoteHistory(nil, userID, noteID, historyID)
}

func GetNoteHistoryTx(tx *sql.Tx, userID, noteID, historyID int64) (*NoteHistory, error) {
	return getNoteHistory(tx, userID, noteID, historyID)
}

func getNoteHistory(tx *sql.Tx, userID, noteID, historyID int64) (*NoteHistory, error) {
	query := "SELECT h.id, h.note_id, h.title, h.content, h.tags, h.source, h.size_bytes, h.created_at " +
		"FROM note_history h JOIN notes n ON n.id = h.note_id " +
		"WHERE h.id = ? AND h.note_id = ? AND h.user_id = ? AND n.user_id = ? AND n.deleted_at IS NULL"
	var (
		h   NoteHistory
		row *sql.Row
	)
	if tx != nil {
		row = tx.QueryRow(query, historyID, noteID, userID, userID)
	} else {
		row = database.DB.QueryRow(query, historyID, noteID, userID, userID)
	}
	if err := row.Scan(&h.ID, &h.NoteID, &h.Title, &h.Content, &h.Tags, &h.Source, &h.SizeBytes, &h.CreatedAt); err != nil {
		return nil, err
	}
	return &h, nil
}

// SnapshotRecentNotes 为「有更新但尚未快照」的非加密笔记补一条自动快照（hash 去重）。
func SnapshotRecentNotes(limit int) (int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := database.DB.Query(`
		SELECT n.id, n.user_id FROM notes n
		LEFT JOIN (SELECT note_id, MAX(created_at) AS last_at FROM note_history GROUP BY note_id) h ON h.note_id = n.id
		WHERE n.deleted_at IS NULL AND n.is_encrypted = 0
		  AND (h.last_at IS NULL OR n.updated_at > h.last_at)
		ORDER BY n.updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ noteID, userID int64 }
	var list []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.noteID, &c.userID); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, c)
	}
	rows.Close()

	inserted := 0
	for _, c := range list {
		tx, err := database.DB.Begin()
		if err != nil {
			logger.Error("SnapshotRecentNotes begin note=%d: %v", c.noteID, err)
			continue
		}
		ok, err := CaptureNoteHistoryTx(tx, c.noteID, c.userID, HistorySourceAuto)
		if err != nil {
			_ = tx.Rollback()
			logger.Error("SnapshotRecentNotes capture note=%d: %v", c.noteID, err)
			continue
		}
		if !ok {
			_ = tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			logger.Error("SnapshotRecentNotes commit note=%d: %v", c.noteID, err)
			continue
		}
		inserted++
	}
	return inserted, nil
}

var (
	historyCleanupSignal = make(chan struct{}, 1)
	historyCleanupMu     sync.Mutex
)

// NotifyHistoryConfigChanged 非阻塞通知清理协程：三项历史配置保存后立即按新值收敛。
func NotifyHistoryConfigChanged() {
	select {
	case historyCleanupSignal <- struct{}{}:
	default:
	}
}

// HistoryCleanupSignal 供服务端清理协程监听配置变更。
func HistoryCleanupSignal() <-chan struct{} {
	return historyCleanupSignal
}

// CleanupNoteHistory 按份数/天数/总字节三重上限清理；任一步失败即回滚（Fail-Closed）。
// maxPerNote=0 表示不按份数裁剪（历史生成已由 CaptureNoteHistoryTx 跳过），
// 天数/总字节上限仍继续生效。
func CleanupNoteHistory(maxPerNote, maxAgeDays int, maxTotalBytes int64) (int, error) {
	historyCleanupMu.Lock()
	defer historyCleanupMu.Unlock()

	tx, err := database.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	deleted := int64(0)
	exec := func(query string, args ...interface{}) error {
		res, err := tx.Exec(query, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		deleted += n
		return nil
	}
	if maxPerNote > 0 {
		if err := exec(`DELETE FROM note_history WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY note_id ORDER BY created_at DESC, id DESC) AS rn
				FROM note_history
			) WHERE rn > ?)`, maxPerNote); err != nil {
			return 0, err
		}
	}
	if maxAgeDays > 0 {
		if err := exec("DELETE FROM note_history WHERE created_at < datetime('now', ?)", fmt.Sprintf("-%d days", maxAgeDays)); err != nil {
			return 0, err
		}
	}
	if maxTotalBytes > 0 {
		if err := exec(`DELETE FROM note_history WHERE id IN (
			SELECT id FROM (
				SELECT id, SUM(size_bytes) OVER (ORDER BY created_at DESC, id DESC) AS cum
				FROM note_history
			) WHERE cum > ?)`, maxTotalBytes); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(deleted), nil
}

// GetHistoryConfig 从 configs 表读取三项历史配置；缺失或非法时回退默认值。
// 环境变量只在新装/升级首次建键时作为初始值，运行时以系统配置页为准。
func GetHistoryConfig() (maxPerNote, maxAgeDays int, maxTotalBytes int64) {
	maxPerNote = configInt("history_max_per_note", DefaultHistoryMaxPerNote, 0, 1000)
	maxAgeDays = configInt("history_retention_days", DefaultHistoryRetentionDays, 1, 3650)
	maxTotalMB := configInt("history_max_total_mb", DefaultHistoryMaxTotalMB, 64, 102400)
	return maxPerNote, maxAgeDays, int64(maxTotalMB) * 1024 * 1024
}

// CleanupNoteHistoryWithConfig 按当前系统配置执行清理。
func CleanupNoteHistoryWithConfig() (int, error) {
	maxPerNote, maxAgeDays, maxTotalBytes := GetHistoryConfig()
	return CleanupNoteHistory(maxPerNote, maxAgeDays, maxTotalBytes)
}

func configInt(key string, def, min, max int) int {
	raw, err := GetConfig(key)
	if err != nil {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < min || v > max {
		return def
	}
	return v
}
