package database

import (
	"path/filepath"
	"testing"
)

// ============================================================================
// columnExists 白名单遍历式防呆测试（v21.126 P3 修复）
//
// 背景：v21.54 引入 columnExists 的表名白名单以消除 PRAGMA 拼接的注入面。
// 但白名单是"手工维护"的——每次新增表都需要同步登记，否则未来给新表加列
// 的迁移会命中 fail-closed 直接报错。v21.125 之前 note_folders 与
// refresh_sessions 已漏登，v21.126 补齐。
//
// 本用例把"防漏"变成可执行断言：遍历 sqlite_master 里所有 type='table'
// 条目（包括 sqlite_sequence、sqlite_stat1 等 SQLite 内部表也一并检查——
// 若它们被调用会走 PRAGMA 分支，同样需要被允许或显式排除）。
//
// 修复前策略：白名单必须覆盖所有本项目 DDL 建出的表；SQLite 内部表不在
// 迁移代码调用 columnExists 的路径上，测试显式跳过。
// ============================================================================

func TestColumnExists_AllTablesWhitelisted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := Init(dbPath, "1.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer func() {
		DB.Close()
		DB = nil
	}()

	// 列出所有本项目 DDL 建出的表（排除 sqlite_% 内部表，它们不在 columnExists 调用路径上）
	rows, err := DB.Query(
		"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables found after migration — Init may be broken")
	}

	t.Logf("discovered %d tables: %v", len(tables), tables)

	// 显式豁免清单：以下表**永远不会**被迁移代码用 columnExists 检查列，
	// 因此不需要出现在 allowedTables 白名单里。若将来给它们加列，需同步
	// 更新本清单与 allowedTables。
	//   - FTS5 影子表：SQLite 内部管理的虚拟表及其 config/data/docsize/idx 附属
	//   - upgrade_records：迁移元表，schema 恒定（id / version / applied_at）
	skipTables := map[string]bool{
		"notes_fts":         true, // FTS5 外部内容表
		"notes_fts_config":  true, // FTS5 影子表
		"notes_fts_data":    true, // FTS5 影子表
		"notes_fts_docsize": true, // FTS5 影子表
		"notes_fts_idx":     true, // FTS5 影子表
		"upgrade_records":   true, // 迁移元表
	}

	// 对每张表调 columnExists，任意一张返回 "invalid table for PRAGMA" 即白名单缺漏。
	// 用一个不存在的列名（如 __definitely_missing__）不影响判定——我们只关心
	// "是否被白名单拒"而不是"列是否存在"。
	for _, tbl := range tables {
		if skipTables[tbl] {
			continue
		}
		_, err := columnExists(tbl, "__definitely_missing__")
		if err != nil && strings_contains(err.Error(), "invalid table for PRAGMA") {
			t.Errorf("columnExists 白名单缺漏：表 %q 未登记（错误：%v）；"+
				"请到 server/database/upgrade_ensure.go allowedTables 里补充，"+
				"或确认本表不应参与迁移加列（则在 skipTables 里显式跳过）", tbl, err)
		}
		// 其他错误（如 PRAGMA 本身失败）不在本测试关心范围，跳过
	}
}

// TestColumnExists_RejectsUnknownTable 防呆反向：白名单外的表名必须被拒
// （证明白名单机制仍然生效，v21.54 的注入面收敛不被回退）。
func TestColumnExists_RejectsUnknownTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := Init(dbPath, "1.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer func() {
		DB.Close()
		DB = nil
	}()

	_, err := columnExists("notepad_evil_table; DROP TABLE users", "x")
	if err == nil {
		t.Fatal("期望白名单拒绝未知表名，但返回 nil")
	}
	if !strings_contains(err.Error(), "invalid table for PRAGMA") {
		t.Errorf("期望白名单拒绝，得到其他错误：%v", err)
	}
}

// strings_contains 是 strings.Contains 的本地包装，避免在测试文件里 import strings
// 造成阅读干扰（database 包的其他测试也遵循同样风格）。
func strings_contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
