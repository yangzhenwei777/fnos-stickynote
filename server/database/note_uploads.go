package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"stickynote/logger"
)

// uploadRefRe 统一匹配笔记内容中的本机图片引用（v21.58 F-07：全仓唯一事实源，
// 与历史分散在 handler/database 的多个正则合并；含扩展名 .png/.jpg 等）。
// v21.90 P1（N-05 审计核对）：收紧边界——首字符必须为字母/数字且必须含扩展名，
// 拒绝 `.`、`..`、`..foo` 等纯点/点开头名（上传文件名恒为 UUID+白名单扩展名，不受影响）。
var uploadRefRe = regexp.MustCompile(`/uploads/([A-Za-z0-9][A-Za-z0-9._-]*\.[A-Za-z0-9]{1,10})`)

// 附件生命周期状态（v21.70：统一状态机，消除散落字符串字面量）。
// staged → attached → orphan（宽限期内可恢复）→ delete_pending（确定删除、物理删除失败待重试）→ deleted
const (
	UploadStatusStaged        = "staged"         // 上传但未绑定笔记
	UploadStatusAttached      = "attached"       // 被笔记引用
	UploadStatusOrphan        = "orphan"         // 移除引用，宽限期内可恢复
	UploadStatusDeletePending = "delete_pending" // 确定删除，物理删除失败待重试
)

// ExtractUploadRefs 从 HTML 内容提取 /uploads/ 引用的文件名（去重、保持出现顺序）。
func ExtractUploadRefs(content string) []string {
	matches := uploadRefRe.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	refs := make([]string, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			refs = append(refs, name)
		}
	}
	return refs
}

// SyncNoteUploadsTx 在事务内同步笔记的附件关系（先删旧、再插新）。
// v21.58 F-02：note_uploads 作为「笔记 → 附件」的唯一事实源；
// 加密笔记的 Content 为空，也能通过此表追踪附件生命周期。
// 传入的 content 必须是明文 HTML（加密笔记在加密前调用本函数）。
// v21.66 P1-6：插入时校验 filename 归属当前用户，防止跨用户引用他人文件建立关系。
// v21.66 P1-3：同步时更新 uploads.status 状态机——新引用标记 attached，
// 被移除且无其他引用的附件标记 orphan（由后台 GC 统一清理物理文件）。
func SyncNoteUploadsTx(tx *sql.Tx, noteID, userID int64, content string) error {
	// 查旧关系（用于标记被移除的附件为 orphan）
	oldRows, err := tx.Query("SELECT filename FROM note_uploads WHERE note_id = ?", noteID)
	if err != nil {
		return err
	}
	var oldNames []string
	for oldRows.Next() {
		var name string
		if err := oldRows.Scan(&name); err != nil {
			oldRows.Close()
			return err
		}
		oldNames = append(oldNames, name)
	}
	oldRows.Close()

	// 删旧关系
	if _, err := tx.Exec("DELETE FROM note_uploads WHERE note_id = ?", noteID); err != nil {
		return err
	}

	// 插新关系（校验归属）+ 标记 attached
	newSet := make(map[string]bool)
	for _, name := range ExtractUploadRefs(content) {
		newSet[name] = true
		if _, err := tx.Exec(
			"INSERT OR IGNORE INTO note_uploads (note_id, filename) SELECT ?, filename FROM uploads WHERE filename = ? AND user_id = ?",
			noteID, name, userID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			"UPDATE uploads SET status = 'attached', status_changed_at = CURRENT_TIMESTAMP WHERE filename = ? AND user_id = ?",
			name, userID,
		); err != nil {
			return err
		}
	}

	// 标记被移除的附件为 orphan（无其他笔记引用时）
	for _, name := range oldNames {
		if newSet[name] {
			continue
		}
		var cnt int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM note_uploads WHERE filename = ?", name,
		).Scan(&cnt); err != nil {
			return err
		}
		if cnt == 0 {
			if _, err := tx.Exec(
				"UPDATE uploads SET status = 'orphan', status_changed_at = CURRENT_TIMESTAMP WHERE filename = ? AND user_id = ?",
				name, userID,
			); err != nil {
				return err
			}
		}
	}

	return nil
}

// ListNoteUploads 返回笔记引用的附件文件名清单。
// 必须在删除笔记**之前**调用——note_uploads 有 ON DELETE CASCADE，
// 笔记删除后关系记录随之消失，无法再查询。
func ListNoteUploads(noteID int64) ([]string, error) {
	rows, err := DB.Query("SELECT filename FROM note_uploads WHERE note_id = ?", noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// DeleteOrphanUploadFiles 对给定文件名清单，删除「无任何笔记引用」的物理文件 + uploads 记录。
// 调用时机：笔记已从 notes 删除（note_uploads 已级联删除），此时 note_uploads 中
// 仍存在的同名记录即「其他笔记的引用」，需保留。
// v21.66 P1-6：删除 uploads 记录时校验 user_id，防止跨用户误删他人文件（第二层保护）。
func DeleteOrphanUploadFiles(uploadDir string, filenames []string, userID int64) int {
	cleaned := 0
	seen := make(map[string]bool)
	for _, name := range filenames {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		var cnt int
		if err := DB.QueryRow(
			"SELECT COUNT(*) FROM note_uploads WHERE filename = ?", name,
		).Scan(&cnt); err != nil || cnt > 0 {
			continue // 仍被其他笔记引用，保留
		}
		if err := os.Remove(filepath.Join(uploadDir, name)); err != nil && !os.IsNotExist(err) {
			logger.Error("DeleteOrphanUploadFiles: remove %s failed: %v", name, err) // v21.58 F-16
			// v21.70：删除失败标记 delete_pending（语义分离：orphan=可恢复宽限期，
			// delete_pending=确定删除待重试），由 CleanPendingUploads 统一重试，
			// 避免「DB 已删、文件残留」的永久孤儿。
			if _, uerr := DB.Exec(
				"UPDATE uploads SET status = ?, status_changed_at = CURRENT_TIMESTAMP WHERE filename = ? AND user_id = ?",
				UploadStatusDeletePending, name, userID,
			); uerr != nil {
				logger.Error("DeleteOrphanUploadFiles: mark delete_pending %s failed: %v", name, uerr)
			}
			continue
		}
		if _, err := DB.Exec("DELETE FROM uploads WHERE filename = ? AND user_id = ?", name, userID); err != nil {
			logger.Error("DeleteOrphanUploadFiles: delete upload record %s failed: %v", name, err)
		}
		cleaned++
	}
	return cleaned
}

// orphanGraceDuration 附件保留宽限期（v21.66 P1-3 / v21.67 P1-02）：标记 orphan 或保持 staged
// 后保留宽限期内不删，防止误删「刚上传/刚移除引用、用户可能反悔恢复」的文件。
// v21.108 P2-19：常量改 env 可配（UPLOAD_GRACE_HOURS，默认 24，非法/越界回落默认值）。
// 审查报告的边缘场景——「编辑中插入图片久未保存 + NAS 重启（GC 启动即跑）」会跨过 24h
// 窗口把图删成坏图；长编辑间隔/低频保存部署可调大（如 168=7 天）。
func orphanGraceDuration() time.Duration {
	const def = 24 * time.Hour
	v := strings.TrimSpace(os.Getenv("UPLOAD_GRACE_HOURS"))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > 24*365 { // 上限一年，防误配成永久不清
		logger.Info("orphanGraceDuration: UPLOAD_GRACE_HOURS=%q 非法，回落 24h", v)
		return def
	}
	return time.Duration(n) * time.Hour
}

// orphanHeldBySnapshot 批次5 §73：文件被未失效 Bootstrap 快照 Hold 时 GC 跳过。
// 查询失败按"持有"处理（fail-closed，宁漏删不误删快照引用件）。
func orphanHeldBySnapshot(filename string) bool {
	var n int
	err := DB.QueryRow(`SELECT COUNT(*) FROM sync_snapshots_hold h JOIN sync_snapshots s ON s.id = h.snapshot_id
		WHERE h.filename = ? AND s.status IN ('PENDING','BUILDING','READY')`, filename).Scan(&n)
	if err != nil {
		if !strings.Contains(err.Error(), "no such table") { // 老库迁移前窗口属正常无 Hold
			logger.Error("orphanHeldBySnapshot: query failed for %s: %v (treat as held)", filename, err)
			return true
		}
	}
	return n > 0
}

// CleanStagedUploads 清理超过宽限期的 staged 状态附件（上传后未绑定笔记的孤儿文件）。
// v21.67 P1-02：staged 文件从不进入 orphan 状态，需独立 GC；物理删除失败保留记录下次重试。
func CleanStagedUploads(uploadDir string) int {
	cutoff := time.Now().Add(-orphanGraceDuration())
	rows, err := DB.Query(
		"SELECT filename, user_id FROM uploads WHERE status = 'staged' AND created_at < ?",
		cutoff,
	)
	if err != nil {
		logger.Error("CleanStagedUploads: query failed: %v", err)
		return 0
	}
	defer rows.Close()

	type staged struct {
		filename string
		userID   int64
	}
	var pending []staged
	for rows.Next() {
		var s staged
		if err := rows.Scan(&s.filename, &s.userID); err != nil {
			continue
		}
		pending = append(pending, s)
	}

	cleaned := 0
	for _, s := range pending {
		// 批次5 §73：Bootstrap 快照 Hold 的文件**整段跳过**——含"转 attached"状态流转，
		// 否则投影/打包期间被 GC 改写状态，Hold 保护形同虚设（刀 1 测试坐实）。
		if orphanHeldBySnapshot(s.filename) {
			continue
		}
		// 防御性二次确认：staged 理论上无引用，若异常被引用则改标记 attached 保留。
		// v21.69 P1-02：归属查询失败即不删除（fail-closed），宁漏清理也不误删被引用文件。
		var cnt int
		if err := DB.QueryRow("SELECT COUNT(*) FROM note_uploads WHERE filename = ?", s.filename).Scan(&cnt); err != nil {
			logger.Error("CleanStagedUploads: reference query failed for %s: %v (skip)", s.filename, err)
			continue // 状态不确定，不删除
		}
		if cnt > 0 {
			DB.Exec("UPDATE uploads SET status = 'attached', status_changed_at = CURRENT_TIMESTAMP WHERE filename = ? AND user_id = ?", s.filename, s.userID)
			continue
		}
		if err := os.Remove(filepath.Join(uploadDir, s.filename)); err != nil && !os.IsNotExist(err) {
			logger.Error("CleanStagedUploads: remove %s failed: %v", s.filename, err)
			continue // 删除失败保留记录，下次重试
		}
		if _, err := DB.Exec(
			"DELETE FROM uploads WHERE filename = ? AND user_id = ? AND status = 'staged'",
			s.filename, s.userID,
		); err != nil {
			logger.Error("CleanStagedUploads: delete upload record %s failed: %v", s.filename, err)
			continue
		}
		cleaned++
	}
	return cleaned
}

// CleanOrphanUploads 清理超过宽限期的 orphan 状态附件（物理文件 + uploads 记录）。
// 每次启动执行一次（与 CleanExpiredTrash 同频），轻量扫描。
// v21.66 P2：物理删除失败时保留 uploads 记录，下次启动重试（不再「DB 已删、文件残留」）。
func CleanOrphanUploads(uploadDir string) int {
	cutoff := time.Now().Add(-orphanGraceDuration())
	rows, err := DB.Query(
		"SELECT filename, user_id FROM uploads WHERE status = 'orphan' AND status_changed_at < ?",
		cutoff,
	)
	if err != nil {
		logger.Error("CleanOrphanUploads: query failed: %v", err)
		return 0
	}
	defer rows.Close()

	type orphan struct {
		filename string
		userID   int64
	}
	var pending []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.filename, &o.userID); err != nil {
			continue
		}
		pending = append(pending, o)
	}

	cleaned := 0
	for _, o := range pending {
		if orphanHeldBySnapshot(o.filename) { // 批次5 §73
			continue
		}
		if err := os.Remove(filepath.Join(uploadDir, o.filename)); err != nil && !os.IsNotExist(err) {
			logger.Error("CleanOrphanUploads: remove %s failed: %v", o.filename, err)
			continue // 删除失败保留记录，下次重试
		}
		if _, err := DB.Exec(
			"DELETE FROM uploads WHERE filename = ? AND user_id = ? AND status = 'orphan'",
			o.filename, o.userID,
		); err != nil {
			logger.Error("CleanOrphanUploads: delete upload record %s failed: %v", o.filename, err)
			continue
		}
		cleaned++
	}
	return cleaned
}

// CleanPendingUploads 重试清理删除失败的物理文件（v21.67 P1-03）。
// 删除用户时物理文件删除失败会入队 pending_upload_cleanup，此函数兜底重试。
// v21.68 P2-04：按 retry_count 指数退避（0→立即、之后 2h/4h/8h/16h），
// 超过 maxPendingRetries 后放弃静默重试，避免日志污染。
func CleanPendingUploads(uploadDir string) int {
	const maxPendingRetries = 5
	rows, err := DB.Query("SELECT filename, retry_count, last_attempt_at FROM pending_upload_cleanup")
	if err != nil {
		logger.Error("CleanPendingUploads: query failed: %v", err)
		return 0
	}
	defer rows.Close()

	type item struct {
		filename      string
		retryCount    int
		lastAttemptAt sql.NullTime
	}
	var pending []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.filename, &it.retryCount, &it.lastAttemptAt); err != nil {
			continue
		}
		pending = append(pending, it)
	}

	now := time.Now()
	cleaned := 0
	for _, it := range pending {
		if it.retryCount >= maxPendingRetries {
			continue // 超过最大重试次数，放弃（保留记录备查）
		}
		// 指数退避：retry 0 立即，1→2h，2→4h，3→8h，4→16h
		if it.lastAttemptAt.Valid {
			backoff := time.Duration(1<<uint(it.retryCount)) * time.Hour
			if now.Sub(it.lastAttemptAt.Time) < backoff {
				continue // 未到退避时间
			}
		}
		if orphanHeldBySnapshot(it.filename) { // 批次5 §73
			continue
		}
		if err := os.Remove(filepath.Join(uploadDir, it.filename)); err != nil && !os.IsNotExist(err) {
			logger.Error("CleanPendingUploads: remove %s failed: %v", it.filename, err)
			DB.Exec("UPDATE pending_upload_cleanup SET retry_count = retry_count + 1, last_attempt_at = CURRENT_TIMESTAMP WHERE filename = ?", it.filename)
			// v21.69 P2-05：达到最大重试次数时告警（仅在临界点告警一次），提示管理员手工处理
			if it.retryCount+1 >= maxPendingRetries {
				logger.Error("CleanPendingUploads: %s reached max retries (%d), manual cleanup required", it.filename, maxPendingRetries)
			}
			continue
		}
		if _, err := DB.Exec("DELETE FROM pending_upload_cleanup WHERE filename = ?", it.filename); err != nil {
			logger.Error("CleanPendingUploads: delete queue record %s failed: %v", it.filename, err)
			continue
		}
		cleaned++
	}

	// v21.70：统一处理 uploads.status='delete_pending'（普通删除失败待重试）。
	// delete_pending 语义是「确定删除」，应尽快重试——随 CleanPendingUploads 每 6h 周期直接重试，
	// 不做额外退避（与 pending_upload_cleanup 表的指数退避区分）。
	dpRows, err := DB.Query("SELECT filename, user_id FROM uploads WHERE status = ?", UploadStatusDeletePending)
	if err != nil {
		logger.Error("CleanPendingUploads: query delete_pending failed: %v", err)
		return cleaned
	}
	defer dpRows.Close()
	type dpItem struct {
		filename string
		userID   int64
	}
	var dpItems []dpItem
	for dpRows.Next() {
		var it dpItem
		if err := dpRows.Scan(&it.filename, &it.userID); err != nil {
			continue
		}
		dpItems = append(dpItems, it)
	}
	for _, it := range dpItems {
		if err := os.Remove(filepath.Join(uploadDir, it.filename)); err != nil && !os.IsNotExist(err) {
			logger.Error("CleanPendingUploads: remove delete_pending %s failed: %v", it.filename, err)
			continue // 保留 delete_pending，下次 6h 后重试
		}
		if _, err := DB.Exec("DELETE FROM uploads WHERE filename = ? AND user_id = ? AND status = ?", it.filename, it.userID, UploadStatusDeletePending); err != nil {
			logger.Error("CleanPendingUploads: delete delete_pending record %s failed: %v", it.filename, err)
			continue
		}
		cleaned++
	}
	return cleaned
}
