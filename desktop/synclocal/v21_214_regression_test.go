package synclocal

// v21_214_regression_test.go —— 211c 实机两症状的回归钉。
//
// 211c 实机铁证：
//  ①`13:01:06 调度器未在预期时限内退出` 与 `13:01:06 新进程启动` 同秒，
//     紧接 `13:01:09 凭据被服务端拒绝（AUTH_REVOKED）`
//     ⇒ 根因：v21.213 的 `WithoutCancel` 让**在途换票跨越了换代**，
//       `SaveRefreshCAS` 写向已关闭的旧句柄 ⇒ 新票丢失（服务端已轮转作废旧票）。
//       修法（用户选定方案 C）：换代窗口前**先持换票锁**。
//  ② web 端移动笔记，桌面端无任何输出、永不同步
//     ⇒ 根因：`applyNoteChange` 的 folder 反查**失败时静默吞掉**，folderID 保持 nil
//       ⇒ UPDATE 把笔记写回「我的笔记」，用户的移动被**无声撤销**。
//
// 本文件钉住：
//  - TestResolveNoteFolderIDRejectsUnknownFolder（②核心）
//  - TestResolveNoteFolderIDNilMeansMyNotes（②语义边界）

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// applyTxForTest 建最小可用测试库并开启事务（resolveNoteFolderID 收 *sql.Tx，
// 与生产 applyNoteChange 的调用形态一致 —— 这样测试才真正覆盖"事务内反查"这条路径）。
// 口径与 pull_fk_test.go 一致：`sql.Open("sqlite", path)` + MaxOpenConns(1)。
func applyTxForTest(t *testing.T) *sql.Tx {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v214.db"))
	if err != nil {
		t.Skipf("sqlite 不可用，跳过：%v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE note_folders (id INTEGER PRIMARY KEY, user_id INTEGER, name TEXT,
		sort_order INTEGER, sync_id TEXT, version INTEGER DEFAULT 0,
		is_encrypted INTEGER DEFAULT 0, encrypt_password_hash TEXT DEFAULT '')`); err != nil {
		t.Skipf("建表失败，跳过：%v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Skipf("开事务失败，跳过：%v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// TestResolveNoteFolderIDRejectsUnknownFolder 定死 ②的核心修法：
// payload 带 folder_sync_id 但**本地查不到**时，必须**拒绝**，
// 绝不能静默返回 nil（那会把用户的移动撤销成「我的笔记」）。
//
// 真实复现路径：web 端把笔记从 A 移到 B，B 的 FOLDER change 若尚未落地
// （或本地漏收），NOTE change 就会带一个查不到的 folder_sync_id。
// 原实现 `if err == nil { folderID = &fid }` 把err 吞掉 ⇒ 笔记被写回默认文件夹，
// 用户视角就是「移动没有同步过去，而且没有任何报错」。
func TestResolveNoteFolderIDRejectsUnknownFolder(t *testing.T) {
	db := applyTxForTest(t)
	missing := "folder-sync-that-does-not-exist"
	p := payloadIn{FolderSyncID: &missing}

	got, err := resolveNoteFolderID(db, p)
	if err == nil {
		t.Fatalf("本地查不到 folder_sync_id=%s 时必须报错，实得 folderID=%v —— "+
			"静默返回 nil 会把用户的移动无声撤销为「我的笔记」", missing, got)
	}
	if got != nil {
		t.Fatalf("失败时不得返回 folderID（否则 UPDATE 会写回默认文件夹），实得 %v", got)
	}
}

// TestResolveNoteFolderIDNilMeansMyNotes 守住语义边界：
// payload **不带** folder_sync_id（nil 或空串）= 权威语义「我的笔记」，
// 必须**正常返回 nil 且不报错** —— 这是最常见的合法情形，不能被误伤。
func TestResolveNoteFolderIDNilMeansMyNotes(t *testing.T) {
	db := applyTxForTest(t)

	// ① payload 完全没有该字段
	got, err := resolveNoteFolderID(db, payloadIn{SyncID: "n1"})
	if err != nil {
		t.Fatalf("无 folder_sync_id 是合法语义（「我的笔记」），不应报错：%v", err)
	}
	if got != nil {
		t.Fatalf("「我的笔记」应解析为 nil folderID，实得 %v", *got)
	}

	// ② 字段存在但为空串（服务端 JSON 序列化成 "" 的情形）
	empty := ""
	got2, err2 := resolveNoteFolderID(db, payloadIn{FolderSyncID: &empty})
	if err2 != nil {
		t.Fatalf("空串 folder_sync_id 同为合法语义，不应报错：%v", err2)
	}
	if got2 != nil {
		t.Fatalf("空串应解析为 nil，实得 %v", *got2)
	}
}

// TestResolveNoteFolderIDResolvesExistingFolder 守住正向路径：
// 本地已有该 sync_id 时必须正确解析出 id（这是移动能生效的正例）。
func TestResolveNoteFolderIDResolvesExistingFolder(t *testing.T) {
	db := applyTxForTest(t)
	const syncID = "folder-sync-42"
	if _, err := db.Exec(`INSERT INTO note_folders (user_id, name, sort_order, sync_id, version)
		VALUES (1, 'B', 0, ?, 1)`, syncID); err != nil {
		t.Skipf("note_folders 表结构不可用，跳过：%v", err)
	}
	p := payloadIn{FolderSyncID: &[]string{syncID}[0]}
	got, err := resolveNoteFolderID(db, p)
	if err != nil {
		t.Fatalf("本地已有该文件夹时应解析成功：%v", err)
	}
	if got == nil {
		t.Fatal("本地已有该文件夹时不得返回 nil")
	}
}

// 断言辅助：确保签名在改动下保持兼容（防止误删 folderID 透传能力）。
var _ = func() error {
	// 编译期断言 resolveNoteFolderID 必须返回 (*int64, error)
	var f func(*sql.Tx, payloadIn) (*int64, error) = resolveNoteFolderID
	_ = f
	return nil
}
