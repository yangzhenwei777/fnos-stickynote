package database

import (
	"strconv"
	"time"

	"stickynote/logger"
)

// CleanExpiredTrash 根据 trash_retention_days 配置清理过期回收站笔记（P2-6）。
// 默认 0=不自动清理；>0 时删除 deleted_at 超过保留天数的笔记及无引用上传文件。
// 每次启动执行一次，轻量扫描（仅扫回收站笔记）。
func CleanExpiredTrash(uploadDir string) {
	var val string
	if err := DB.QueryRow("SELECT value FROM configs WHERE key = 'trash_retention_days'").Scan(&val); err != nil {
		return // 配置键不存在或读取失败
	}
	days, err := strconv.Atoi(val)
	if err != nil || days <= 0 {
		return // 0 或无效值 = 永久保留（默认行为）
	}

	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	// 1. 查过期回收站笔记（同时取 user_id，供附件清理时校验归属）
	rows, err := DB.Query(
		"SELECT id, user_id FROM notes WHERE deleted_at IS NOT NULL AND deleted_at < ?",
		cutoff,
	)
	if err != nil {
		logger.Error("CleanExpiredTrash: query failed: %v", err)
		return
	}
	defer rows.Close()

	type expiredNote struct {
		id     int64
		userID int64
	}
	var expired []expiredNote
	for rows.Next() {
		var n expiredNote
		if err := rows.Scan(&n.id, &n.userID); err != nil {
			continue
		}
		expired = append(expired, n)
	}

	if len(expired) == 0 {
		return
	}

	// v21.58 F-02：删除前收集附件清单（note_uploads 随笔记级联删除，删后不可再查）。
	// 此前用 content 正则扫描，但加密笔记 Content 为空、且正则漏扩展名（F-07），
	// 导致附件无法清理——现统一走 note_uploads 关系表。
	// v21.66 P1-6：按 userID 分组，删除时校验归属。
	// v21.69 P1-01：读取附件关系失败即跳过该笔记（fail-closed），下次重试，避免附件关系消失后文件残留。
	grouped := map[int64][]string{}
	var deletable []expiredNote
	for _, n := range expired {
		names, err := ListNoteUploads(n.id)
		if err != nil {
			logger.Error("CleanExpiredTrash: list attachments for note %d failed: %v (skip, retry next run)", n.id, err)
			continue // 不删除该笔记，下次重试
		}
		grouped[n.userID] = append(grouped[n.userID], names...)
		deletable = append(deletable, n)
	}

	// 2. 删笔记（级联删 note_uploads）
	deleted := 0
	for _, n := range deletable {
		if _, err := DB.Exec("DELETE FROM notes WHERE id = ?", n.id); err != nil {
			logger.Error("CleanExpiredTrash: delete note %d failed: %v", n.id, err)
			continue
		}
		deleted++
	}

	// 3. 按用户清无引用上传文件
	orphanFiles := 0
	for uid, names := range grouped {
		orphanFiles += DeleteOrphanUploadFiles(uploadDir, names, uid)
	}
	logger.Info("CleanExpiredTrash: cleaned %d expired notes, %d orphan upload files (retention=%d days)", deleted, orphanFiles, days)
}
