package localstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // 桌面自带驱动注册，不经服务端 database 包的全局句柄
)

// desktopDSN：§93 Desktop 档位 —— WAL + synchronous=FULL（主数据 durability 优先，
// 与 Server 的 NORMAL 有意不同且**不得为桌面改 Server**）+ foreign_keys + busy_timeout
// + _txlock=immediate（与服务端同款的 BEGIN IMMEDIATE 纪律）。
const desktopDSN = "file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)&_pragma=cache_size(10000)&_txlock=immediate"

// OpenGenDB 打开一个 Generation 库并按 §94 **逐物理连接**回读验证 PRAGMA。
// MaxOpenConns=2（§93）；验证循环取满 2 条连接后即归还，覆盖整个池的稳态。
func OpenGenDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf(desktopDSN, dbPath))
	if err != nil {
		return nil, fmt.Errorf("open gen db: %w", err)
	}
	db.SetMaxOpenConns(2)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping gen db: %w", err)
	}
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("acquire conn %d: %w", i, err)
		}
		if err := verifyConnPragmas(conn); err != nil {
			conn.Close()
			db.Close()
			return nil, err
		}
		conn.Close()
	}
	return db, nil
}

func verifyConnPragmas(conn *sql.Conn) error {
	ctx := context.Background()
	var jm string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&jm); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if strings.ToLower(jm) != "wal" {
		return fmt.Errorf("journal_mode=%q want wal", jm)
	}
	var sync, fk, busy int
	if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		return err
	}
	if sync != 2 { // 2 = FULL
		return fmt.Errorf("synchronous=%d want 2(FULL)", sync)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return err
	}
	if fk != 1 {
		return fmt.Errorf("foreign_keys=%d want 1", fk)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		return err
	}
	if busy != 5000 {
		return fmt.Errorf("busy_timeout=%d want 5000", busy)
	}
	return nil
}

// QuickCheckOK 对已打开库做 quick_check（恢复收敛/激活后轻量完整性判定）。
func QuickCheckOK(db *sql.DB) error {
	var v string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&v); err != nil {
		return err
	}
	if v != "ok" {
		return fmt.Errorf("quick_check: %s", v)
	}
	return nil
}

// IntegrityCheckFile 对**封闭文件**（staging/sealed 副本）做全量 integrity_check。
func IntegrityCheckFile(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	var v string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&v); err != nil {
		return fmt.Errorf("integrity_check query: %w", err)
	}
	if v != "ok" {
		return fmt.Errorf("integrity_check: %s", v)
	}
	return nil
}
