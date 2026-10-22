//go:build windows

package main

// e8_purge_enqueue_windows_test.go —— E8 双向闭环回归钉（2026-09-23 用户实测缺口）：
// 桌面端「彻底删除 / 清空回收站」必须**入队 PURGE 推给服务端**——此前只做本地物理删，
// 服务端回收站里仍留着（用户实测第 2/4 条）。入队须在物理删**之前**（payload 需行在）。

import (
	"testing"

	"stickynote-desktop/synclocal"
	"stickynote/database"
)

// mountAccountingWorker 挂空票 worker（真实形态：档案可开即挂工，enqueueNote 才有通道）。
func mountAccountingWorker(t *testing.T) {
	t.Helper()
	setWorker(synclocal.NewWorker(database.DB, synclocal.NewClient("", "", "")))
	t.Cleanup(func() { setWorker(nil) })
}

func TestPermanentDeleteEnqueuesPurge(t *testing.T) {
	ls, _ := bootWithLogin(t)
	mountAccountingWorker(t)
	n, err := ls.CreateNote("待彻底删", "<p>x</p>", "", nil)
	if err != nil || n == nil {
		t.Fatalf("create: %v", err)
	}
	if err := ls.DeleteNote(n.ID); err != nil { // 先软删（进回收站）
		t.Fatalf("soft delete: %v", err)
	}
	if _, err := ls.PermanentDeleteNote(n.ID); err != nil {
		t.Fatalf("permanent delete: %v", err)
	}
	var n2 int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_outbox
		WHERE operation='PURGE' AND status='PENDING'`).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n2 != 1 {
		t.Fatalf("E8：彻底删必须入队 1 条 PURGE，得 %d", n2)
	}
	var live int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM notes WHERE id = ?`, n.ID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatal("本地行应已物理删")
	}
}

func TestEmptyTrashEnqueuesPurgePerNote(t *testing.T) {
	ls, _ := bootWithLogin(t)
	mountAccountingWorker(t)
	var ids []int64
	for i := 0; i < 2; i++ {
		n, err := ls.CreateNote("夹", "<p>x</p>", "", nil)
		if err != nil || n == nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if err := ls.DeleteNote(n.ID); err != nil {
			t.Fatalf("soft delete %d: %v", i, err)
		}
		ids = append(ids, n.ID)
	}
	if _, err := ls.EmptyTrash(); err != nil {
		t.Fatalf("empty trash: %v", err)
	}
	var n int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_outbox
		WHERE operation='PURGE' AND status='PENDING'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("E8：清空回收站应按条入队 PURGE（2 条），得 %d", n)
	}
	for _, id := range ids {
		var c int
		if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM notes WHERE id = ?`, id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("本地行 %d 应已物理删", id)
		}
	}
}
