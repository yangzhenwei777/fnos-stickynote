//go:build windows

package main

// contract_bindings_windows_test.go —— 双端契约回归钉（开发纪律 A-15，2026-10-30）。
//
// 背景：Wails 绑定与 server/handler 是两份手写契约；2.3.0 交付后复扫实锤三处漂移
// （PUT /notes 缺 updated_at、restore 缺 folder_locked、move 缺 version）。
// 本文件钉住"binding 必须能给 LocalTransport 提供 handler 同形数据"这一前提。
// 与 web/src/__tests__/localTransport.test.js 互为表里：那边钉形状，这边钉数据源。

import (
	"context"
	"path/filepath"
	"testing"
)

// TestExportNotesZipAllReportsFolderSkipped —— 用户报障回归（2026-10-30）：
// 桌面「导出全部」必须把锁定文件夹里的笔记数（folders_skipped）如实返回给 UI，
// 否则提示只剩「跳过 N 篇加密笔记」，缺「N 条加密文件夹中的笔记」。
func TestExportNotesZipAllReportsFolderSkipped(t *testing.T) {
	ls, _ := bootWithLogin(t)
	ls.ctx = context.Background()
	f, err := ls.CreateFolder("锁定夹")
	if err != nil || f == nil || f.ID == nil {
		t.Fatalf("create folder: %v", err)
	}
	if _, err := ls.CreateNote("夹内", "<p>a</p>", "", f.ID); err != nil {
		t.Fatalf("create note: %v", err)
	}
	if _, err := ls.CreateNote("夹外", "<p>b</p>", "", nil); err != nil {
		t.Fatalf("create note: %v", err)
	}
	if _, err := ls.EncryptFolder(*f.ID, "folderpw123", "folderpw123"); err != nil {
		t.Fatalf("encrypt folder: %v", err)
	}
	out := filepath.Join(t.TempDir(), "all.zip")
	stubSaveDialog(t, out, nil)

	res, err := ls.ExportNotesZip(nil, true)
	if err != nil {
		t.Fatalf("ExportNotesZip: %v", err)
	}
	if res["exported"] != 1 {
		t.Fatalf("exported = %v, want 1（仅夹外一篇）", res["exported"])
	}
	if res["folders_skipped"] != 1 {
		t.Fatalf("folders_skipped = %v, want 1（锁定夹笔记数，UI 文案依赖它）", res["folders_skipped"])
	}
	entries := readZipEntries(t, out)
	if _, ok := entries["夹外.md"]; !ok {
		t.Fatalf("缺少条目 夹外.md（实际: %v）", entries)
	}
	if _, ok := entries["夹内.md"]; ok {
		t.Fatalf("锁定夹笔记不得导出（实际: %v）", entries)
	}
}

// TestMoveNoteReturnsVersion —— LocalTransport 复刻 handler 的 {message, version} 需要本地版本。
func TestMoveNoteReturnsVersion(t *testing.T) {
	ls, _ := bootWithLogin(t)
	f, err := ls.CreateFolder("契约-移动")
	if err != nil || f == nil || f.ID == nil {
		t.Fatalf("create folder: %v", err)
	}
	n, err := ls.CreateNote("移动契约", "<p>x</p>", "", nil)
	if err != nil || n == nil {
		t.Fatalf("create note: %v", err)
	}
	version, err := ls.MoveNote(n.ID, f.ID)
	if err != nil {
		t.Fatalf("MoveNote: %v", err)
	}
	if version <= 0 {
		t.Fatalf("MoveNote 必须返回本地版本（供 handler 同形响应），got %d", version)
	}
}

// TestRestoreNoteReportsFolderLocked —— 恢复到锁定文件夹必须带 folder_locked=true，
// 解锁后同场景为 false（对应 TrashView 的"该文件夹已加密，解锁后查看"提示）。
func TestRestoreNoteReportsFolderLocked(t *testing.T) {
	ls, _ := bootWithLogin(t)
	f, err := ls.CreateFolder("契约-恢复")
	if err != nil || f == nil || f.ID == nil {
		t.Fatalf("create folder: %v", err)
	}
	// 锁定态下不允许新建，故先建笔记再加锁
	nLocked, err := ls.CreateNote("锁定恢复", "<p>a</p>", "", f.ID)
	if err != nil || nLocked == nil {
		t.Fatalf("create note: %v", err)
	}
	nGranted, err := ls.CreateNote("解锁恢复", "<p>b</p>", "", f.ID)
	if err != nil || nGranted == nil {
		t.Fatalf("create note: %v", err)
	}
	if _, err := ls.EncryptFolder(*f.ID, "folderpw123", "folderpw123"); err != nil {
		t.Fatalf("encrypt folder: %v", err)
	}
	if err := ls.DeleteNote(nLocked.ID); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	res, err := ls.RestoreNote(nLocked.ID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res["folder_locked"] != true {
		t.Fatalf("恢复到锁定文件夹必须 folder_locked=true，got %#v", res)
	}

	// 解锁后同场景应为 false
	if _, err := ls.UnlockFolder(*f.ID, "folderpw123"); err != nil {
		t.Fatalf("unlock folder: %v", err)
	}
	if err := ls.DeleteNote(nGranted.ID); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	res2, err := ls.RestoreNote(nGranted.ID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res2["folder_locked"] != false {
		t.Fatalf("解锁后 folder_locked 必须为 false，got %#v", res2)
	}
}

// TestGetPublicConfigDefaultsTrashRetention —— 与 handler/config.go 同形：键未落库补 "0"。
func TestGetPublicConfigDefaultsTrashRetention(t *testing.T) {
	ls, _ := bootWithLogin(t)
	cfg, err := ls.GetPublicConfig()
	if err != nil {
		t.Fatalf("GetPublicConfig: %v", err)
	}
	if _, ok := cfg["trash_retention_days"]; !ok {
		t.Fatalf("public config 必须补 trash_retention_days 默认值，got %#v", cfg)
	}
}
