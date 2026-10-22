package model

import (
	"database/sql"
	"errors"

	"stickynote/database"
)

// 加密态哨兵（P2 批次1 下沉到 model：Tx 变体在事务内判定，app 层别名转发，
// 入口层 errors.Is 判类，禁止匹配文案）。
var (
	ErrAlreadyEncrypted = errors.New("note already encrypted")
	ErrNotEncrypted     = errors.New("note not encrypted")
)

func SetEncrypted(noteID, userID int64, passwordHash, ciphertext string) error {
	_, err := database.DB.Exec(
		"UPDATE notes SET is_encrypted=1, encrypt_password_hash=?, encrypted_content=?, content='', content_text='' WHERE id=? AND user_id=?",
		passwordHash, ciphertext, noteID, userID,
	)
	return err
}

// ClearEncrypted 移除加密状态，恢复明文（v21.90 M-06：同 SetEncrypted 补 user_id）。
func ClearEncrypted(noteID, userID int64, content, contentText string) error {
	_, err := database.DB.Exec(
		"UPDATE notes SET is_encrypted=0, encrypt_password_hash='', encrypted_content='', content=?, content_text=? WHERE id=? AND user_id=?",
		content, contentText, noteID, userID,
	)
	return err
}

// UpdateEncryptedNote 加密笔记更新（单事务，v21.52 P1 审计修复）。
// 原实现 ClearEncrypted→UpdateNote(明文落库)→SetEncrypted 三步非原子，
// 中间崩溃/断电/锁冲突会留下"is_encrypted=0 + 明文 content"的持久化状态，
// 破坏"加密笔记不以明文持久化"的安全目标（gpt 审计 P1）。
// 本实现：明文只写入事务内存，落库即密文 + 加密标记，不存在明文中间态。
func UpdateEncryptedNote(id, userID int64, title, tags, passwordHash, ciphertext, plaintext string, version int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		"UPDATE notes SET title=?, content='', content_text='', tags=?, is_encrypted=1, encrypt_password_hash=?, encrypted_content=?, version=version+1, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND deleted_at IS NULL AND version=?",
		title, tags, passwordHash, ciphertext, id, userID, version,
	)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// v21.72 P2-08：区分版本冲突与笔记不存在
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).Scan(&exists); err == nil && exists > 0 {
			return ErrVersionConflict
		}
		return sql.ErrNoRows
	}

	// 同事务内同步标签关联表（v21.58 P2：复用 syncNoteTagsTx，错误全部检查）
	if err := syncNoteTagsTx(tx, id, tags); err != nil {
		return err
	}
	// v21.58 F-02：加密笔记以明文 HTML 提取附件建立关系（Content 落库为空，
	// 附件生命周期依赖 note_uploads 表，明文只在事务内存中、不落库）。
	if err := database.SyncNoteUploadsTx(tx, id, userID, plaintext); err != nil {
		return err
	}
	return tx.Commit()
}

// GetEncryptedContent 取加密笔记的密码哈希和密文（验证密码用）。
func GetEncryptedContent(noteID, userID int64) (passwordHash, ciphertext string, err error) {
	err = database.DB.QueryRow(
		"SELECT encrypt_password_hash, encrypted_content FROM notes WHERE id=? AND user_id=? AND is_encrypted=1 AND deleted_at IS NULL",
		noteID, userID,
	).Scan(&passwordHash, &ciphertext)
	return
}

// CountEncryptedNotes 统计用户的加密笔记数（v20.0，导出全部时跳过计数用）。
func CountEncryptedNotes(userID int64, out *int) {
	database.DB.QueryRow(
		"SELECT COUNT(*) FROM notes WHERE user_id=? AND deleted_at IS NULL AND is_encrypted=1",
		userID,
	).Scan(out)
}

// ListNotesByIDs 按 ID 列表查询属于指定用户的笔记（v20.7 批量导出用）。
