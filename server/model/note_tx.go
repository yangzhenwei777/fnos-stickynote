package model

import (
	"database/sql"
	"fmt"
	"time"

	"stickynote/changelog"
	"stickynote/database"
	"stickynote/htmltext"
	"stickynote/logger"
)

// note_tx.go —— 写路径 *Tx 变体（P2 批次1，离线方案 v20 §16 Model Tx 规范）。
//
// 事务由调用方（app Facade / sync push）持有：实体更新 + ChangeLog（调用方经
// changelog 包写）同事务提交（§24/§42）。本文件各函数只做实体变更，不产事件。
//
// 版本纪律（SOURCE-FACT §2 + v20 §56~§60）：服务端 Web 写路径统一 version++——
// 但 version++ **不在本文件**：桌面 LocalService 复用同一套 model 操作本地库，桌面纪律是
// "本地 version 恒为最后已知服务端版本，本地编辑不自增"（synclocal/store.go §25 P0-1，
// Outbox base_version 口径）。因此 bump 由 app Facade 的 server 模式在同一事务内显式执行：
//   - DeleteNote/RestoreNote：§58/§59（SOURCE-FACT #3/#4 工单）；
//   - MoveNoteToFolder：§57（version++ 但 updated_at 不动；对 v21.87 "移动不动 version"
//     的定向推翻仅限服务端语义，桌面本地移动仍不动 version）。
// 本文件各函数只做实体变更与 sync_id 保证，不产事件、不做服务端 bump。

// ensureNoteSyncIDTx 兼容包装：委托 changelog.EnsureNoteSyncID（单一实现）。
func ensureNoteSyncIDTx(tx *sql.Tx, noteID int64) (string, error) {
	return changelog.EnsureNoteSyncID(tx, noteID)
}

// CreateNoteInFolderTx 事务内完成「文件夹校验 + 配额检查 + 插入（含 sync_id）+ 标签 + 附件关系」。
// 返回新笔记 ID（version=1 由 DB DEFAULT 保证，v21.75）。
func CreateNoteInFolderTx(tx *sql.Tx, userID int64, title, content, tags string, maxNotes int, folderID *int64) (int64, error) {
	if err := validateFolderTx(tx, folderID, userID); err != nil {
		return 0, err
	}

	if maxNotes > 0 {
		var count int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM notes WHERE user_id = ? AND deleted_at IS NULL", userID,
		).Scan(&count); err != nil {
			return 0, err
		}
		if count >= maxNotes {
			return 0, ErrQuotaExceeded
		}
	}

	result, err := tx.Exec(
		"INSERT INTO notes (user_id, title, content, content_text, tags, is_encrypted, encrypt_password_hash, encrypted_content, folder_id, sync_id) VALUES (?, ?, ?, ?, ?, 0, '', '', ?, ?)",
		userID, title, content, htmltext.ToText(content), tags, folderID, changelog.NewSyncID(),
	)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get inserted note id: %w", err)
	}
	if err := syncNoteTagsTx(tx, id, tags); err != nil {
		return 0, fmt.Errorf("sync note tags: %w", err)
	}
	if err := database.SyncNoteUploadsTx(tx, id, userID, content); err != nil {
		return 0, fmt.Errorf("sync note uploads: %w", err)
	}
	return id, nil
}

// UpdateNoteLocalTx 桌面本地写（E4-4，2026-09-23）：**不 bump version、不做 CAS**。
// 不变量（P0-1）：桌面本地 version 恒为"最后已知服务端版本"，推送 ACK 时才由
// applyResult 回写服务端权威版本；冲突判定交给同步推送时的服务端 CAS。
// 除 version 外的字段处理与 UpdateNoteTx 一致（content_text/tags/附件账本/updated_at）。
func UpdateNoteLocalTx(tx *sql.Tx, id, userID int64, title, content, tags string) error {
	res, err := tx.Exec(
		"UPDATE notes SET title = ?, content = ?, content_text = ?, tags = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		title, content, htmltext.ToText(content), tags, id, userID,
	)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	if err := syncNoteTagsTx(tx, id, tags); err != nil {
		return err
	}
	return database.SyncNoteUploadsTx(tx, id, userID, content)
}

// UpdateNoteTx CAS 更新（§47/ADR-002 基点语义与原 UpdateNote 一致）：
// 0 行命中时区分「版本冲突」与「笔记不存在」。返回更新后新版本。
func UpdateNoteTx(tx *sql.Tx, id, userID int64, title, content, tags string, version int64) (int64, error) {
	res, err := tx.Exec(
		"UPDATE notes SET title = ?, content = ?, content_text = ?, tags = ?, version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND version = ?",
		title, content, htmltext.ToText(content), tags, id, userID, version,
	)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).Scan(&exists); err == nil && exists > 0 {
			return 0, ErrVersionConflict
		}
		return 0, sql.ErrNoRows
	}
	if err := syncNoteTagsTx(tx, id, tags); err != nil {
		return 0, err
	}
	if err := database.SyncNoteUploadsTx(tx, id, userID, content); err != nil {
		return 0, err
	}
	return readNoteVersionTx(tx, id)
}

// DeleteNoteTx 软删入回收站（服务端 version++ 与 DELETE 墓碑事件由 app server 模式同事务补齐，
// §58）。附件大小缓存在同事务内完成（原实现在事务外单连，属批次1 收紧）。
func DeleteNoteTx(tx *sql.Tx, id, userID int64) error {
	cacheTrashUploadBytesTx(tx, id)
	if _, err := ensureNoteSyncIDTx(tx, id); err != nil {
		return err
	}
	res, err := tx.Exec(
		"UPDATE notes SET deleted_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
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

// RestoreNoteTx 从回收站恢复（服务端 version++ 与 RESTORE 事件由 app server 模式同事务补齐，§59）。
func RestoreNoteTx(tx *sql.Tx, id, userID int64) error {
	if _, err := ensureNoteSyncIDTx(tx, id); err != nil {
		return err
	}
	res, err := tx.Exec(
		"UPDATE notes SET deleted_at = NULL, trash_upload_bytes = 0 WHERE id = ? AND user_id = ? AND deleted_at IS NOT NULL",
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

// MoveNoteToFolderTx 移动到目标文件夹（§57 的 version++ 由 app server 模式同事务补齐；
// updated_at 不动的 v21.87 语义两侧保持）。
func MoveNoteToFolderTx(tx *sql.Tx, id, userID int64, folderID *int64) error {
	if err := validateFolderTx(tx, folderID, userID); err != nil {
		return err
	}
	if _, err := ensureNoteSyncIDTx(tx, id); err != nil {
		return err
	}
	res, err := tx.Exec(
		"UPDATE notes SET folder_id = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		folderID, id, userID,
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

// CopyNoteToFolderTx 复制笔记到目标文件夹（新副本带独立 sync_id + version=1）。
// 语义与原 CopyNoteToFolder 一致（密文直拷不解密 / 关系行复制 / 事务内配额），返回新笔记 ID。
func CopyNoteToFolderTx(tx *sql.Tx, id, userID int64, folderID *int64, maxNotes int) (int64, error) {
	if err := validateFolderTx(tx, folderID, userID); err != nil {
		return 0, err
	}
	var (
		srcTitle, srcContent, srcTags string
		srcIsEncrypted                bool
		srcPwdHash, srcCipher         string
	)
	err := tx.QueryRow(
		"SELECT title, content, tags, is_encrypted, encrypt_password_hash, encrypted_content FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		id, userID,
	).Scan(&srcTitle, &srcContent, &srcTags, &srcIsEncrypted, &srcPwdHash, &srcCipher)
	if err != nil {
		return 0, err // sql.ErrNoRows 由调用方映射 404
	}
	if maxNotes > 0 {
		var count int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM notes WHERE user_id = ? AND deleted_at IS NULL", userID,
		).Scan(&count); err != nil {
			return 0, err
		}
		if count >= maxNotes {
			return 0, ErrQuotaExceeded
		}
	}
	result, err := tx.Exec(
		"INSERT INTO notes (user_id, title, content, content_text, tags, is_encrypted, encrypt_password_hash, encrypted_content, folder_id, sync_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		userID, copyTitle(srcTitle), srcContent, htmltext.ToText(srcContent), srcTags, srcIsEncrypted, srcPwdHash, srcCipher, folderID, changelog.NewSyncID(),
	)
	if err != nil {
		return 0, err
	}
	newID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get inserted note id: %w", err)
	}
	if _, err := tx.Exec(
		"INSERT OR IGNORE INTO note_tags (note_id, tag) SELECT ?, tag FROM note_tags WHERE note_id = ?",
		newID, id,
	); err != nil {
		return 0, err
	}
	// 附件关系按源笔记行复制（同一批物理文件被两个笔记引用，状态保持 attached）。
	if _, err := tx.Exec(
		"INSERT OR IGNORE INTO note_uploads (note_id, filename) SELECT ?, filename FROM note_uploads WHERE note_id = ?",
		newID, id,
	); err != nil {
		return 0, err
	}
	return newID, nil
}

// readNoteVersionTx 事务内读回实体当前版本（事件 server_version 字段用）。
func readNoteVersionTx(tx *sql.Tx, id int64) (int64, error) {
	var v int64
	if err := tx.QueryRow("SELECT version FROM notes WHERE id = ?", id).Scan(&v); err != nil {
		return 0, fmt.Errorf("read note %d version: %w", id, err)
	}
	return v, nil
}

// cacheTrashUploadBytesTx cacheTrashUploadBytes 的事务内变体：统计结果随外层事务一起提交。
// 统计失败仅记日志不阻断（原语义 v21.58 F-16：尽力而为），但 UPDATE 失败同样不阻断——
// trash_upload_bytes 是派生统计列，主操作（软删）不可因此失败。
func cacheTrashUploadBytesTx(tx *sql.Tx, noteID int64) {
	var total int64
	if err := tx.QueryRow(
		"SELECT COALESCE(SUM(u.size), 0) FROM note_uploads nu JOIN uploads u ON u.filename = nu.filename WHERE nu.note_id = ?",
		noteID,
	).Scan(&total); err != nil {
		logger.Error("cacheTrashUploadBytesTx: query trash upload bytes: %v", err)
		return
	}
	if _, err := tx.Exec("UPDATE notes SET trash_upload_bytes = ? WHERE id = ?", total, noteID); err != nil {
		logger.Error("cacheTrashUploadBytesTx: update trash_upload_bytes: %v", err)
	}
}

// ---- 兼容包装：保留原非 Tx 入口（导入/CLI/既有测试调用），内部转 Tx 变体 ----

// CreateNoteInFolder v21.86：带文件夹归属的创建（自持事务）。
func CreateNoteInFolder(userID int64, title, content, tags string, maxNotes int, folderID *int64) (*Note, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := CreateNoteInFolderTx(tx, userID, title, content, tags, maxNotes, folderID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Note{
		ID:        id,
		UserID:    userID,
		Title:     title,
		Content:   content,
		Tags:      tags,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Version:   1, // v21.75：与 DB version 列 DEFAULT 1 对齐，否则前端拿 0 导致首次保存 409
		FolderID:  folderID,
	}, nil
}

// UpdateNote 自持事务 CAS 更新（P2 批次1 起推荐经 app Facade 调用以同步产事件）。
func UpdateNote(id, userID int64, title, content, tags string, version int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := UpdateNoteTx(tx, id, userID, title, content, tags, version); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteNote 自持事务软删（P2 批次1 起推荐经 app Facade 调用以同步产事件+version++）。
func DeleteNote(id, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := DeleteNoteTx(tx, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// RestoreNote 自持事务恢复（P2 批次1 起推荐经 app Facade 调用以同步产事件+version++）。
func RestoreNote(id, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := RestoreNoteTx(tx, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveNoteToFolder 自持事务移动（P2 批次1 起推荐经 app Facade 调用以同步产事件+version++）。
func MoveNoteToFolder(id, userID int64, folderID *int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := MoveNoteToFolderTx(tx, id, userID, folderID); err != nil {
		return err
	}
	return tx.Commit()
}

// CopyNoteToFolder 自持事务复制（P2 批次1 起推荐经 app Facade 调用以同步产事件）。
func CopyNoteToFolder(id, userID int64, folderID *int64, maxNotes int) (*Note, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	newID, err := CopyNoteToFolderTx(tx, id, userID, folderID, maxNotes)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	note, err := GetNote(newID, userID)
	if err != nil {
		return nil, err
	}
	note.FolderID = folderID
	return note, nil
}

// ---- 加密三件 Tx 变体（P2 批次1·刀2，§60 / SOURCE-FACT #9~#11）----
//
// 与 CRUD 同纪律：本层不做服务端 version++（桌面本地路径复用），bump 与事件由
// app server 模式在同一事务内补齐。Set/ClearEncrypted 的 CAS 基点取事务内当前版本
// （BEGIN IMMEDIATE 持写锁后读-改-写无竞态），Web API 不要求客户端传 version。

// SetEncryptedTx 明文 → 加密态（CAS：is_encrypted=0 且 version=事务内读到值）。
// 0 行命中区分：已加密（并发加密）/ 版本漂移 → ErrAlreadyEncrypted / ErrVersionConflict；行不存在 → ErrNoRows。
func SetEncryptedTx(tx *sql.Tx, noteID, userID int64, passwordHash, ciphertext string) error {
	var (
		ver   int64
		isEnc int
	)
	if err := tx.QueryRow(
		"SELECT version, CASE WHEN is_encrypted=1 THEN 1 ELSE 0 END FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		noteID, userID,
	).Scan(&ver, &isEnc); err != nil {
		return err
	}
	if isEnc == 1 {
		return ErrAlreadyEncrypted
	}
	res, err := tx.Exec(
		"UPDATE notes SET is_encrypted=1, encrypt_password_hash=?, encrypted_content=?, content='', content_text='', version=version+1 WHERE id=? AND user_id=? AND is_encrypted=0 AND version=?",
		passwordHash, ciphertext, noteID, userID, ver,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrVersionConflict
	}
	return nil
}

// ClearEncryptedTx 加密态 → 明文落回（CAS：is_encrypted=1）。
func ClearEncryptedTx(tx *sql.Tx, noteID, userID int64, content, contentText string) error {
	var (
		ver   int64
		isEnc int
	)
	if err := tx.QueryRow(
		"SELECT version, CASE WHEN is_encrypted=1 THEN 1 ELSE 0 END FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		noteID, userID,
	).Scan(&ver, &isEnc); err != nil {
		return err
	}
	if isEnc == 0 {
		return ErrNotEncrypted
	}
	res, err := tx.Exec(
		"UPDATE notes SET is_encrypted=0, encrypt_password_hash='', encrypted_content='', content=?, content_text=?, version=version+1 WHERE id=? AND user_id=? AND is_encrypted=1 AND version=?",
		content, contentText, noteID, userID, ver,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrVersionConflict
	}
	return nil
}

// UpdateEncryptedNoteTx 加密笔记更新（v21.52 单事务纪律的 Tx 化）：CAS baseVersion，
// 明文只在事务内存、落库即密文。返回更新后的新版本。
func UpdateEncryptedNoteTx(tx *sql.Tx, id, userID int64, title, tags, passwordHash, ciphertext, plaintext string, version int64) (int64, error) {
	res, err := tx.Exec(
		"UPDATE notes SET title=?, content='', content_text='', tags=?, is_encrypted=1, encrypt_password_hash=?, encrypted_content=?, version=version+1, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND deleted_at IS NULL AND is_encrypted=1 AND version=?",
		title, tags, passwordHash, ciphertext, id, userID, version,
	)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).Scan(&exists); err == nil && exists > 0 {
			return 0, ErrVersionConflict
		}
		return 0, sql.ErrNoRows
	}
	if err := syncNoteTagsTx(tx, id, tags); err != nil {
		return 0, err
	}
	// 加密笔记以明文 HTML 提取附件建立关系（明文不落库，v21.52 纪律）。
	if err := database.SyncNoteUploadsTx(tx, id, userID, plaintext); err != nil {
		return 0, err
	}
	return readNoteVersionTx(tx, id)
}
