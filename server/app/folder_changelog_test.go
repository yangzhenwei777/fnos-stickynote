package app

import (
	"encoding/json"
	"strings"
	"testing"

	"stickynote/database"
	"stickynote/syncspec"
)

// folder_changelog_test.go —— P1 批次2 门禁测试（§129 文件夹部分 / §18 / §55~§56）：
// server 模式文件夹 mutation 事务内产事件；移动产单条 FOLDER_ORDER 集合级事件（单一 seq）；
// 删除文件夹时回收站受影响笔记逐行 UPDATE 事件（payload 保持墓碑态）；桌面模式零事件零 bump。

func entityEvents(t *testing.T, userID int64, entityType string) []changeRow {
	t.Helper()
	var out []changeRow
	for _, r := range changeEvents(t, userID) {
		if r.Entity == entityType {
			out = append(out, r)
		}
	}
	return out
}

func TestServerFolderMutationsProduceChangeLog(t *testing.T) {
	setupDB(t)
	fs := NewServerFolderService()

	// CREATE → v1。
	f1, err := fs.Create(1, "夹A")
	if err != nil || f1.ID == nil {
		t.Fatalf("Create: %v", err)
	}
	evs := entityEvents(t, 1, syncspec.EntityTypeFolder)
	if len(evs) != 1 || evs[0].Op != "CREATE" || evs[0].Ver != 1 || evs[0].SyncID == nil {
		t.Fatalf("FOLDER CREATE 事件不符: %+v", evs)
	}

	// Rename → v2。
	if err := fs.Rename(*f1.ID, 1, "夹A改"); err != nil {
		t.Fatal(err)
	}
	evs = entityEvents(t, 1, syncspec.EntityTypeFolder)
	if evs[1].Op != "UPDATE" || evs[1].Ver != 2 || !strings.Contains(evs[1].Payload, "夹A改") {
		t.Fatalf("FOLDER rename 事件不符: %+v", evs[1])
	}

	// 第二夹 + 移动 → 单条 FOLDER_ORDER 事件（单一 seq），两夹 version 各 ++，逐行无独立 FOLDER 事件。
	f2, err := fs.Create(1, "夹B")
	if err != nil {
		t.Fatal(err)
	}
	folderEventsBefore := len(entityEvents(t, 1, syncspec.EntityTypeFolder))
	orderEventsBefore := len(entityEvents(t, 1, syncspec.EntityTypeFolderOrder))
	if err := fs.Move(*f2.ID, 1, "top"); err != nil {
		t.Fatal(err)
	}
	orderEvs := entityEvents(t, 1, syncspec.EntityTypeFolderOrder)
	if len(orderEvs) != orderEventsBefore+1 {
		t.Fatalf("一次移动应产恰好 1 条 FOLDER_ORDER 事件: %d→%d", orderEventsBefore, len(orderEvs))
	}
	if len(entityEvents(t, 1, syncspec.EntityTypeFolder)) != folderEventsBefore {
		t.Fatal("移动不得产逐行 FOLDER 事件（集合级单事件，§55）")
	}
	oev := orderEvs[len(orderEvs)-1]
	if oev.SyncID != nil {
		t.Fatal("FOLDER_ORDER entity_sync_id 必须为 NULL（单例键）")
	}
	var op syncspec.FolderOrderPayload
	if err := json.Unmarshal([]byte(oev.Payload), &op); err != nil {
		t.Fatalf("FOLDER_ORDER payload 解析: %v", err)
	}
	if len(op.Order) != 2 || len(op.Versions) != 2 {
		t.Fatalf("全序数组/版本表应各 2 项: %+v", op)
	}
	for _, vr := range op.Versions {
		if vr.Version < 2 {
			t.Fatalf("移动后各 folder version 应被推进: %+v", op.Versions)
		}
	}
	// 库内权威顺序：夹B 现在第一。
	var firstName string
	if err := database.DB.QueryRow(
		`SELECT name FROM note_folders WHERE user_id = 1 ORDER BY sort_order ASC, id ASC LIMIT 1`).Scan(&firstName); err != nil {
		t.Fatal(err)
	}
	if firstName != "夹B" {
		t.Fatalf("移动后首位应为夹B，得 %q", firstName)
	}

	// 幂等移动（已居顶再 top）→ 零新事件。
	nOrder := len(entityEvents(t, 1, syncspec.EntityTypeFolderOrder))
	if err := fs.Move(*f2.ID, 1, "top"); err != nil {
		t.Fatal(err)
	}
	if len(entityEvents(t, 1, syncspec.EntityTypeFolderOrder)) != nOrder {
		t.Fatal("次序未变的移动必须零事件（幂等）")
	}
}

// TestFolderDeleteRepointsTrashedNotes —— §56/§143：删除文件夹时回收站悬空引用
// 逐行 version++ + UPDATE 事件，payload 保持 deleted_at 墓碑态（桌面应用端不得复活）。
func TestFolderDeleteRepointsTrashedNotes(t *testing.T) {
	setupDB(t)
	ns := NewServerNoteService(100)
	fs := NewServerFolderService()

	f, err := fs.Create(1, "暂存夹")
	if err != nil || f.ID == nil {
		t.Fatal(err)
	}
	n, err := ns.CreateNote(1, "将入回收站", "c", "", f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.DeleteNote(n.ID, 1); err != nil { // v2 墓碑
		t.Fatal(err)
	}
	if err := fs.Delete(*f.ID, 1); err != nil {
		t.Fatal(err)
	}

	// 事件：FOLDER DELETE ×1 + NOTE UPDATE（墓碑态重指向）×1。
	var folderDel, noteUpd *changeRow
	all := changeEvents(t, 1)
	for i := range all {
		r := &all[i]
		switch {
		case r.Entity == syncspec.EntityTypeFolder && r.Op == "DELETE":
			folderDel = r
		case r.Entity == "NOTE" && r.Op == "UPDATE":
			noteUpd = r
		}
	}
	if folderDel == nil || noteUpd == nil {
		t.Fatalf("删除文件夹事件链不符: folderDel=%v noteUpd=%v", folderDel, noteUpd)
	}
	if !strings.Contains(noteUpd.Payload, `"deleted_at":`) {
		t.Fatalf("受影响笔记事件必须携带 deleted_at 墓碑态: %s", noteUpd.Payload)
	}
	if strings.Contains(noteUpd.Payload, `"folder_sync_id":"`) {
		t.Fatalf("受影响笔记事件 folder_sync_id 应为 null: %s", noteUpd.Payload)
	}
	var ver int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE id = ?`, n.ID).Scan(&ver); err != nil || ver != 3 {
		t.Fatalf("受影响笔记 version 应为 3（create1+软删2+重指向3），得 %d %v", ver, err)
	}
}

// TestDesktopFolderModeNoEvents —— 桌面本地模式：五个 mutation 全零事件零 bump（§25 P0-1）。
func TestDesktopFolderModeNoEvents(t *testing.T) {
	setupDB(t)
	fs := NewFolderService()
	f1, err := fs.Create(1, "本地夹1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Create(1, "本地夹2"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(*f1.ID, 1, "本地夹1改"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Move(*f1.ID, 1, "bottom"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete(*f1.ID, 1); err != nil {
		t.Fatal(err)
	}
	if evs := changeEvents(t, 1); len(evs) != 0 {
		t.Fatalf("桌面文件夹路径不得产任何事件: %+v", evs)
	}
	var ver int64
	if err := database.DB.QueryRow(`SELECT version FROM note_folders WHERE user_id = 1 ORDER BY id LIMIT 1`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != 1 {
		t.Fatalf("桌面本地文件夹 version 不得被 bump（应恒 1），得 %d", ver)
	}
}
