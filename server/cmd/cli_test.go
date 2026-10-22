package cmd

import (
	"bufio"
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recover-admin 二次确认（v18.8，P1-8）：三种输入路径
func TestConfirmAdminIdentity(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		username string
		want     bool
	}{
		{"用户名一致放行", "alice\n", "alice", true},
		{"用户名不一致中止", "bob\n", "alice", false},
		{"带空白修剪后一致放行", "  alice  \n", "alice", true},
		{"空输入中止", "\n", "alice", false},
		{"EOF（无换行直接结束）按不一致处理", "", "alice", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got := confirmAdminIdentity(bufio.NewReader(strings.NewReader(tc.input)), tc.username, &out)
			if got != tc.want {
				t.Errorf("confirmAdminIdentity(%q, %q) = %v, want %v", tc.input, tc.username, got, tc.want)
			}
			if !tc.want && !strings.Contains(out.String(), "aborted") && tc.input != "" {
				t.Errorf("中止时缺少 aborted 提示: %q", out.String())
			}
		})
	}
}

// backup-db 一致性快照（v21.95 H-03'）：建库写数据 → backup-db → 目标文件可打开且含数据
func TestBackupDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// 与 backup-db 生产路径一致：直接打开库建表写数据（不 Init、不迁移）
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS backup_probe (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO backup_probe(v) VALUES ('h03-probe-2026')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	raw.Close()

	target := filepath.Join(dir, "snapshot.db")
	backupDB(dbPath, []string{target})
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("backup 目标未生成: %v", err)
	}

	// 用独立连接打开备份文件验证数据完整
	probe, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer probe.Close()
	var v string
	if err := probe.QueryRow("SELECT v FROM backup_probe WHERE id = 1").Scan(&v); err != nil {
		t.Fatalf("query backup: %v", err)
	}
	if v != "h03-probe-2026" {
		t.Errorf("备份数据不符: got %q", v)
	}

	// 幂等：目标已存在时再备份应覆盖成功
	backupDB(dbPath, []string{target})
	if _, err := os.Stat(target); err != nil {
		t.Errorf("覆盖备份后目标丢失: %v", err)
	}

	// 缺参：应打印 Usage 且不崩溃
	backupDB(dbPath, nil)
}
