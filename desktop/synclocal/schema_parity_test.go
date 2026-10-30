package synclocal

import (
	"database/sql"
	"path/filepath"
	"testing"

	"stickynote/database"
)

// 批次 7 发布工程 · DDL 同构校验门禁（用户点名列入）。
//
// 背景：批次 3/6 两轮教训——桌面集成工装用最小 DDL，与生产迁移产物不同构
// （uploads 缺 sha256 列、note_uploads 缺 FK），把两个真缺陷掩盖到真机才暴露
// （批次 6 §148 走查坐实的 pull-FK-崩即第二起）。本测试把"生产 schema 必须覆盖
// 桌面同步面全部引用列"变成 CI 常驻门禁：生产列漂移（改名/删除）或工装缺列都直接红。
//
// 口径：不要求全等（工装刻意最小化，users 行仅 §98 FK 占位），要求
// **引用列集合 ⊆ 生产  工装**——两侧同时钉。

// requiredCols = 桌面同步执行面（worker/synclocal/bootstrap carryEntityRow/portable）
// 以列名显式引用的集合。改动这些 SQL 时必须同步登记此处（漂移即红，防"改代码忘改工装"）。
var requiredCols = map[string][]string{
	"notes": {"id", "user_id", "title", "content", "content_text", "tags", "is_encrypted",
		"encrypt_password_hash", "encrypted_content", "created_at", "updated_at", "deleted_at",
		"version", "folder_id", "sync_id", "trash_upload_bytes"},
	"note_folders": {"id", "user_id", "name", "sort_order", "created_at", "sync_id", "version", "is_encrypted", "encrypt_password_hash"},
	"uploads":      {"filename", "user_id", "size", "status", "sha256"},
	"note_uploads": {"note_id", "filename"},
	"note_tags":    {"note_id", "tag"},
}

func TestProductionSchemaCoversSyncSurface(t *testing.T) {
	prodPath := filepath.Join(t.TempDir(), "prod.db")
	if err := database.Init(prodPath, "99.0.0"); err != nil {
		t.Fatalf("production migrations: %v", err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	prod, err := sql.Open("sqlite", "file:"+prodPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()

	hPath := filepath.Join(t.TempDir(), "harness.db")
	h, err := sql.Open("sqlite", hPath)
	if err != nil {
		t.Fatal(err)
	}
	h.SetMaxOpenConns(1)
	defer h.Close()
	if _, err := h.Exec(clientDDL); err != nil {
		t.Fatalf("harness ddl: %v", err)
	}
	if err := EnsureTables(h); err != nil {
		t.Fatal(err)
	}

	for table, cols := range requiredCols {
		p, err := columnSet(prod, table)
		if err != nil {
			t.Fatalf("prod %s: %v", table, err)
		}
		c, err := columnSet(h, table)
		if err != nil {
			t.Fatalf("harness %s: %v", table, err)
		}
		for _, col := range cols {
			if !p[col] {
				t.Errorf("生产漂移：migration 产物 %s 缺列 %s（桌面同步面在引用）", table, col)
			}
			if !c[col] {
				t.Errorf("工装失同构：clientDDL %s 缺列 %s（批次3 uploads.sha256 掩盖事故同款，必须补齐）", table, col)
			}
		}
	}

	// 生产 FK 在位断言：note_uploads→uploads 外键（v21.71 addNoteUploadsFK）是
	// pull 补桩修复（批次 6）成立的前提；若未来迁移重构掉它，此处先红、提示同步复核。
	rows, err := prod.Query(`SELECT "table" FROM pragma_foreign_key_list('note_uploads')`)
	if err != nil {
		t.Fatalf("fk list: %v", err)
	}
	defer rows.Close()
	var hasFK bool
	for rows.Next() {
		var parent string
		if rows.Scan(&parent) == nil && parent == "uploads" {
			hasFK = true
		}
	}
	if !hasFK {
		t.Error("生产漂移：note_uploads 不再引用 uploads（FK 被移除？）——复核 pull 补桩与 GC 计数面")
	}
}

func columnSet(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	if len(out) == 0 {
		return nil, sql.ErrNoRows
	}
	return out, rows.Err()
}

// 防呆：requiredCols 的表名拼写错误会静默漏检——列集合为空即报错。
func TestRequiredColsTablesExist(t *testing.T) {
	prodPath := filepath.Join(t.TempDir(), "prod2.db")
	if err := database.Init(prodPath, "99.0.0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	prod, err := sql.Open("sqlite", "file:"+prodPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	for table := range requiredCols {
		if _, err := columnSet(prod, table); err != nil {
			t.Errorf("requiredCols 引用了生产不存在的表 %s（拼写漂移？）", table)
		}
	}
}
