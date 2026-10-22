package model

import (
	"database/sql"
	"time"

	"stickynote/database"
	"stickynote/logger"
)

// 备份功能数据访问层（v1.0.167）。表结构见 database/upgrade_backup.go。
// 敏感字段 secret_encrypted 存的是"主密钥子密钥 EncryptRaw 后的 base64 串"——
// 加解密在服务层完成，本层只存取字符串；**绝不**把明文 WebDAV 密码写库或回显 API。

// BackupTarget 一个备份目标（一期仅 WebDAV）。
type BackupTarget struct {
	ID                  int64      `json:"id"`
	Name                string     `json:"name"`
	Type                string     `json:"type"`
	BaseURL             string     `json:"base_url"`
	Username            string     `json:"username"`
	SecretEncrypted     string     `json:"-"`          // 永不出 API
	HasSecret           bool       `json:"has_secret"` // 仅回"是否已设置"
	RemotePath          string     `json:"remote_path"`
	Enabled             bool       `json:"enabled"`
	RetentionCount      int        `json:"retention_count"`
	ScheduleInterval    string     `json:"schedule_interval"`
	NextRunAt           *time.Time `json:"next_run_at,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	AllowLoopback       bool       `json:"allow_loopback"`
	AllowInsecureTLS    bool       `json:"allow_insecure_tls"`
	CustomCAPem         string     `json:"-"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastError           string     `json:"last_error"`
}

const targetCols = `id, name, type, base_url, username, secret_encrypted, remote_path, enabled,
	retention_count, schedule_interval, next_run_at, consecutive_failures, allow_loopback,
	allow_insecure_tls, custom_ca_pem, last_success_at, last_error`

func scanTarget(scan func(dest ...any) error) (BackupTarget, error) {
	var t BackupTarget
	var enabled, loopback, insecure int
	var secret, ca string
	var nextRun, lastSucc sql.NullTime
	err := scan(&t.ID, &t.Name, &t.Type, &t.BaseURL, &t.Username, &secret, &t.RemotePath, &enabled,
		&t.RetentionCount, &t.ScheduleInterval, &nextRun, &t.ConsecutiveFailures, &loopback,
		&insecure, &ca, &lastSucc, &t.LastError)
	t.SecretEncrypted = secret
	t.CustomCAPem = ca
	t.HasSecret = secret != ""
	t.Enabled = enabled != 0
	t.AllowLoopback = loopback != 0
	t.AllowInsecureTLS = insecure != 0
	if nextRun.Valid {
		v := nextRun.Time
		t.NextRunAt = &v
	}
	if lastSucc.Valid {
		v := lastSucc.Time
		t.LastSuccessAt = &v
	}
	return t, err
}

// ListBackupTargets 返回全部目标（含密文字段，仅服务内部用；API 层再脱敏）。
func ListBackupTargets() ([]BackupTarget, error) {
	rows, err := database.DB.Query("SELECT " + targetCols + " FROM backup_targets ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupTarget
	for rows.Next() {
		t, err := scanTarget(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func GetBackupTarget(id int64) (BackupTarget, error) {
	row := database.DB.QueryRow("SELECT "+targetCols+" FROM backup_targets WHERE id = ?", id)
	return scanTarget(row.Scan)
}

// CreateBackupTarget 新建目标，secretEncrypted 已是密文串；返回新 id。
func CreateBackupTarget(t BackupTarget) (int64, error) {
	res, err := database.DB.Exec(`INSERT INTO backup_targets
		(name, type, base_url, username, secret_encrypted, remote_path, enabled, retention_count,
		 schedule_interval, allow_loopback, allow_insecure_tls, custom_ca_pem)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Name, orDefault(t.Type, "webdav"), t.BaseURL, t.Username, t.SecretEncrypted,
		orDefault(t.RemotePath, "/StickyNote-Backup/"), b2i(t.Enabled), t.RetentionCount,
		orDefault(t.ScheduleInterval, "24h"), b2i(t.AllowLoopback), b2i(t.AllowInsecureTLS), t.CustomCAPem)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateBackupTarget 更新目标；secretEncrypted 为空表示"保持原密文不变"（避免脱敏回显覆盖）。
func UpdateBackupTarget(t BackupTarget) error {
	if t.SecretEncrypted == "" {
		_, err := database.DB.Exec(`UPDATE backup_targets SET name=?,type=?,base_url=?,username=?,
			remote_path=?,enabled=?,retention_count=?,schedule_interval=?,allow_loopback=?,
			allow_insecure_tls=?,custom_ca_pem=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			t.Name, orDefault(t.Type, "webdav"), t.BaseURL, t.Username,
			orDefault(t.RemotePath, "/StickyNote-Backup/"), b2i(t.Enabled), t.RetentionCount,
			orDefault(t.ScheduleInterval, "24h"), b2i(t.AllowLoopback), b2i(t.AllowInsecureTLS),
			t.CustomCAPem, t.ID)
		return err
	}
	_, err := database.DB.Exec(`UPDATE backup_targets SET name=?,type=?,base_url=?,username=?,
		secret_encrypted=?,remote_path=?,enabled=?,retention_count=?,schedule_interval=?,
		allow_loopback=?,allow_insecure_tls=?,custom_ca_pem=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		t.Name, orDefault(t.Type, "webdav"), t.BaseURL, t.Username, t.SecretEncrypted,
		orDefault(t.RemotePath, "/StickyNote-Backup/"), b2i(t.Enabled), t.RetentionCount,
		orDefault(t.ScheduleInterval, "24h"), b2i(t.AllowLoopback), b2i(t.AllowInsecureTLS),
		t.CustomCAPem, t.ID)
	return err
}

// DeleteBackupTarget 仅删本地配置，不删远端归档（避免误操作波及云端数据）。
func DeleteBackupTarget(id int64) error {
	_, err := database.DB.Exec("DELETE FROM backup_targets WHERE id = ?", id)
	return err
}

// ---- snapshots ----

// BackupSnapshot 一份 .snbak 快照本身（本地态）。
type BackupSnapshot struct {
	BackupID        string    `json:"backup_id"`
	CreatedAt       time.Time `json:"created_at"`
	Trigger         string    `json:"trigger"`
	ArchiveSha256   string    `json:"archive_sha256"`
	SizeBytes       int64     `json:"size_bytes"`
	AppVersion      string    `json:"app_version"`
	SchemaVersion   int       `json:"schema_version"`
	NoteCount       int       `json:"note_count"`
	UserCount       int       `json:"user_count"`
	AttachmentCount int       `json:"attachment_count"`
	Status          string    `json:"status"` // LOCAL_CREATED|LOCAL_VERIFIED|INCOMPLETE|FAILED
	Incomplete      bool      `json:"incomplete"`
	// 以下两项由 ListBackupSnapshots 经子查询计算，非 backup_snapshots 列：
	// VerifiedPlacements = 该 backup_id 在任意目标上 status='VERIFIED' 的放置数；
	// HasRemote = VerifiedPlacements>0，即"有可下载/可恢复的远端副本"。历史 prune 遗留的
	// 孤儿快照行（有记录无副本）HasRemote=false，前端据此禁用下载/恢复/校验。
	VerifiedPlacements int  `json:"verified_placements"`
	HasRemote          bool `json:"has_remote"`
}

// InsertBackupSnapshot 登记一份本地快照（created_at 用 UTC）。
func InsertBackupSnapshot(s BackupSnapshot) error {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	_, err := database.DB.Exec(`INSERT INTO backup_snapshots
		(backup_id, created_at, trigger_kind, archive_sha256, size_bytes, app_version,
		 schema_version, note_count, user_count, attachment_count, status, incomplete)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(backup_id) DO UPDATE SET
		status=excluded.status, archive_sha256=excluded.archive_sha256, size_bytes=excluded.size_bytes`,
		s.BackupID, s.CreatedAt.UTC().Format(time.RFC3339), orDefault(s.Trigger, "manual"),
		s.ArchiveSha256, s.SizeBytes, s.AppVersion, s.SchemaVersion, s.NoteCount, s.UserCount,
		s.AttachmentCount, orDefault(s.Status, "LOCAL_CREATED"), b2i(s.Incomplete))
	return err
}

func ListBackupSnapshots(limit int) ([]BackupSnapshot, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := database.DB.Query(`SELECT s.backup_id, s.created_at, s.trigger_kind, s.archive_sha256,
		s.size_bytes, s.app_version, s.schema_version, s.note_count, s.user_count, s.attachment_count,
		s.status, s.incomplete,
		(SELECT COUNT(*) FROM backup_placements p WHERE p.backup_id = s.backup_id AND p.status='VERIFIED')
		FROM backup_snapshots s ORDER BY s.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupSnapshot
	for rows.Next() {
		var s BackupSnapshot
		var createdStr string // created_at 存的是 RFC3339 文本，必须按 string 扫（早先误用 int → Scan 报错 → 列表恒空）
		var inc int
		if err := rows.Scan(&s.BackupID, &createdStr, &s.Trigger, &s.ArchiveSha256, &s.SizeBytes,
			&s.AppVersion, &s.SchemaVersion, &s.NoteCount, &s.UserCount, &s.AttachmentCount,
			&s.Status, &inc, &s.VerifiedPlacements); err != nil {
			return nil, err
		}
		if ts, perr := time.Parse(time.RFC3339, createdStr); perr == nil {
			s.CreatedAt = ts
		}
		s.Incomplete = inc != 0
		s.HasRemote = s.VerifiedPlacements > 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// SnapshotExists 判断某 backup_id 是否已在本地快照账本中（远端发现时避免覆盖更丰富的本地记录）。
func SnapshotExists(backupID string) (bool, error) {
	var n int
	err := database.DB.QueryRow("SELECT COUNT(*) FROM backup_snapshots WHERE backup_id=?", backupID).Scan(&n)
	return n > 0, err
}

// ---- placements ----

// UpsertBackupPlacement 建立/更新 (backup_id,target_id) 的上传状态。
func UpsertBackupPlacement(backupID string, targetID int64, status, remotePath, errMsg string) error {
	_, err := database.DB.Exec(`INSERT INTO backup_placements(backup_id, target_id, status, remote_path, error,
		uploaded_at, verified_at) VALUES(?,?,?,?,?, CURRENT_TIMESTAMP,
		CASE WHEN ?='VERIFIED' THEN CURRENT_TIMESTAMP ELSE NULL END)
		ON CONFLICT(backup_id,target_id) DO UPDATE SET status=excluded.status,
		remote_path=excluded.remote_path, error=excluded.error,
		verified_at=CASE WHEN excluded.status='VERIFIED' THEN CURRENT_TIMESTAMP ELSE backup_placements.verified_at END`,
		backupID, targetID, status, remotePath, errMsg, status)
	return err
}

// ---- jobs（执行行为 + 崩溃扫描）----

// CreateBackupJob 登记一次执行（type=backup|restore），返回 job id。
func CreateBackupJob(jobType string, targetID int64, backupID string) (int64, error) {
	res, err := database.DB.Exec(`INSERT INTO backup_jobs(type, target_id, backup_id, status, phase)
		VALUES(?,?,?,?, '')`, jobType, targetID, backupID, "STARTING")
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateJobPhase(id int64, status, phase string) error {
	_, err := database.DB.Exec("UPDATE backup_jobs SET status=?, phase=? WHERE id=?", status, phase, id)
	return err
}

func FinishJob(id int64, status, errCode, errMsg string) error {
	_, err := database.DB.Exec(`UPDATE backup_jobs SET status=?, error_code=?, error_message=?,
		finished_at=CURRENT_TIMESTAMP WHERE id=?`, status, errCode, errMsg, id)
	return err
}

// InterruptStaleJobs 启动期崩溃恢复：把上次进程残留的 STARTING/RUNNING 任务标 INTERRUPTED。
// 返回受影响行数。备份任务不自动续传（下个调度周期再做），恢复任务由 restore.Recovery 处理。
func InterruptStaleJobs() (int, error) {
	res, err := database.DB.Exec(`UPDATE backup_jobs SET status='INTERRUPTED',
		error_message='interrupted by restart', finished_at=CURRENT_TIMESTAMP
		WHERE status IN ('STARTING','RUNNING')`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteBackupPlacement 删除 (backup_id,target_id) 放置记录（远端已删除后清本地态）。
func DeleteBackupPlacement(backupID string, targetID int64) error {
	_, err := database.DB.Exec("DELETE FROM backup_placements WHERE backup_id=? AND target_id=?", backupID, targetID)
	return err
}

// BackupPlacementRow 一行放置记录（手动删除备份时遍历所有目标用）。
type BackupPlacementRow struct {
	TargetID   int64  `json:"target_id"`
	RemotePath string `json:"remote_path"`
	Status     string `json:"status"`
}

// ListBackupPlacements 返回某 backup_id 在所有目标上的放置记录（含 FAILED/UPLOADING，
// 手动删除时要尽力清干净每个目标上的远端对象，不能只挑 VERIFIED）。
func ListBackupPlacements(backupID string) ([]BackupPlacementRow, error) {
	rows, err := database.DB.Query(
		"SELECT target_id, remote_path, status FROM backup_placements WHERE backup_id=?", backupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupPlacementRow
	for rows.Next() {
		var p BackupPlacementRow
		if err := rows.Scan(&p.TargetID, &p.RemotePath, &p.Status); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteBackupSnapshot 删除快照行；backup_placements 通过 FK ON DELETE CASCADE 自动清空。
// 调用前须先把所有目标上的远端对象删干净（否则远端会留孤儿归档）。
func DeleteBackupSnapshot(backupID string) error {
	_, err := database.DB.Exec("DELETE FROM backup_snapshots WHERE backup_id=?", backupID)
	return err
}

// CountBackupPlacements 返回某 backup_id 剩余的放置记录数。轮转删掉某目标的放置后，
// 若计数归零说明该快照在所有目标上都已无远端副本（孤儿）→ 可一并删快照行，避免列表残留。
func CountBackupPlacements(backupID string) (int, error) {
	var n int
	err := database.DB.QueryRow("SELECT COUNT(*) FROM backup_placements WHERE backup_id=?", backupID).Scan(&n)
	return n, err
}

// PurgeOrphanBackupSnapshotsOnce 一次性清理"有快照行但无任何放置记录"的孤儿（v21.130）。
// 根因：v21.127/128 时代的 prune 删远端文件 + 删 placement，却**不删 backup_snapshots 行**，
// 留下列表可见、点下载却必失败的死记录。本函数经 configs 旗标幂等控制，仅在升级后首次
// 启动跑一次（由 cmd/server 在调度器启动前调用，故无并发在途备份被误删）。
// 返回清理行数；旗标已置位则直接返回 (0, nil) 不再执行。
func PurgeOrphanBackupSnapshotsOnce() (int, error) {
	const flagKey = "orphan_snapshots_purged_v170"
	var flag string
	if err := database.DB.QueryRow("SELECT value FROM configs WHERE key=?", flagKey).Scan(&flag); err == nil && flag == "true" {
		return 0, nil // 已清理过
	}
	res, err := database.DB.Exec(`DELETE FROM backup_snapshots
		WHERE backup_id NOT IN (SELECT DISTINCT backup_id FROM backup_placements)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, serr := database.DB.Exec(`INSERT INTO configs (key, value, description)
		VALUES (?, 'true', '存量孤儿备份快照行一次性清理已完成（v21.130）')
		ON CONFLICT(key) DO UPDATE SET value='true'`, flagKey); serr != nil {
		return int(n), serr
	}
	return int(n), nil
}

// SetTargetRunResult 备份执行结束后回写目标健康态：
// 成功 → last_success_at=now、consecutive_failures 归零、清 last_error；
// 失败 → consecutive_failures+1、记 last_error。
func SetTargetRunResult(id int64, success bool, errMsg string) error {
	if success {
		_, err := database.DB.Exec(`UPDATE backup_targets SET last_success_at=CURRENT_TIMESTAMP,
			consecutive_failures=0, last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		return err
	}
	_, err := database.DB.Exec(`UPDATE backup_targets SET
		consecutive_failures=consecutive_failures+1, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		truncate(errMsg, 500), id)
	return err
}

// SetTargetNextRun 更新下次调度时间（RFC3339 UTC；传空串清除）。
func SetTargetNextRun(id int64, nextRunRFC3339 string) error {
	if nextRunRFC3339 == "" {
		_, err := database.DB.Exec("UPDATE backup_targets SET next_run_at=NULL WHERE id=?", id)
		return err
	}
	_, err := database.DB.Exec("UPDATE backup_targets SET next_run_at=? WHERE id=?", nextRunRFC3339, id)
	return err
}

// SetTargetRetention 调整某目标的保留份数（测试/管理用）。
func SetTargetRetention(id int64, keep int) error {
	_, err := database.DB.Exec("UPDATE backup_targets SET retention_count=? WHERE id=?", keep, id)
	return err
}

// ListPrunableBackupIDs 返回该目标上"已验证(VERIFIED)"、但超出保留数 keep 的旧备份 id
// （按快照创建时间倒序，keep 之后的都列为候选删除）。never-to-zero 由调用方兜底：
// keep<1 一律按 1 处理，且运行期还会额外排除本次刚上传的 id。
func ListPrunableBackupIDs(targetID int64, keep int) ([]string, error) {
	if keep < 1 {
		keep = 1
	}
	rows, err := database.DB.Query(`SELECT p.backup_id FROM backup_placements p
		JOIN backup_snapshots s ON s.backup_id = p.backup_id
		WHERE p.target_id=? AND p.status='VERIFIED'
		ORDER BY s.created_at DESC, p.id DESC LIMIT -1 OFFSET ?`, targetID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// BackupJob 一次执行行为的状态（API status/轮询用）。
type BackupJob struct {
	ID           int64  `json:"id"`
	Type         string `json:"type"`
	TargetID     int64  `json:"target_id"`
	BackupID     string `json:"backup_id"`
	Status       string `json:"status"`
	Phase        string `json:"phase"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at,omitempty"`
}

func scanJob(scan func(dest ...any) error) (BackupJob, error) {
	var j BackupJob
	var targetID sql.NullInt64
	var finished sql.NullString
	err := scan(&j.ID, &j.Type, &targetID, &j.BackupID, &j.Status, &j.Phase,
		&j.ErrorCode, &j.ErrorMessage, &j.StartedAt, &finished)
	if targetID.Valid {
		j.TargetID = targetID.Int64
	}
	if finished.Valid {
		j.FinishedAt = finished.String
	}
	return j, err
}

const jobCols = `id, type, target_id, backup_id, status, phase, error_code, error_message, started_at, finished_at`

func GetBackupJob(id int64) (BackupJob, error) {
	row := database.DB.QueryRow("SELECT "+jobCols+" FROM backup_jobs WHERE id=?", id)
	return scanJob(row.Scan)
}

func ListBackupJobs(limit int) ([]BackupJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := database.DB.Query("SELECT "+jobCols+" FROM backup_jobs ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupJob
	for rows.Next() {
		j, err := scanJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetPlacementForDownload 找某备份在任一目标上的 VERIFIED 放置，返回目标 id + 远端归档路径。
func GetPlacementForDownload(backupID string) (targetID int64, remotePath string, ok bool, err error) {
	row := database.DB.QueryRow(`SELECT target_id, remote_path FROM backup_placements
		WHERE backup_id=? AND status='VERIFIED' ORDER BY verified_at DESC LIMIT 1`, backupID)
	err = row.Scan(&targetID, &remotePath)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return targetID, remotePath, true, nil
}

// ListDueTargets 返回 enabled 且 next_run_at 已到（<= nowRFC）的目标。
func ListDueTargets(nowRFC string) ([]BackupTarget, error) {
	rows, err := database.DB.Query("SELECT "+targetCols+" FROM backup_targets WHERE enabled=1 AND next_run_at IS NOT NULL AND next_run_at <= ? ORDER BY next_run_at", nowRFC)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupTarget
	for rows.Next() {
		t, err := scanTarget(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListEnabledTargetIDs 返回所有启用目标的 id（调度器初始化 next_run_at 用）。
func ListEnabledTargetIDs() ([]int64, error) {
	rows, err := database.DB.Query("SELECT id FROM backup_targets WHERE enabled=1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// ---- 恢复收尾（SESSION_REVOKE 幂等）----

// MarkRestoreFinalized 尝试登记一次恢复收尾；返回 true 表示"本次是首次"（应执行会话吊销），
// false 表示"已收尾过"（幂等跳过）。用主键 INSERT ... 判冲突实现单次语义。
func MarkRestoreFinalized(restoreID string) (bool, error) {
	res, err := database.DB.Exec(`INSERT INTO restore_finalizations(restore_id) VALUES(?) ON CONFLICT(restore_id) DO NOTHING`, restoreID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RevokeAllSessions 全局吊销：所有用户 token_version+1、清空 refresh_jti、撤销全部活跃
// refresh 会话。用于全量恢复成功后（旧 .jwt_secret 下的令牌一律失效）。
func RevokeAllSessions() error {
	if _, err := database.DB.Exec("UPDATE users SET token_version = token_version + 1, refresh_jti = ''"); err != nil {
		return err
	}
	if _, err := database.DB.Exec("DELETE FROM refresh_sessions"); err != nil {
		return err
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- helpers ----

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

var _ = logger.Error // 预留：部分写操作可能需记录，保持与既有 model 风格一致
