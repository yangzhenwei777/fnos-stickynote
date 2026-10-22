package database

import (
	"fmt"
	"strings"
)

// ensureSyncMetaTable v1.0.175 R0（离线方案 v20.1）：sync_meta 单例表。
// v1.0.175 P0-D 扩展：sync_changes / sync_operations 两表 + notes/folders.sync_id +
// folders.version（§17/§18/§23/§8；全程幂等，老库回填只命中空值）。
//
// 承载离线同步协议的服务端数据集代际与增量游标锚（v20 §21/§22）：
//
//	server_epoch         服务端数据集代际。整库恢复成功后 new=max(pre,restored)+1（§85），
//	                     客户端据此失效旧 cursor/change_seq，防止"恢复后旧游标被继续信任"（红线 10）。
//	current_change_seq   全局增量传播位置（P0-D 起由 sync push 原子递增，§24）。
//	protocol_version     同步协议版本（与 App/Schema/Backup 版本彻底分层，§155）。
//
// server_instance_id 不在此表——继续以 DataDir/.instance_id 文件为唯一权威（§22），二者职责不同（§91）。
//
// 单例行（id=1）+ CHECK 约束防多行；seed server_epoch=1（新装即有确定起点；
// 老库升级同样落 1，历史归档 manifest 无该字段 → restored_epoch=0 → new=max(pre,0)+1 单调不减）。
//
// ⚠ 新表名已同步登记 columnExists 白名单（upgrade_ensure.go）。
func ensureSyncMetaTable() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sync_meta (
			id                 INTEGER PRIMARY KEY CHECK (id = 1),
			server_epoch       INTEGER NOT NULL DEFAULT 1,
			current_change_seq INTEGER NOT NULL DEFAULT 0,
			protocol_version   INTEGER NOT NULL DEFAULT 1,
			updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT OR IGNORE INTO sync_meta (id, server_epoch) VALUES (1, 1)`,
		// §23 Change Log：不可变增量事件流；(user_id, change_seq) 索引服务 Pull（§25）。
		`CREATE TABLE IF NOT EXISTS sync_changes (
			change_seq      INTEGER NOT NULL,
			user_id         INTEGER NOT NULL,
			entity_type     TEXT NOT NULL,           -- NOTE | FOLDER | FOLDER_ORDER（后两者 P1）
			entity_sync_id  TEXT,                    -- 集合级 op 可为 NULL（§23）
			operation       TEXT NOT NULL,           -- CREATE | UPDATE | DELETE | RESTORE
			server_version  INTEGER NOT NULL DEFAULT 0,
			payload_kind    TEXT NOT NULL DEFAULT 'json', -- json | blob_ref（§32 P1）
			payload_json    TEXT NOT NULL DEFAULT '',
			payload_ref     TEXT NOT NULL DEFAULT '',
			payload_size    INTEGER NOT NULL DEFAULT 0,   -- blob_ref 通道载荷字节数（§32 P1-b）
			device_id       TEXT NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (change_seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_changes_user_seq ON sync_changes(user_id, change_seq)`,
		// §8 op_id 幂等表：request_sha256 双键判重（同 op 同 hash 重放结果、异 hash 报
		// OP_ID_REUSE_ERROR）。ADR-004 R5/§27：V1 长期保留不 prune。
		`CREATE TABLE IF NOT EXISTS sync_operations (
			user_id        INTEGER NOT NULL,
			op_id          TEXT NOT NULL,
			request_sha256 TEXT NOT NULL,
			result_code    TEXT NOT NULL,            -- ACKED | CONFLICT | IDEMPOTENT_REPLAY | ENTITY_GONE ...
			result_version INTEGER NOT NULL DEFAULT 0,
			result_payload TEXT NOT NULL DEFAULT '',
			created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, op_id)
		)`,
	}
	for _, q := range stmts {
		if _, err := DB.Exec(q); err != nil {
			return fmt.Errorf("sync ddl: %w", err)
		}
	}
	// §17/§18：实体跨设备身份列（可空 + 部分唯一索引；重复列错误按幂等吞掉）。
	alters := []string{
		`ALTER TABLE notes ADD COLUMN sync_id TEXT`,
		`ALTER TABLE note_folders ADD COLUMN sync_id TEXT`,
		`ALTER TABLE note_folders ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,
		// §32 P1-b：blob_ref 大载荷通道的载荷字节数（老库幂等补列）。
		`ALTER TABLE sync_changes ADD COLUMN payload_size INTEGER NOT NULL DEFAULT 0`,
	}
	for _, q := range alters {
		if _, err := DB.Exec(q); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("sync idempotent alter: %w", err)
			}
		}
	}
	// 一次性回填：仅空值行（重启后 WHERE 命中 0 行，零开销）。
	backfills := []string{
		`UPDATE notes SET sync_id = lower(hex(randomblob(16))) WHERE sync_id IS NULL OR sync_id = ''`,
		`UPDATE note_folders SET sync_id = lower(hex(randomblob(16))) WHERE sync_id IS NULL OR sync_id = ''`,
	}
	for _, q := range backfills {
		if _, err := DB.Exec(q); err != nil {
			return fmt.Errorf("sync_id backfill: %w", err)
		}
	}
	idx := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_notes_sync_id ON notes(sync_id) WHERE sync_id IS NOT NULL AND sync_id <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_folders_sync_id ON note_folders(sync_id) WHERE sync_id IS NOT NULL AND sync_id <> ''`,
	}
	for _, q := range idx {
		if _, err := DB.Exec(q); err != nil {
			return fmt.Errorf("sync_id index: %w", err)
		}
	}
	return nil
}
