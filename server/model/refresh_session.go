package model

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"stickynote/database"
)

// RefreshSession 多设备 refresh 会话（v21.94 P3-7 多设备一次性轮转）。
//
// 设计：一次登录 = 一个 family；Refresh 轮转在 family 内延续（旧 jti 标记
// revoked，同 family 签发新 jti）；重放（已吊销的 jti 再次出现）撤销整个
// family；登出/改密/重置批量撤销该用户全部活跃会话。
// 时间字段用 unix 秒（INTEGER）存储，避免 SQLite 时间格式与 Go time 序列化差异。
type RefreshSession struct {
	ID         int64
	UserID     int64
	FamilyID   string
	JTI        string
	DeviceName string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// ErrSessionNotFound 会话不存在（jti 未知或已被清理）。
var ErrSessionNotFound = errors.New("refresh session not found")

const refreshSessionSelect = "SELECT id, user_id, family_id, jti, device_name, created_at, expires_at, revoked_at, last_used_at FROM refresh_sessions"

// scanRefreshSession 从 rows 扫描一行。
func scanRefreshSession(row interface{ Scan(...any) error }) (*RefreshSession, error) {
	var (
		s        RefreshSession
		created  string
		expires  int64
		revoked  sql.NullInt64
		lastUsed sql.NullInt64
	)
	if err := row.Scan(&s.ID, &s.UserID, &s.FamilyID, &s.JTI, &s.DeviceName,
		&created, &expires, &revoked, &lastUsed); err != nil {
		return nil, err
	}
	s.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	s.ExpiresAt = time.Unix(expires, 0)
	if revoked.Valid {
		t := time.Unix(revoked.Int64, 0)
		s.RevokedAt = &t
	}
	if lastUsed.Valid {
		t := time.Unix(lastUsed.Int64, 0)
		s.LastUsedAt = &t
	}
	return &s, nil
}

// CreateRefreshSession 登录/换票时插入一条会话。
// expiresAt 用 unix 秒落库。
func CreateRefreshSession(userID int64, familyID, jti, deviceName string, expiresAt time.Time) error {
	if familyID == "" {
		familyID = jti // 兼容：未显式给 family 时以 jti 兜底
	}
	_, err := database.DB.Exec(
		"INSERT INTO refresh_sessions (user_id, family_id, jti, device_name, expires_at) VALUES (?, ?, ?, ?, ?)",
		userID, familyID, jti, deviceName, expiresAt.Unix())
	return err
}

// FindRefreshSessionByJTI 按 jti 精确查找（Refresh 主路径）。
func FindRefreshSessionByJTI(jti string) (*RefreshSession, error) {
	row := database.DB.QueryRow(refreshSessionSelect+" WHERE jti = ?", jti)
	s, err := scanRefreshSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find refresh session: %w", err)
	}
	return s, nil
}

// RevokeRefreshSession 吊销单条会话（轮转时标记旧 jti）。
func RevokeRefreshSession(jti string) error {
	_, err := database.DB.Exec(
		"UPDATE refresh_sessions SET revoked_at = ? WHERE jti = ? AND revoked_at IS NULL",
		time.Now().Unix(), jti)
	return err
}

// RevokeRefreshFamily 吊销整个 family（重放检测时撤销该设备全部会话）。
func RevokeRefreshFamily(familyID string) error {
	_, err := database.DB.Exec(
		"UPDATE refresh_sessions SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL",
		time.Now().Unix(), familyID)
	return err
}

// HasActiveRefreshSession 查询 family 是否仍有活跃（未吊销、未过期）会话。
//
// v21.217（生产实证·弱网桌面端反复掉登录）：refresh rotation 的**重放宽限期**
// 判定依赖它——已吊销 jti 在宽限期内重现时，只有 family 仍活跃（= 之前那次
// 轮转成功签发了新票，只是响应没到达客户端）才允许续命；全族已撤（真重放
// 攻击或人工登出）必须维持 401。与 Okta/Auth0 的 rotation+grace 同款语义。
func HasActiveRefreshSession(familyID string) (bool, error) {
	var n int
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM refresh_sessions WHERE family_id = ? AND revoked_at IS NULL AND expires_at > ?",
		familyID, time.Now().Unix()).Scan(&n)
	return n > 0, err
}

// RevokeAllUserRefreshSessions 吊销该用户全部活跃会话（登出/改密/重置/禁用）。
func RevokeAllUserRefreshSessions(userID int64) error {
	_, err := database.DB.Exec(
		"UPDATE refresh_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL",
		time.Now().Unix(), userID)
	return err
}

// TouchRefreshSession 更新 last_used_at（Refresh 成功后）。
func TouchRefreshSession(jti string) error {
	_, err := database.DB.Exec(
		"UPDATE refresh_sessions SET last_used_at = ? WHERE jti = ?", time.Now().Unix(), jti)
	return err
}

// RotateRefreshSessionTx 原子轮转 refresh 会话（R-1 修复，v1.0.134 审计缺陷）。
//
// 背景：修复前 handler 用 5 次**独立** DB 调用完成轮转（吊销旧票 / 插新会话 /
// touch 旧票 / 更新 users.refresh_jti），**既无事务包裹**，又把吊销的错误用 `_ =`
// 丢弃 → 并发持同一旧 token 可双花，且旧票吊销失败会被静默忽略。
//
// 语义（一次性 token）：本函数在一个事务内完成四步，任一写失败即整体回滚，
// 绝不留"旧票仍可用、新票已签发"的中间态（fail-closed：宁可本次换票失败）。
//
//	① CAS 吊销旧 jti：UPDATE ... WHERE jti = ? AND revoked_at IS NULL
//	② 同 family 插入新 jti 会话
//	③ 更新旧 jti 的 last_used_at（审计可追溯）
//	④ 更新 users.refresh_jti 为新 jti（存量单 jti 兼容登记）
//
// 返回值：
//
//	rotated=true  → 本次成功吊销旧票，新会话已签发（正常轮转）
//	rotated=false → CAS 未命中：旧 jti **已被吊销**（重放或并发抢用）。
//	                本事务回滚、不签发新会话；调用方应撤销整个 family 并返 401。
//
// 事务说明：事务内**第一条语句就是写**（CAS UPDATE），而非先读后写，
// 以避免 WAL 下"延迟事务先读后写"升级写锁时 SQLITE_BUSY_SNAPSHOT 不可重试的问题；
// 写锁竞争由 DSN 的 busy_timeout(5000) 兜底（见 database.Init）。
func RotateRefreshSessionTx(userID int64, familyID, oldJTI, newJTI, deviceName string, expiresAt time.Time) (rotated bool, err error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return false, fmt.Errorf("begin rotate tx: %w", err)
	}
	// 任何失败路径（含 CAS 未命中）都回滚，保证"要么全生效、要么全不生效"
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().Unix()

	// ① CAS 吊销旧 jti：只有"旧票仍存活"的调用方能命中，命中即独占轮转权
	res, err := tx.Exec(
		"UPDATE refresh_sessions SET revoked_at = ? WHERE jti = ? AND revoked_at IS NULL",
		now, oldJTI)
	if err != nil {
		return false, fmt.Errorf("revoke old session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("revoke old session rows: %w", err)
	}
	if n == 0 {
		// 旧票已被吊销 → 重放或并发抢用：回滚，不签发新会话
		return false, nil
	}

	// ② 同 family 签发新会话
	if familyID == "" {
		familyID = newJTI // 与 CreateRefreshSession 一致的兜底
	}
	if _, err := tx.Exec(
		"INSERT INTO refresh_sessions (user_id, family_id, jti, device_name, expires_at) VALUES (?, ?, ?, ?, ?)",
		userID, familyID, newJTI, deviceName, expiresAt.Unix()); err != nil {
		return false, fmt.Errorf("create rotated session: %w", err)
	}

	// ③ 旧会话 last_used_at
	if _, err := tx.Exec(
		"UPDATE refresh_sessions SET last_used_at = ? WHERE jti = ?", now, oldJTI); err != nil {
		return false, fmt.Errorf("touch old session: %w", err)
	}

	// ④ 单 jti 兼容登记（等价于 model.UpdateRefreshJTI，但必须在同一事务内）
	if _, err := tx.Exec(
		"UPDATE users SET refresh_jti = ? WHERE id = ?", newJTI, userID); err != nil {
		return false, fmt.Errorf("update user refresh jti: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit rotate tx: %w", err)
	}
	committed = true
	return true, nil
}

// CleanupExpiredRefreshSessions 清理所有已过期的会话（防止表无限膨胀）。
//
// v21.97（审计 P-03 整改）：删除条件由「过期且已吊销」放宽为「仅过期」——
// 过期票在 Refresh 处理入口即被拒绝（handler 校验 expires_at，401），
// 保留过期未吊销行无任何业务价值，反而占表（用户装后长期不登录的场景）。
// 活跃会话（未过期）必须保留，轮转仍可能引用。
func CleanupExpiredRefreshSessions() (int64, error) {
	res, err := database.DB.Exec(
		"DELETE FROM refresh_sessions WHERE expires_at < ?",
		time.Now().Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
