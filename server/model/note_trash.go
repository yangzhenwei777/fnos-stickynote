package model

import (
	"database/sql"
	"fmt"

	"stickynote/database"
	"stickynote/logger"
)

func ListTrashedNotes(userID int64, page, pageSize int) (*NoteListResponse, error) {
	// v21.66 P2：page 上限
	const maxPage = 100000
	if page < 1 {
		page = 1
	} else if page > maxPage {
		page = maxPage
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var total int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM notes WHERE user_id = ? AND deleted_at IS NOT NULL",
		userID,
	).Scan(&total); err != nil {
		return nil, err
	}

	rows, err := database.DB.Query(
		"SELECT id, user_id, title, content, tags, is_encrypted, encrypt_password_hash, encrypted_content, created_at, updated_at, deleted_at FROM notes WHERE user_id = ? AND deleted_at IS NOT NULL ORDER BY deleted_at DESC LIMIT ? OFFSET ?",
		userID, pageSize, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Content, &n.Tags, &n.IsEncrypted, &n.EncryptPasswordHash, &n.EncryptedContent, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt); err != nil {
			return nil, err
		}
		// v21.90 P2（审计 L-06）：回收站列表同样屏蔽加密笔记内容
		MaskEncryptedNote(&n)
		notes = append(notes, n)
	}
	if notes == nil {
		notes = []Note{}
	}
	return &NoteListResponse{Notes: notes, Total: total}, nil
}

// cacheTrashUploadBytes 从 note_uploads 关系表汇总附件大小，写入 notes.trash_upload_bytes 列。
// v21.66 P1-4：改用 note_uploads JOIN uploads 统计（加密笔记 content 为空也能正确统计），
// 且无条件 UPDATE（即使 0 也清零，修复「恢复后再删」的统计残留）。
// content 参数保留用于兼容调用签名（软删时传入，当前实现已不依赖 content 正则）。
func cacheTrashUploadBytes(noteID int64, content string) {
	_ = content // v21.66 P1-4：统计已迁移到 note_uploads，不再扫描 content
	var total int64
	if err := database.DB.QueryRow(
		"SELECT COALESCE(SUM(u.size), 0) FROM note_uploads nu JOIN uploads u ON u.filename = nu.filename WHERE nu.note_id = ?",
		noteID,
	).Scan(&total); err != nil {
		logger.Error("cacheTrashUploadBytes: query trash upload bytes: %v", err) // v21.58 F-16
		return
	}
	if _, err := database.DB.Exec("UPDATE notes SET trash_upload_bytes = ? WHERE id = ?", total, noteID); err != nil {
		logger.Error("cacheTrashUploadBytes: update trash_upload_bytes: %v", err)
	}
}

// GetTrashStats 回收站可视化（v18.8，优-10）：返回回收站条数与其中笔记所引用图片的占用字节数。
// 口径：uploads 表中有归属记录、且仍被回收站笔记 content 引用的文件 size 之和。
func GetTrashStats(userID int64) (count int, uploadBytes int64, err error) {
	// P3-14: count + SUM(trash_upload_bytes) 替代 O(N) 全量扫描正则匹配。
	if err = database.DB.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(trash_upload_bytes), 0) FROM notes WHERE user_id = ? AND deleted_at IS NOT NULL",
		userID,
	).Scan(&count, &uploadBytes); err != nil {
		return 0, 0, err
	}
	return count, uploadBytes, nil
}

// RestoreNote 实现已迁至 note_tx.go（P2 批次1：RestoreNoteTx + 自持事务包装）。

// PermanentDeleteNote 彻底删除（仅限回收站中的笔记，物理删除不可恢复）。
func PermanentDeleteNote(id, userID int64) error {
	res, err := database.DB.Exec(
		"DELETE FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NOT NULL",
		id, userID,
	)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// EmptyTrashNotes 清空当前用户回收站中的所有笔记（永久删除）。
// 返回被删除的笔记列表 + 删除前收集的附件文件名清单（供调用方做孤儿文件清理）。
func EmptyTrashNotes(userID int64) ([]Note, []string, error) {
	rows, err := database.DB.Query(
		"SELECT id FROM notes WHERE user_id = ? AND deleted_at IS NOT NULL",
		userID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID); err != nil {
			return notes, nil, err
		}
		notes = append(notes, n)
	}
	// v21.58 F-02：删除前收集附件清单（note_uploads 随 notes 级联删除，删后不可再查）
	// v21.69 P1-01：读取失败即返回错误停止删除（fail-closed），避免附件关系级联消失后文件残留
	var uploadNames []string
	for _, n := range notes {
		names, err := database.ListNoteUploads(n.ID)
		if err != nil {
			return notes, nil, fmt.Errorf("list note %d attachments: %w", n.ID, err)
		}
		uploadNames = append(uploadNames, names...)
	}
	// v21.95 B-1（审计 P2）：批量删除改显式事务 + 单条批量 DELETE，替代逐条独立事务。
	// 1) BEGIN IMMEDIATE（DSN _txlock=immediate）事务开始即持写锁，无锁升级；
	// 2) 单条 WHERE 语句原子删除全部（note_tags/note_uploads 外键级联），
	//    比逐条 DELETE 独立事务快一个数量级，总持锁时间大幅缩短；
	// 3) 全删或全不删，中途失败不留部分删除状态。
	tx, err := database.DB.Begin()
	if err != nil {
		return notes, uploadNames, fmt.Errorf("begin empty-trash tx: %w", err)
	}
	if _, err := tx.Exec(
		"DELETE FROM notes WHERE user_id = ? AND deleted_at IS NOT NULL",
		userID,
	); err != nil {
		_ = tx.Rollback()
		logger.Error("EmptyTrashNotes: batch delete failed: %v", err)
		return notes, uploadNames, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		logger.Error("EmptyTrashNotes: commit failed: %v", err)
		return notes, uploadNames, err
	}
	return notes, uploadNames, nil
}

// PurgeExpiredTrash / StartTrashPurger（回收站超期自动清理）已于 v18.4 移除：
// 回收站自 v18 起永久保留，该函数自始未被启用（调用点一直注释），
// 保留只会让人误以为存在自动清理链路。如需恢复此策略，从版本历史取回即可。
