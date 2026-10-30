package model

import (
	"stickynote/database"
)

func ListAllNotesForExport(userID int64, granted ...map[int64]struct{}) ([]Note, error) {
	grant := firstGranted(granted)
	lockCond, lockArgs := FolderLockCondition("", userID, grant)
	args := append([]interface{}{userID}, lockArgs...)
	rows, err := database.DB.Query(
		"SELECT id, user_id, title, content, tags, is_encrypted, encrypt_password_hash, encrypted_content, created_at, updated_at FROM notes WHERE user_id = ? AND deleted_at IS NULL AND is_encrypted = 0"+lockCond+" ORDER BY created_at ASC",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Content, &n.Tags, &n.IsEncrypted, &n.EncryptPasswordHash, &n.EncryptedContent, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	if notes == nil {
		notes = []Note{}
	}
	return notes, nil
}

// ---- v20.0 笔记加密 ----

// MaskEncryptedNote 对加密笔记屏蔽 content，避免在前端列表/回收站泄漏明文摘要。
func MaskEncryptedNote(n *Note) {
	if n.IsEncrypted {
		n.Content = ""
	}
}

// SetEncrypted 标记笔记为加密状态：存储 bcrypt 哈希和 AES-GCM 密文，清空明文。
// v21.90 P1（M-06 审计核对）：WHERE 补 user_id——handler 虽已先做归属校验，
// 模型层原 WHERE id=? 无属主条件，未来调用方漏检即成越权加密他人笔记的纵深缺口
// （对照 UpdateEncryptedNote 已带 user_id）。
