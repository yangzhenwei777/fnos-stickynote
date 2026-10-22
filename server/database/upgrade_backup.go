package database

import "fmt"

// ensureBackupTables v1.0.167 系统备份功能建表（幂等 CREATE TABLE IF NOT EXISTS，
// 每次启动执行，新老库均可到位——沿用本项目"新表走 ensureSchema 幂等补丁"的既有约定）。
//
// 模型遵循 v4.3-final A4：
//
//	backup_snapshots   一份 .snbak 快照本身（**只存本地态**，不存远端态）
//	backup_placements  snapshot × target 的上传/验证状态（远端态在此，支持一快照多目标）
//	backup_jobs        一次执行行为（备份或恢复）
//	backup_targets     备份目标（WebDAV 凭据 secret_encrypted 用主密钥子密钥加密后存此）
//	restore_finalizations SESSION_REVOKE 幂等键（restore_id 主键；slice 3 恢复事务用，先建表）
//
// ⚠ 新增表名必须同步登记到 columnExists 白名单（upgrade_ensure.go），否则
//
//	TestColumnExists_AllTablesWhitelisted 遍历 sqlite_master 会报红。
func ensureBackupTables() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS backup_targets (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			name               TEXT NOT NULL,
			type               TEXT NOT NULL DEFAULT 'webdav',
			base_url           TEXT NOT NULL DEFAULT '',
			username           TEXT NOT NULL DEFAULT '',
			secret_encrypted   TEXT NOT NULL DEFAULT '',
			remote_path        TEXT NOT NULL DEFAULT '/StickyNote-Backup/',
			enabled            INTEGER NOT NULL DEFAULT 0,
			retention_count    INTEGER NOT NULL DEFAULT 7,
			schedule_interval  TEXT NOT NULL DEFAULT '24h',
			next_run_at        DATETIME,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			allow_loopback     INTEGER NOT NULL DEFAULT 0,
			allow_insecure_tls INTEGER NOT NULL DEFAULT 0,
			custom_ca_pem      TEXT NOT NULL DEFAULT '',
			last_success_at    DATETIME,
			last_error         TEXT NOT NULL DEFAULT '',
			created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS backup_snapshots (
			backup_id        TEXT PRIMARY KEY,
			created_at       TEXT NOT NULL,
			trigger_kind     TEXT NOT NULL DEFAULT 'manual',
			archive_sha256   TEXT NOT NULL DEFAULT '',
			size_bytes       INTEGER NOT NULL DEFAULT 0,
			app_version      TEXT NOT NULL DEFAULT '',
			schema_version   INTEGER NOT NULL DEFAULT 0,
			note_count       INTEGER NOT NULL DEFAULT 0,
			user_count       INTEGER NOT NULL DEFAULT 0,
			attachment_count INTEGER NOT NULL DEFAULT 0,
			status           TEXT NOT NULL DEFAULT 'LOCAL_CREATED', -- LOCAL_CREATED|LOCAL_VERIFIED|INCOMPLETE|FAILED
			incomplete       INTEGER NOT NULL DEFAULT 0,
			created_at_ts    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS backup_placements (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			backup_id    TEXT NOT NULL,
			target_id    INTEGER NOT NULL,
			status       TEXT NOT NULL DEFAULT 'UPLOADING', -- UPLOADING|UPLOADED|VERIFIED|FAILED
			remote_path  TEXT NOT NULL DEFAULT '',
			error        TEXT NOT NULL DEFAULT '',
			uploaded_at  DATETIME,
			verified_at  DATETIME,
			created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(backup_id, target_id),
			FOREIGN KEY (backup_id) REFERENCES backup_snapshots(backup_id) ON DELETE CASCADE,
			FOREIGN KEY (target_id) REFERENCES backup_targets(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS backup_jobs (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			type         TEXT NOT NULL,             -- backup | restore
			target_id    INTEGER,
			backup_id    TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL,             -- STARTING|RUNNING|SUCCESS|FAILED|INTERRUPTED
			phase        TEXT NOT NULL DEFAULT '',
			retry_count  INTEGER NOT NULL DEFAULT 0,
			error_code   TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			started_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			finished_at  DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS restore_finalizations (
			restore_id   TEXT PRIMARY KEY,
			completed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_backup_placements_backup ON backup_placements(backup_id)`,
		`CREATE INDEX IF NOT EXISTS idx_backup_jobs_started ON backup_jobs(started_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := DB.Exec(q); err != nil {
			return fmt.Errorf("backup ddl: %w", err)
		}
	}
	return nil
}
