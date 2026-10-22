package model

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"stickynote/database"
)

// 批次 5 · P5：Bootstrap 快照账本（sync_snapshots / sync_snapshots_hold）。
// 生命周期：PENDING →(原子领取)→ BUILDING → READY | FAILED；READY 到期 → EXPIRED；
// 服务端恢复换代 → 全部未失效快照 INVALIDATED（刀 2 挂 commitReady）。
// 时间列统一 Go 侧 RFC3339 文本（与 backup_snapshots.created_at 同口径——扫描须按 string 读）。

const (
	SnapshotPending     = "PENDING"
	SnapshotBuilding    = "BUILDING"
	SnapshotReady       = "READY"
	SnapshotFailed      = "FAILED"
	SnapshotExpired     = "EXPIRED"
	SnapshotInvalidated = "INVALIDATED"
)

// SnapshotActiveStates 占用并发帽/保护 Hold 的活跃态集合（SQL IN 用）。
const snapshotActiveStates = "'PENDING','BUILDING','READY'"

type SyncSnapshot struct {
	ID          string
	UserID      int64
	Status      string
	ServerEpoch int64
	Watermark   int64
	InstanceID  string
	Sha256      string
	FilePath    string
	SizeBytes   int64
	FileCount   int
	Error       string
	CreatedAt   time.Time
	BuiltAt     time.Time
	ExpiresAt   time.Time
}

func NewSnapshotID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "snap-" + hex.EncodeToString(b)
}

func CreateSyncSnapshot(s *SyncSnapshot) error {
	db := database.GetDB()
	if db == nil {
		return errors.New("database not ready")
	}
	_, err := db.Exec(`INSERT INTO sync_snapshots (id, user_id, status, expires_at) VALUES (?, ?, ?, ?)`,
		s.ID, s.UserID, SnapshotPending, s.ExpiresAt.UTC().Format(time.RFC3339))
	return err
}

func GetSyncSnapshot(id string) (*SyncSnapshot, error) {
	db := database.GetDB()
	if db == nil {
		return nil, errors.New("database not ready")
	}
	var s SyncSnapshot
	var builtAt, expiresAt sql.NullString
	err := db.QueryRow(`SELECT id, user_id, status, server_epoch, watermark, instance_id, sha256,
		file_path, size_bytes, file_count, error, created_at, built_at, expires_at
		FROM sync_snapshots WHERE id = ?`, id).
		Scan(&s.ID, &s.UserID, &s.Status, &s.ServerEpoch, &s.Watermark, &s.InstanceID, &s.Sha256,
			&s.FilePath, &s.SizeBytes, &s.FileCount, &s.Error, &s.CreatedAt, &builtAt, &expiresAt)
	if err != nil {
		return nil, err
	}
	s.BuiltAt = parseSnapshotTS(builtAt)
	s.ExpiresAt = parseSnapshotTS(expiresAt)
	return &s, nil
}

func parseSnapshotTS(v sql.NullString) time.Time {
	if !v.Valid || v.String == "" {
		return time.Time{}
	}
	for _, layout := range rangeSnapshotLayouts() {
		if t, err := time.Parse(layout, v.String); err == nil {
			return t
		}
	}
	return time.Time{}
}

func rangeSnapshotLayouts() []string {
	return []string{time.RFC3339, "2006-01-02 15:04:05", time.RFC3339Nano}
}

// CountUserActiveSnapshots §47 per-user 帽：该用户活跃态（PENDING/BUILDING/READY）快照数。
// 帽语义在调用侧解释（生成中 ≤1：PENDING+BUILDING；READY 不占生成帽）。
func CountUserActiveSnapshots(userID int64, statuses []string) (int, error) {
	db := database.GetDB()
	if db == nil {
		return 0, errors.New("database not ready")
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
	args := make([]any, len(statuses))
	for i, s := range statuses {
		args[i] = s
	}
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sync_snapshots WHERE user_id = ? AND status IN (`+ph+`)`,
		append([]any{userID}, args...)...).Scan(&n)
	return n, err
}

// CountBuildingSnapshots 全局 BUILDING 数（生成并发帽 ≤2 用）。
func CountBuildingSnapshots() (int, error) {
	db := database.GetDB()
	if db == nil {
		return 0, errors.New("database not ready")
	}
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sync_snapshots WHERE status IN ('PENDING','BUILDING')`).Scan(&n)
	return n, err
}

// ClaimSnapshotForBuild 原子领取：PENDING → BUILDING（RowsAffected=1 才归本 worker，§47 双保险）。
func ClaimSnapshotForBuild(id string) (bool, error) {
	db := database.GetDB()
	if db == nil {
		return false, errors.New("database not ready")
	}
	res, err := db.Exec(`UPDATE sync_snapshots SET status = 'BUILDING' WHERE id = ? AND status = 'PENDING'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// FinishSnapshotBuild 终态落账：READY 必须带 sha256/file/epoch/watermark；FAILED 带 error。
// 仅允许从 BUILDING 推进（防双写者竞态覆盖 READY）。
func FinishSnapshotBuild(id, status string, epoch, watermark int64, instanceID, sha, filePath string, sizeBytes int64, fileCount int, errMsg string) error {
	if status != SnapshotReady && status != SnapshotFailed {
		return fmt.Errorf("illegal build terminal status %q", status)
	}
	db := database.GetDB()
	if db == nil {
		return errors.New("database not ready")
	}
	_, err := db.Exec(`UPDATE sync_snapshots SET status=?, server_epoch=?, watermark=?, instance_id=?, sha256=?,
		file_path=?, size_bytes=?, file_count=?, error=?, built_at=?
		WHERE id=? AND status='BUILDING'`,
		status, epoch, watermark, instanceID, sha, filePath, sizeBytes, fileCount, errMsg,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// ListExpiredReadySnapshots TTL 扫描对象：READY 且已过期。
func ListExpiredReadySnapshots(now time.Time) ([]*SyncSnapshot, error) {
	db := database.GetDB()
	if db == nil {
		return nil, errors.New("database not ready")
	}
	rows, err := db.Query(`SELECT id, user_id, status, server_epoch, watermark, instance_id, sha256,
		file_path, size_bytes, file_count, error, created_at, built_at, expires_at
		FROM sync_snapshots WHERE status = 'READY' AND expires_at != '' AND expires_at <= ?`,
		now.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SyncSnapshot
	for rows.Next() {
		var s SyncSnapshot
		var builtAt, expiresAt sql.NullString
		if err := rows.Scan(&s.ID, &s.UserID, &s.Status, &s.ServerEpoch, &s.Watermark, &s.InstanceID, &s.Sha256,
			&s.FilePath, &s.SizeBytes, &s.FileCount, &s.Error, &s.CreatedAt, &builtAt, &expiresAt); err != nil {
			return nil, err
		}
		s.BuiltAt = parseSnapshotTS(builtAt)
		s.ExpiresAt = parseSnapshotTS(expiresAt)
		out = append(out, &s)
	}
	return out, rows.Err()
}

// ExpireSnapshot READY→EXPIRED 原子推进（RowsAffected=1 才由本扫描者删文件/释放 Hold，防竞态双删）。
func ExpireSnapshot(id string) (bool, error) {
	db := database.GetDB()
	if db == nil {
		return false, errors.New("database not ready")
	}
	res, err := db.Exec(`UPDATE sync_snapshots SET status = 'EXPIRED' WHERE id = ? AND status = 'READY'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SnapshotFileRef 作废联动返回的清理引用（id 释放 Hold + path 删文件）。
type SnapshotFileRef struct {
	ID       string
	FilePath string
}

// InvalidateAllActiveSnapshots 恢复换代联动（restore OnEpochCommitted 钩子）：
// 活跃快照全部 INVALIDATED 并返回引用（调用侧删文件 + ReleaseHolds）。
func InvalidateAllActiveSnapshots() ([]SnapshotFileRef, error) {
	db := database.GetDB()
	if db == nil {
		return nil, errors.New("database not ready")
	}
	rows, err := db.Query(`SELECT id, file_path FROM sync_snapshots WHERE status IN (` + snapshotActiveStates + `)`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var refs []SnapshotFileRef
	for rows.Next() {
		var r SnapshotFileRef
		if rows.Scan(&r.ID, &r.FilePath) == nil {
			refs = append(refs, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",")
	args := make([]any, len(refs))
	for i, r := range refs {
		args[i] = r.ID
	}
	if _, err := db.Exec(`UPDATE sync_snapshots SET status = 'INVALIDATED' WHERE id IN (`+ph+`)`, args...); err != nil {
		return nil, err
	}
	return refs, nil
}

// MarkSnapshotMissingFile READY 但文件已失（异常态）：置 FAILED 防继续分发幽灵快照。
func MarkSnapshotMissingFile(id string) error {
	db := database.GetDB()
	if db == nil {
		return errors.New("database not ready")
	}
	_, err := db.Exec(`UPDATE sync_snapshots SET status='FAILED', error='file missing' WHERE id=? AND status='READY'`, id)
	return err
}

// ---- Hold（§73）----

// AddSnapshotHolds 批量登记附件 Hold（幂等 INSERT OR IGNORE）。
func AddSnapshotHolds(snapshotID string, filenames []string) error {
	if len(filenames) == 0 {
		return nil
	}
	db := database.GetDB()
	if db == nil {
		return errors.New("database not ready")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, f := range filenames {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO sync_snapshots_hold (snapshot_id, filename) VALUES (?, ?)`, snapshotID, f); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ReleaseSnapshotHolds 释放 Hold（过期/作废/构建失败）。
func ReleaseSnapshotHolds(snapshotID string) error {
	db := database.GetDB()
	if db == nil {
		return errors.New("database not ready")
	}
	_, err := db.Exec(`DELETE FROM sync_snapshots_hold WHERE snapshot_id = ?`, snapshotID)
	return err
}

// AllSyncSnapshotRows 孤儿文件扫描对账用（全行 id→file_path）。
func AllSyncSnapshotRows() (map[string]string, error) {
	db := database.GetDB()
	if db == nil {
		return nil, errors.New("database not ready")
	}
	rows, err := db.Query(`SELECT id, file_path FROM sync_snapshots`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, fp string
		if rows.Scan(&id, &fp) == nil {
			out[id] = fp
		}
	}
	return out, rows.Err()
}

// ListActiveSnapshots v21.194 S-1 加固：全部在途（PENDING/BUILDING）快照行——
// 僵死抢占按 created_at 在 Go 侧过滤（规避 created_at 历史格式差异，行数量级极小）。
func ListActiveSnapshots() ([]*SyncSnapshot, error) {
	db := database.GetDB()
	if db == nil {
		return nil, errors.New("database not ready")
	}
	rows, err := db.Query(`SELECT id, user_id, status, server_epoch, watermark, instance_id, sha256,
		file_path, size_bytes, file_count, error, created_at, built_at, expires_at
		FROM sync_snapshots WHERE status IN ('PENDING','BUILDING')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SyncSnapshot
	for rows.Next() {
		var s SyncSnapshot
		var builtAt, expiresAt sql.NullString
		if err := rows.Scan(&s.ID, &s.UserID, &s.Status, &s.ServerEpoch, &s.Watermark, &s.InstanceID, &s.Sha256,
			&s.FilePath, &s.SizeBytes, &s.FileCount, &s.Error, &s.CreatedAt, &builtAt, &expiresAt); err != nil {
			return nil, err
		}
		s.BuiltAt = parseSnapshotTS(builtAt)
		s.ExpiresAt = parseSnapshotTS(expiresAt)
		out = append(out, &s)
	}
	return out, rows.Err()
}

// FailSnapshotIfActive 终态 CAS（PENDING/BUILDING → FAILED）：看门狗与僵死抢占专用——
// 构建后续若"复活"完成，FinishSnapshotBuild 的 WHERE status='BUILDING' 保证 0 行受影响，
// 双向幂等（2026-10-08 S-1 取证：fnOS 卷上构建可无限期卡死，必须允许"超时判死"）。
func FailSnapshotIfActive(id, errMsg string) (bool, error) {
	db := database.GetDB()
	if db == nil {
		return false, errors.New("database not ready")
	}
	res, err := db.Exec(`UPDATE sync_snapshots SET status='FAILED', error=?, built_at=?
		WHERE id=? AND status IN ('PENDING','BUILDING')`,
		errMsg, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
