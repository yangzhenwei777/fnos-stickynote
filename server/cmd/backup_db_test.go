package cmd

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// v21.112 P1 回归（2026-09-07 深度测试真机复证）：
// backup-db 分支不得调用 config.Load——其副作用 os.MkdirAll(cfg.DataDir, 0700)
// （默认 ./data，相对 cwd）在 cwd 不可写时（fnOS upgrade_init 钩子 cwd 可能为 /）
// [FATAL] 退出，导致无 sqlite3 宿主机上升级前备份必败、升级被 fail-closed 误阻断。
// 本用例锁定：ExecuteCLI("backup-db", target, src) 在 cwd 下**不产生 data/ 目录**。
func TestExecuteCLIBackupDBNoDataDirSideEffect(t *testing.T) {
	tmp := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// 准备源库（隔离目录，不在 cwd ./data 下——模拟 upgrade_init 传入 TRIM_PKGVAR 路径）
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "notepad.db")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE notes(id INTEGER PRIMARY KEY, title TEXT); INSERT INTO notes(title) VALUES('升级前数据')"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	target := filepath.Join(srcDir, "snap.db")
	ExecuteCLI([]string{"backup-db", target, src})

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("快照未生成: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "data")); err == nil {
		t.Fatal("回归：backup-db 在 cwd 下创建了 ./data 目录（config.Load 副作用被重新引入）")
	}
	// 快照内容一致性
	sdb, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	var title string
	if err := sdb.QueryRow("SELECT title FROM notes LIMIT 1").Scan(&title); err != nil || title != "升级前数据" {
		t.Fatalf("快照数据不一致: title=%q err=%v", title, err)
	}
}

// backupDB 基本语义（v21.95 H-03' + v21.112 显式源路径）：VACUUM INTO 单文件一致性快照。
func TestBackupDBExplicitSourceSnapshot(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "old.db")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t VALUES (1),(2),(3)"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	target := filepath.Join(srcDir, "backup.db")
	// backupDB 签名（对齐 CLI：backup-db <target> [source]）：第一参=默认源库，args[0]=目标
	backupDB(src, []string{target})

	sdb, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	var n int
	if err := sdb.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 3 {
		t.Fatalf("快照行数 = %d, err = %v, want 3", n, err)
	}
}
