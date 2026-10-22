package backup

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// SnapshotTo 对活库 srcDBPath 用 VACUUM INTO 产出一致性单文件快照到 dstPath。
//
// 关键约束（v4.3-final 审核 GPT/Claude）：
//   - **另开独立 *sql.DB 连接**，不占用 database.DB 的 MaxOpenConns=2 池
//     （VACUUM INTO 是较长读事务，占池会饿死笔记保存）。
//   - 源库以 WAL 打开：VACUUM INTO 由 SQLite 事务语义产出含"已提交未 checkpoint 的
//     WAL 内容"的一致快照，不阻塞活库读写（busy_timeout 兜并发写锁）。
//   - 完成后对快照跑 PRAGMA integrity_check（SQLite 官方明确 VACUUM INTO 异常关机
//     可能产出不完整文件 → 必须校验后才进打包）。
//
// 复用 cmd/cli.go backupDB 的 VACUUM INTO 语义，抽成引擎内可调用函数（避免两套快照逻辑）。
func SnapshotTo(dstPath, srcDBPath string) error {
	if dstPath == "" {
		return fmt.Errorf("snapshot: empty dst")
	}
	if _, err := os.Stat(srcDBPath); err != nil {
		return fmt.Errorf("snapshot: source db unavailable: %w", err)
	}
	// VACUUM INTO 要求目标不存在
	if err := os.MkdirAll(filepath.Dir(dstPath), 0700); err != nil {
		return fmt.Errorf("snapshot: mkdir dst dir: %w", err)
	}
	if _, err := os.Stat(dstPath); err == nil {
		if err := os.Remove(dstPath); err != nil {
			return fmt.Errorf("snapshot: remove existing dst: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("snapshot: stat dst: %w", err)
	}

	db, err := sql.Open("sqlite", srcDBPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return fmt.Errorf("snapshot: open source: %w", err)
	}
	defer db.Close()

	escaped := strings.ReplaceAll(dstPath, "'", "''") // 防路径注入到 SQL 字面量
	if _, err := db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
		return fmt.Errorf("snapshot: VACUUM INTO: %w", err)
	}
	return IntegrityCheck(dstPath)
}

// IntegrityCheck 对单文件库跑 PRAGMA integrity_check，非 "ok" 返回 ErrDatabaseCorrupted。
func IntegrityCheck(dbPath string) error {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("integrity: open: %w", err)
	}
	defer db.Close()
	var res string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&res); err != nil {
		return fmt.Errorf("integrity: query: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("%w: %s", ErrDatabaseCorrupted, res)
	}
	return nil
}
