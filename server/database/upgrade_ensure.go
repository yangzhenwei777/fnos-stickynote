package database

import (
	"fmt"
	"strconv"
	"strings"

	"stickynote/logger"
)

// ensureFTS5 建立 FTS5 全文索引（v17）：
// 外部内容表 notes_fts（trigram 分词，索引 title + content_text）
// + INSERT/DELETE/UPDATE 三个同步触发器 + 存量数据 rebuild。
// trigram 对 ≥3 字查询可命中中文子串；<3 字由 model 层回退 LIKE。
func ensureFTS5() error {
	ftsHas, err := tableExists("notes_fts")
	if err != nil {
		return err
	}
	if ftsHas {
		return nil
	}

	if _, err := DB.Exec(`CREATE VIRTUAL TABLE notes_fts USING fts5(title, content_text, content='notes', content_rowid='id', tokenize='trigram')`); err != nil {
		return fmt.Errorf("failed to create notes_fts: %w", err)
	}

	triggers := []string{
		`CREATE TRIGGER notes_fts_ai AFTER INSERT ON notes BEGIN
			INSERT INTO notes_fts(rowid, title, content_text) VALUES (new.id, new.title, new.content_text);
		END`,
		`CREATE TRIGGER notes_fts_ad AFTER DELETE ON notes BEGIN
			INSERT INTO notes_fts(notes_fts, rowid, title, content_text) VALUES('delete', old.id, old.title, old.content_text);
		END`,
		`CREATE TRIGGER notes_fts_au AFTER UPDATE ON notes BEGIN
			INSERT INTO notes_fts(notes_fts, rowid, title, content_text) VALUES('delete', old.id, old.title, old.content_text);
			INSERT INTO notes_fts(rowid, title, content_text) VALUES (new.id, new.title, new.content_text);
		END`,
	}
	for _, q := range triggers {
		if _, err := DB.Exec(q); err != nil {
			return fmt.Errorf("failed to create FTS5 trigger: %w", err)
		}
	}

	// 一次性索引存量数据（外部内容表 rebuild 会重读 notes 全表）
	if _, err := DB.Exec(`INSERT INTO notes_fts(notes_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("failed to rebuild notes_fts: %w", err)
	}
	logger.Info("FTS5 full-text index initialized (trigram, external content)")
	return nil
}

// v20.8 管理员操作日志表
func ensureAdminLogsTable() error {
	has, err := tableExists("admin_logs")
	if err != nil {
		return err
	}
	if !has {
		_, err := DB.Exec(`CREATE TABLE admin_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			ip TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP DEFAULT (datetime('now','localtime'))
		)`)
		if err != nil {
			return fmt.Errorf("failed to create admin_logs: %w", err)
		}
		if _, err := DB.Exec("CREATE INDEX idx_admin_logs_created ON admin_logs(created_at)"); err != nil {
			logger.Error("admin_logs index creation failed: %v", err) // v21.58 F-16：错误不再忽略
		}
		logger.Info("Schema: admin_logs table created")
	}
	return nil
}

// ensureLoginFailsTable 登录防爆破持久化表（v20.8 审计优化 P2）。
func ensureLoginFailsTable() error {
	has, err := tableExists("login_fails")
	if err != nil {
		return err
	}
	if !has {
		_, err := DB.Exec(`CREATE TABLE login_fails (
			key TEXT PRIMARY KEY,
			count INTEGER NOT NULL DEFAULT 0,
			window_start INTEGER NOT NULL DEFAULT 0,
			locked_until INTEGER NOT NULL DEFAULT 0
		)`)
		if err != nil {
			return fmt.Errorf("failed to create login_fails: %w", err)
		}
		logger.Info("Schema: login_fails table created")
	}
	return nil
}

func tableExists(table string) (bool, error) {
	var count int
	err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count)
	return count > 0, err
}

func columnExists(table, column string) (bool, error) {
	// v21.54 P2 审计加固：PRAGMA table_info 拼接加表名白名单（全部内部迁移固定表名），
	// 消除"理论上可注入"的拼接受面（此前审计记录为极低危，本版顺手闭环）。
	// v21.126 P3 修复：补齐 note_folders / refresh_sessions——两张表实际存在于
	// schema 但白名单未列，未来任何对它们加列的迁移会命中 fail-closed 报错。
	// 新表在 upgrade.go/upgrade_migrations.go 落地后必须同步登记到此白名单
	// （由 TestColumnExists_AllTablesWhitelisted 遍历 sqlite_master 断言防漏，
	// 见 server/database/upgrade_ensure_test.go）。
	allowedTables := map[string]bool{
		"notes": true, "users": true, "note_tags": true, "uploads": true,
		"configs": true, "login_fails": true, "admin_logs": true, "totp_recovery": true,
		"note_uploads": true, "pending_upload_cleanup": true,
		"note_folders": true, "refresh_sessions": true,
		// v1.0.167 系统备份功能新增表（须与 ensureBackupTables DDL 同步登记）
		"backup_targets": true, "backup_snapshots": true, "backup_placements": true,
		"backup_jobs": true, "restore_finalizations": true,
		// v1.0.175 R0/P0-D 离线同步表（与 ensureSyncMetaTable DDL 同步登记）
		"sync_meta": true, "sync_changes": true, "sync_operations": true,
		// 批次5 P5 Bootstrap 快照表（与 ensureSyncSnapshotTables DDL 同步登记）
		"sync_snapshots": true, "sync_snapshots_hold": true,
	}
	if !allowedTables[table] {
		return false, fmt.Errorf("invalid table for PRAGMA: %q", table)
	}
	rows, err := DB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// compareVersions 比较两个 "X.Y.Z" 格式的版本号
// 返回: -1 (a<b), 0 (a==b), 1 (a>b)
func compareVersions(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")

	maxLen := len(pa)
	if len(pb) > maxLen {
		maxLen = len(pb)
	}

	for i := 0; i < maxLen; i++ {
		var va, vb int
		if i < len(pa) {
			va, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			vb, _ = strconv.Atoi(pb[i])
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}
