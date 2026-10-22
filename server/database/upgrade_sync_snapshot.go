package database

import "fmt"

// ensureSyncSnapshotTables 批次 5 · P5（v20 §73/§75/§46~48）：Bootstrap 冻结快照两表。
//
//	sync_snapshots        快照生命周期账本：PENDING→BUILDING→READY→FAILED/EXPIRED/INVALIDATED。
//	                      epoch/watermark 在构建时锚定（watermark 取 VACUUM 之后读数，见 sync/snapshot.go
//	                      的一致性论证）；sha256 对最终 zip（sealed 文件）计算（F1 纪律同款）。
//	sync_snapshots_hold   附件 Hold（§73）：READY/BUILDING 快照引用的 uploads 文件名，GC 三清理
//	                      （staged/orphan/pending）见到即跳过；快照过期/作废删行即释放。
//	                      行随 snapshot 级联删（FK DELETE CASCADE）；文件本体由扫描器统一回收。
//
// user_id FK→users CASCADE：删用户时快照行级联消失，hold 随 snapshot 级联释放，
// 其附件回归正常 GC 路径（用户数据本就被删，无泄漏面）。
//
// ⚠ 新表名已同步登记 tableExists 白名单（upgrade_ensure.go）。
func ensureSyncSnapshotTables() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sync_snapshots (
			id           TEXT PRIMARY KEY,                -- snap-<hex16>
			user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			status       TEXT NOT NULL DEFAULT 'PENDING', -- PENDING|BUILDING|READY|FAILED|EXPIRED|INVALIDATED
			server_epoch INTEGER NOT NULL DEFAULT 0,
			watermark    INTEGER NOT NULL DEFAULT 0,
			instance_id  TEXT NOT NULL DEFAULT '',
			sha256       TEXT NOT NULL DEFAULT '',
			file_path    TEXT NOT NULL DEFAULT '',
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			file_count   INTEGER NOT NULL DEFAULT 0,
			error        TEXT NOT NULL DEFAULT '',
			created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			built_at     DATETIME NOT NULL DEFAULT '',
			expires_at   DATETIME NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_snapshots_user ON sync_snapshots(user_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_snapshots_expiry ON sync_snapshots(status, expires_at)`,
		`CREATE TABLE IF NOT EXISTS sync_snapshots_hold (
			snapshot_id TEXT NOT NULL REFERENCES sync_snapshots(id) ON DELETE CASCADE,
			filename    TEXT NOT NULL,
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (snapshot_id, filename)
		)`,
	}
	for _, q := range stmts {
		if _, err := DB.Exec(q); err != nil {
			return fmt.Errorf("sync snapshot ddl: %w", err)
		}
	}
	return nil
}
