package model

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"stickynote/database"
)

// folder_lock.go —— v2.3.0 文件夹访问锁（非内容加密）。
//
// 语义：note_folders.is_encrypted=1 时，未持有本会话解锁授权的请求在所有读出口
// 都看不到该文件夹的笔记；文件夹密码只做 bcrypt 校验，不参与正文加密。
// 授权令牌由 handler 层签发/验签，模型层只消费「已解锁文件夹 id 集合」。

var (
	ErrFolderAlreadyEncrypted = errors.New("folder already encrypted")
	ErrFolderNotEncrypted     = errors.New("folder not encrypted")
	ErrFolderLocked           = errors.New("folder is locked")
)

// firstGranted 兼容 variadic 授权参数（老调用点无需改造）。
func firstGranted(granted []map[int64]struct{}) map[int64]struct{} {
	if len(granted) == 0 || granted[0] == nil {
		return map[int64]struct{}{}
	}
	return granted[0]
}

// FolderLockCondition 生成「排除锁定且未授权文件夹」的 SQL 片段。
// prefix 为列前缀（"notes." / "n." / ""），片段以 " AND (" 开头。
// 调用方必须按顺序追加 args；子查询内部参数为用户 ID 与授权文件夹 ID。
func FolderLockCondition(prefix string, userID int64, granted map[int64]struct{}) (string, []interface{}) {
	sub, args := lockedFolderSubquery(userID, granted)
	sql := fmt.Sprintf(" AND (%[1]sfolder_id IS NULL OR %[1]sfolder_id NOT IN %[2]s)", prefix, sub)
	return sql, args
}

// FolderLockedForNote 判断笔记是否属于「锁定且本会话未解锁」的文件夹。
func FolderLockedForNote(noteID, userID int64, granted map[int64]struct{}) (bool, error) {
	var fid sql.NullInt64
	var locked int
	err := database.DB.QueryRow(
		`SELECT n.folder_id, COALESCE(f.is_encrypted, 0)
		   FROM notes n
		   LEFT JOIN note_folders f ON f.id = n.folder_id AND f.user_id = n.user_id
		  WHERE n.id = ? AND n.user_id = ? AND n.deleted_at IS NULL`,
		noteID, userID,
	).Scan(&fid, &locked)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if locked == 0 || !fid.Valid {
		return false, nil
	}
	_, ok := granted[fid.Int64]
	return !ok, nil
}

// LockedNoteCount 统计因文件夹锁定而未导出的笔记数（导出全部响应头提示用）。
func LockedNoteCount(userID int64, granted map[int64]struct{}) (int, error) {
	sub, args := lockedFolderSubquery(userID, granted)
	sqlStr := "SELECT COUNT(*) FROM notes WHERE user_id = ? AND deleted_at IS NULL AND folder_id IN " + sub
	args = append([]interface{}{userID}, args...)
	var n int
	if err := database.DB.QueryRow(sqlStr, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// lockedFolderSubquery 返回「锁定且未授权」的文件夹 id 子查询与其参数。
func lockedFolderSubquery(userID int64, granted map[int64]struct{}) (string, []interface{}) {
	args := []interface{}{userID}
	ids := make([]int64, 0, len(granted))
	for id := range granted {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	sqlStr := "(SELECT id FROM note_folders WHERE user_id = ? AND is_encrypted = 1"
	if len(ids) > 0 {
		ph := make([]string, len(ids))
		for i, id := range ids {
			ph[i] = "?"
			args = append(args, id)
		}
		sqlStr += " AND id NOT IN (" + strings.Join(ph, ",") + ")"
	}
	sqlStr += ")"
	return sqlStr, args
}

// FolderFingerprint 锁定密码哈希指纹（令牌绑定，改密后旧令牌自动失效）。
func FolderFingerprint(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:])[:16]
}

// FolderGrantState 授权令牌校验所需的文件夹锁定快照。
type FolderGrantState struct {
	IsEncrypted bool
	Hash        string
}

// GetFolderGrantStates 批量读取文件夹锁定状态（校验令牌指纹用）。
func GetFolderGrantStates(userID int64, ids []int64) (map[int64]FolderGrantState, error) {
	out := make(map[int64]FolderGrantState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, userID)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	rows, err := database.DB.Query(
		"SELECT id, is_encrypted, encrypt_password_hash FROM note_folders WHERE user_id = ? AND id IN ("+strings.Join(ph, ",")+")",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var encrypted int
		var hash string
		if err := rows.Scan(&id, &encrypted, &hash); err != nil {
			return nil, err
		}
		out[id] = FolderGrantState{IsEncrypted: encrypted != 0, Hash: hash}
	}
	return out, rows.Err()
}

// GetFolderEncryptInfo 取文件夹锁定状态与密码哈希（校验归属）。
func GetFolderEncryptInfo(id, userID int64) (name string, isEncrypted bool, hash string, version int64, err error) {
	err = database.DB.QueryRow(
		"SELECT name, is_encrypted, encrypt_password_hash, version FROM note_folders WHERE id = ? AND user_id = ?",
		id, userID,
	).Scan(&name, &isEncrypted, &hash, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, "", 0, ErrFolderNotFound
	}
	return
}

// SetFolderEncryptedTx 事务内锁定文件夹：写 bcrypt 哈希。
// 不 bump version——服务端语义由 app.FolderService 统一补齐；桌面模式保持
// 「本地 version = 最后已知服务端版本」的 Outbox 基点口径（与 RenameFolderTx 一致）。
func SetFolderEncryptedTx(tx *sql.Tx, id, userID int64, hash string) error {
	var isEncrypted int
	err := tx.QueryRow("SELECT is_encrypted FROM note_folders WHERE id = ? AND user_id = ?", id, userID).
		Scan(&isEncrypted)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFolderNotFound
		}
		return err
	}
	if isEncrypted != 0 {
		return ErrFolderAlreadyEncrypted
	}
	_, err = tx.Exec(
		"UPDATE note_folders SET is_encrypted = 1, encrypt_password_hash = ?, encrypted_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?",
		hash, id, userID,
	)
	return err
}

// ClearFolderEncryptedTx 事务内解除锁定：清哈希。不 bump version（同 Set）。
func ClearFolderEncryptedTx(tx *sql.Tx, id, userID int64) error {
	var isEncrypted int
	err := tx.QueryRow("SELECT is_encrypted FROM note_folders WHERE id = ? AND user_id = ?", id, userID).
		Scan(&isEncrypted)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFolderNotFound
		}
		return err
	}
	if isEncrypted == 0 {
		return ErrFolderNotEncrypted
	}
	_, err = tx.Exec(
		"UPDATE note_folders SET is_encrypted = 0, encrypt_password_hash = '', encrypted_at = NULL WHERE id = ? AND user_id = ?",
		id, userID,
	)
	return err
}
