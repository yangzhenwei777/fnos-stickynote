// 批次 4 · 刀 1：localstore 启动链集成测试（bootLocalstore 四态）。
// CI 暂不编译 desktop（批次 7 进矩阵）；本组测试为本地 Windows/门禁面 + 走查前自检。
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"stickynote/app"
	"stickynote/database"

	"stickynote-desktop/localstore"
)

// openAndCount 经全局句柄查笔记数（间接验证 SetDB 已接管）。
func openAndCount(t *testing.T) int {
	t.Helper()
	db := database.GetDB()
	if db == nil {
		t.Fatal("global DB not set")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notes WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count notes: %v", err)
	}
	return n
}

// assertDesktopTier 断言当前全局句柄连接为 Desktop 档（§93/§94：FULL，区别于服务端 NORMAL）。
func assertDesktopTier(t *testing.T) {
	t.Helper()
	conn, err := database.GetDB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var sync int
	if err := conn.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if sync != 2 {
		t.Fatalf("generation db must run Desktop tier synchronous=FULL(2), got %d", sync)
	}
}

func TestBootFromLegacySkeleton(t *testing.T) {
	dir := t.TempDir()
	// 造旧骨架库：database.Init 直开 + Web 语义写 2 条笔记（检验数据随迁完整性）。
	if err := database.Init(filepath.Join(dir, "notepad.db"), localstoreAppVersion); err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalUser(); err != nil {
		t.Fatal(err)
	}
	svc := app.NewServerNoteService(100000)
	for i := 0; i < 2; i++ {
		if _, err := svc.CreateNote(1, "legacy", "content", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	database.GetDB().Close() // 模拟进程重启（bootLocalstore 将再 Init/SetDB）

	gen, err := bootLocalstore(dir)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })

	l := localstore.Layout{ProfileDir: dir}
	if active, _ := l.Active(); active != gen || gen == "" {
		t.Fatalf("pointer=%q gen=%q want equal non-empty", active, gen)
	}
	if got := openAndCount(t); got != 2 {
		t.Fatalf("legacy notes must survive migration: got %d want 2", got)
	}
	assertDesktopTier(t)
	// 旧骨架库原位保留（灾备不删除）；代目录有 manifest。
	if b, err := os.ReadFile(filepath.Join(dir, "notepad.db")); err != nil || len(b) == 0 {
		t.Fatalf("legacy db must be kept in place: %v", err)
	}
	if b, err := os.ReadFile(l.GenManifest(gen)); err != nil || len(b) == 0 {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestBootFreshInstall(t *testing.T) {
	dir := t.TempDir()
	gen, err := bootLocalstore(dir)
	if err != nil {
		t.Fatalf("boot fresh: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })
	if gen == "" {
		t.Fatal("fresh install must provision a generation")
	}
	assertDesktopTier(t)
	// 全新库同样具备服务端全量 schema（sync 表由启动迁移产出）。
	var n int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM sync_changes`).Scan(&n); err != nil {
		t.Fatalf("sync_changes table must exist in provisioned schema: %v", err)
	}
}

func TestBootSecondRunOpensExistingGen(t *testing.T) {
	dir := t.TempDir()
	gen1, err := bootLocalstore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 写入一条笔记后经"重启"再开：数据在同代可见。
	svc := app.NewNoteService(100000) // 桌面本地构造器：不 bump 不产事件（离线批次纪律）
	if _, err := svc.CreateNote(1, "persist", "x", "", nil); err != nil {
		t.Fatal(err)
	}
	database.GetDB().Close()

	gen2, err := bootLocalstore(dir)
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })
	if gen1 != gen2 {
		t.Fatalf("second run must keep active generation %s, got %s", gen1, gen2)
	}
	if got := openAndCount(t); got != 1 {
		t.Fatalf("want 1 note, got %d", got)
	}
	assertDesktopTier(t)
}

func TestBootFallbackToLKG(t *testing.T) {
	dir := t.TempDir()
	gen1, err := bootLocalstore(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := localstore.Layout{ProfileDir: dir}
	// 手工激活第二代：从当前代库快照出 staging（VACUUM INTO 自带封闭单文件语义）。
	staging := filepath.Join(l.StagingDir(), "v2.db")
	if err := vacuumInto(t, database.GetDB(), staging); err != nil {
		t.Fatal(err)
	}
	database.GetDB().Close() // Activate 空间预检/复制不依赖旧句柄；先关连接避免 Windows 文件占用
	gen2, err := localstore.NewActivator(dir).Activate(staging, []byte(`{"provenance":"test-v2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gen2 == gen1 {
		t.Fatal("expected new generation id")
	}
	// 破坏当前代 → 重启应回落 lkg=gen1。
	if err := os.Remove(l.GenDB(gen2)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(l.GenDB(gen2) + "-wal"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	bootGen, err := bootLocalstore(dir)
	if err != nil {
		t.Fatalf("boot after gen loss: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })
	if bootGen != gen1 {
		t.Fatalf("must fallback to lkg %s, booted %s", gen1, bootGen)
	}
	assertDesktopTier(t)
}

func TestSetDBRefusesNil(t *testing.T) {
	if err := database.Init(filepath.Join(t.TempDir(), "x.db"), localstoreAppVersion); err != nil {
		t.Fatal(err)
	}
	before := database.GetDB()
	if old := database.SetDB(nil); old != nil {
		t.Fatal("SetDB(nil) must refuse and return no old handle")
	}
	if database.GetDB() != before {
		t.Fatal("SetDB(nil) must not touch current handle")
	}
	database.GetDB().Close()
}

func vacuumInto(t *testing.T, db *sql.DB, dst string) error {
	t.Helper()
	_, err := db.Exec("VACUUM INTO '" + filepath.ToSlash(dst) + "'")
	return err
}

// TestFreshProvisionSchemaComplete —— 2026-09-23 首装空库回归钉（E 组根因修复）。
// 全新 profile（无 legacy notepad.db）provision 后，激活代必须带完整 schema：
// 此前 database.Init 建表落在 WAL、gen0 主文件是 4096B 空壳被直接当 staging
// Activate → 激活出 0 表库 → ensureLocalUser "no such table: users" → 双击秒退。
// 修复 = 与 legacy 路径对称，SnapshotTo 产出含 WAL 内容的 sealed 快照再 Activate。
func TestFreshProvisionSchemaComplete(t *testing.T) {
	dir := t.TempDir() // 空档案：无 legacy notepad.db → 走 fresh 分支
	gen, err := bootLocalstore(dir)
	if err != nil {
		t.Fatalf("fresh boot: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })
	if gen == "" {
		t.Fatal("gen must be non-empty")
	}
	l := localstore.Layout{ProfileDir: dir}
	if active, _ := l.Active(); active != gen {
		t.Fatalf("pointer=%q gen=%q", active, gen)
	}
	// 激活代必须有 users 表（修复前这里是 0 表空壳）。
	var n int
	if err := database.GetDB().QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('users','notes','configs')`,
	).Scan(&n); err != nil {
		t.Fatalf("query activated schema: %v", err)
	}
	if n != 3 {
		t.Fatalf("fresh provision activated db must have users/notes/configs, got %d", n)
	}
	if err := ensureLocalUser(); err != nil {
		t.Fatalf("ensureLocalUser on fresh provision: %v", err)
	}
}
