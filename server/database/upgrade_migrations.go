package database

import (
	"fmt"

	"stickynote/htmltext"
	"stickynote/logger"
)

func backfillContentText() error {
	rows, err := DB.Query("SELECT id, content FROM notes WHERE content_text = ''")
	if err != nil {
		return err
	}
	type noteRow struct {
		id      int64
		content string
	}
	var pending []noteRow
	for rows.Next() {
		var r noteRow
		if err := rows.Scan(&r.id, &r.content); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, r := range pending {
		if _, err := DB.Exec("UPDATE notes SET content_text = ? WHERE id = ?", htmltext.ToText(r.content), r.id); err != nil {
			return err
		}
	}
	if len(pending) > 0 {
		logger.Info("Backfilled content_text for %d notes", len(pending))
	}
	return nil
}

// backfillNoteUploads 回填存量笔记的附件关系（v21.58 F-02）。
// 仅能回填 content 非空的普通笔记；加密笔记的 Content 为空，
// 其附件关系需在解密编辑保存时重建（历史限制，已文档化）。
// v21.67 P1-04：插入时校验 filename 归属当前用户，防止历史跨用户引用建立关系。
// v21.68 P1-02：返回首个插入错误（供调用方据此决定是否写完成旗标，实现可重试）。
func backfillNoteUploads() error {
	rows, err := DB.Query("SELECT id, user_id, content FROM notes WHERE content != ''")
	if err != nil {
		return err
	}
	defer rows.Close()
	type ref struct {
		noteID int64
		userID int64
		name   string
	}
	var inserts []ref
	for rows.Next() {
		var id int64
		var uid int64
		var content string
		if err := rows.Scan(&id, &uid, &content); err != nil {
			return err // v21.69 P2-01：单行失败即返回，不再跳过（避免部分迁移仍写完成）
		}
		// v21.74：复用 ExtractUploadRefs 统一事实源（内部去重+保序），消除迁移/运行时的正则漂移
		for _, name := range ExtractUploadRefs(content) {
			inserts = append(inserts, ref{id, uid, name})
		}
	}
	if err := rows.Err(); err != nil {
		return err // v21.69 P2-01：遍历结束检查底层错误
	}
	var firstErr error
	for _, r := range inserts {
		if _, err := DB.Exec(
			"INSERT OR IGNORE INTO note_uploads (note_id, filename) SELECT ?, filename FROM uploads WHERE filename = ? AND user_id = ?",
			r.noteID, r.name, r.userID,
		); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			logger.Error("note_uploads backfill insert: %v", err) // v21.58 F-16：错误不再忽略
		}
	}
	if len(inserts) > 0 {
		logger.Info("note_uploads backfilled %d refs", len(inserts))
	}
	return firstErr
}

// addNoteUploadsFK 为老库 note_uploads 表补 FK 到 uploads(filename)（v21.71 P2-02）。
// SQLite 不支持 ALTER TABLE 加 FK，需重建表：清孤儿 → 建新表 → 迁数据 → 换名 → 建索引。
// 整个流程在一个事务内，中断即回滚，下次启动由旗标触发重试。
func addNoteUploadsFK() error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. 清理存量孤儿关系（filename 不在 uploads 中，否则 INSERT 时 FK 约束会失败）
	if _, err := tx.Exec("DELETE FROM note_uploads WHERE filename NOT IN (SELECT filename FROM uploads)"); err != nil {
		return fmt.Errorf("clean orphan note_uploads: %w", err)
	}

	// 2. 建新表（带 FK）
	if _, err := tx.Exec(`CREATE TABLE note_uploads_new (
		note_id INTEGER NOT NULL,
		filename TEXT NOT NULL,
		PRIMARY KEY (note_id, filename),
		FOREIGN KEY (note_id) REFERENCES notes(id) ON DELETE CASCADE,
		FOREIGN KEY (filename) REFERENCES uploads(filename) ON DELETE CASCADE
	)`); err != nil {
		return fmt.Errorf("create note_uploads_new: %w", err)
	}

	// 3. 迁数据
	if _, err := tx.Exec("INSERT INTO note_uploads_new SELECT note_id, filename FROM note_uploads"); err != nil {
		return fmt.Errorf("migrate note_uploads: %w", err)
	}

	// 4. 换表
	if _, err := tx.Exec("DROP TABLE note_uploads"); err != nil {
		return fmt.Errorf("drop old note_uploads: %w", err)
	}
	if _, err := tx.Exec("ALTER TABLE note_uploads_new RENAME TO note_uploads"); err != nil {
		return fmt.Errorf("rename note_uploads_new: %w", err)
	}

	// 5. 重建索引
	if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_note_uploads_filename ON note_uploads(filename)"); err != nil {
		return fmt.Errorf("create note_uploads index: %w", err)
	}

	// 6. 旗标
	if _, err := tx.Exec("INSERT INTO configs (key, value, description) VALUES ('note_uploads_fk_added', 'true', 'note_uploads FK 已添加（v21.71）') ON CONFLICT(key) DO UPDATE SET value = 'true'"); err != nil {
		return fmt.Errorf("write note_uploads_fk_added flag: %w", err)
	}
	return tx.Commit()
}
