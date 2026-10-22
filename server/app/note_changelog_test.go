package app

import (
	"strings"
	"testing"
	"time"

	"stickynote/changelog"
	"stickynote/database"
	"stickynote/syncspec"
)

// note_changelog_test.go —— P2 批次1 门禁测试（§129 清单·笔记部分）：
// 服务端 Web 写路径（NewServerNoteService）必须在同一事务内产 ChangeLog 事件，
// 且 Delete/Restore/Move 统一 version++（§56~§60 / SOURCE-FACT #2~#8）；
// 桌面模式（NewNoteService）保持 P0-A 语义：本地不产事件、不 bump（synclocal §25 P0-1）。

// changeEvents 读回 user 的全部事件（seq 升序）。
type changeRow struct {
	Seq      int64
	Entity   string
	SyncID   *string
	Op       string
	Ver      int64
	DeviceID string
	Payload  string
}

func changeEvents(t *testing.T, userID int64) []changeRow {
	t.Helper()
	rows, err := database.DB.Query(`SELECT change_seq, entity_type, entity_sync_id, operation, server_version, device_id, payload_json
		FROM sync_changes WHERE user_id = ? ORDER BY change_seq`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []changeRow
	for rows.Next() {
		var r changeRow
		if err := rows.Scan(&r.Seq, &r.Entity, &r.SyncID, &r.Op, &r.Ver, &r.DeviceID, &r.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestServerWritePathsProduceChangeLog(t *testing.T) {
	setupDB(t)
	svc := NewServerNoteService(100)

	// CREATE：事件 +1，server_version=1，sync_id 非空（插入即生成，不依赖重启回填）。
	n, err := svc.CreateNote(1, "标题A", "<p>正文</p>", "标签1", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	evs := changeEvents(t, 1)
	if len(evs) != 1 || evs[0].Op != "CREATE" || evs[0].Ver != 1 || evs[0].Entity != "NOTE" {
		t.Fatalf("CREATE 事件不符: %+v", evs)
	}
	if evs[0].SyncID == nil || *evs[0].SyncID == "" {
		t.Fatal("CREATE 事件缺 entity_sync_id")
	}
	if evs[0].DeviceID != "" {
		t.Fatalf("Web 路径 device_id 应为空: %q", evs[0].DeviceID)
	}
	if !strings.Contains(evs[0].Payload, `"title":"标题A"`) {
		t.Fatalf("payload 应含权威标题: %s", evs[0].Payload)
	}
	var rowSyncID string
	if err := database.DB.QueryRow(`SELECT sync_id FROM notes WHERE id = ?`, n.ID).Scan(&rowSyncID); err != nil || rowSyncID == "" {
		t.Fatalf("新笔记行应有 sync_id: %v %q", err, rowSyncID)
	}

	// UPDATE：CAS 成功 → 事件 +1，version=2。
	if _, err := svc.UpdateNote(n.ID, 1, "标题B", "<p>新</p>", "", 1); err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	evs = changeEvents(t, 1)
	if len(evs) != 2 || evs[1].Op != "UPDATE" || evs[1].Ver != 2 {
		t.Fatalf("UPDATE 事件不符: %+v", evs)
	}

	// MOVE：version++（§57）但 updated_at 不动；payload.folder_sync_id 指向目标夹。
	fs := NewFolderService()
	f, err := fs.Create(1, "批次1夹")
	if err != nil || f.ID == nil {
		t.Fatalf("CreateFolder: %v %+v", err, f)
	}
	var updatedAtBefore time.Time
	if err := database.DB.QueryRow(`SELECT updated_at FROM notes WHERE id = ?`, n.ID).Scan(&updatedAtBefore); err != nil {
		t.Fatal(err)
	}
	mv, err := svc.MoveNoteToFolder(n.ID, 1, f.ID)
	if err != nil || mv != 3 {
		t.Fatalf("MoveNoteToFolder: err=%v ver=%d", err, mv)
	}
	var updatedAtAfter time.Time
	if err := database.DB.QueryRow(`SELECT updated_at FROM notes WHERE id = ?`, n.ID).Scan(&updatedAtAfter); err != nil {
		t.Fatal(err)
	}
	if !updatedAtAfter.Equal(updatedAtBefore) {
		t.Fatalf("移动不应动 updated_at: before=%v after=%v", updatedAtBefore, updatedAtAfter)
	}
	evs = changeEvents(t, 1)
	if len(evs) != 3 || evs[2].Op != "UPDATE" || evs[2].Ver != 3 {
		t.Fatalf("MOVE 事件不符: %+v", evs)
	}
	if !strings.Contains(evs[2].Payload, `"folder_sync_id":"`) {
		t.Fatalf("MOVE payload 应携带 folder_sync_id: %s", evs[2].Payload)
	}

	// DELETE：version++ + 墓碑事件（§58）。
	if err := svc.DeleteNote(n.ID, 1); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	evs = changeEvents(t, 1)
	if len(evs) != 4 || evs[3].Op != "DELETE" || evs[3].Ver != 4 {
		t.Fatalf("DELETE 事件不符: %+v", evs)
	}
	if !strings.Contains(evs[3].Payload, `"deleted_at":`) {
		t.Fatalf("DELETE payload 应含 deleted_at（软删态）: %s", evs[3].Payload)
	}

	// RESTORE：version++ + 事件（§59）。
	if err := svc.RestoreNote(n.ID, 1); err != nil {
		t.Fatalf("RestoreNote: %v", err)
	}
	evs = changeEvents(t, 1)
	if len(evs) != 5 || evs[4].Op != "RESTORE" || evs[4].Ver != 5 {
		t.Fatalf("RESTORE 事件不符: %+v", evs)
	}

	// COPY：新副本独立 sync_id + CREATE 事件（payload 含附件 refs 键）。
	cp, err := svc.CopyNoteToFolder(n.ID, 1, nil)
	if err != nil || cp.ID == n.ID {
		t.Fatalf("CopyNoteToFolder: %v %+v", err, cp)
	}
	evs = changeEvents(t, 1)
	if len(evs) != 6 || evs[5].Op != "CREATE" || evs[5].Ver != 1 {
		t.Fatalf("COPY 事件不符: %+v", evs)
	}
	if evs[5].SyncID == nil || *evs[5].SyncID == *evs[0].SyncID {
		t.Fatal("副本 sync_id 必须独立于源笔记")
	}
	if !strings.Contains(evs[5].Payload, `"attachment_refs":`) {
		t.Fatalf("COPY payload 应含 attachment_refs 键: %s", evs[5].Payload)
	}

	// change_seq 全局严格递增（§24 权威序列，非 MAX 语义）。
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq <= evs[i-1].Seq {
			t.Fatalf("change_seq 非严格递增: %d→%d", evs[i-1].Seq, evs[i].Seq)
		}
	}
}

// TestDesktopModeNoEvents 桌面模式（NewNoteService）不得产事件、不得 bump 本地版本
// （本地 version = 最后已知服务端版本，Outbox base_version 口径，synclocal §25 P0-1）。
func TestDesktopModeNoEvents(t *testing.T) {
	setupDB(t)
	svc := NewNoteService(100)

	n, err := svc.CreateNote(1, "本地建", "<p>本地</p>", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if evs := changeEvents(t, 1); len(evs) != 0 {
		t.Fatalf("桌面 CREATE 不应产事件: %+v", evs)
	}
	if _, err := svc.UpdateNote(n.ID, 1, "本地改", "", "", 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteNote(n.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreNote(n.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveNoteToFolder(n.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if evs := changeEvents(t, 1); len(evs) != 0 {
		t.Fatalf("桌面路径不应产任何事件: %+v", evs)
	}
	// 桌面 Delete/Restore/Move 不 bump（v21.87/§25 语义保持）。
	// E4-4（2026-09-23）：桌面 Update 也不 bump——本地 version 恒为"最后已知服务端版本"
	//（P0-1），bump 只由推送 ACK 回写；此前旧断言把「Update bump」当既有行为钉住，
	// 正是导致推送 base 恒大于服务端 version、编辑必 409 的缺陷形状。
	var ver int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE id = ?`, n.ID).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != 1 {
		t.Fatalf("桌面本地 Update 不得 bump version（应保持 last-known=1），得 %d", ver)
	}
}

// TestEnsureNoteSyncID 存量 NULL sync_id 行在变更事件前被同事务补齐（防御批次1 部署间隙）。
func TestEnsureNoteSyncID(t *testing.T) {
	setupDB(t)
	n, err := NewServerNoteService(100).CreateNote(1, "t", "c", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟部署间隙产生的无身份行（清空 sync_id）。
	if _, err := database.DB.Exec(`UPDATE notes SET sync_id = '' WHERE id = ?`, n.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := database.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := changelog.EnsureNoteSyncID(tx, n.ID)
	if err != nil || id == "" {
		t.Fatalf("EnsureNoteSyncID: %v %q", err, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var back string
	if err := database.DB.QueryRow(`SELECT sync_id FROM notes WHERE id = ?`, n.ID).Scan(&back); err != nil || back != id {
		t.Fatalf("sync_id 未落库: %v %q", err, back)
	}
}

// TestServerCryptoWritesProduceChangeLog —— 加密三件（§60 / SOURCE-FACT #9~#11）：
// server 模式 Set/Clear/UpdateEncrypted 同事务产 UPDATE 事件且 version++；
// 重复加密等业务拒绝零新增事件；桌面模式零事件零 bump。
func TestServerCryptoWritesProduceChangeLog(t *testing.T) {
	setupDB(t)
	svc := NewServerNoteService(100)

	n, err := svc.CreateNote(1, "机密", "<p>密</p>", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 加密 → UPDATE 事件 v2，payload 密文态。
	v, err := svc.EncryptNote(n.ID, 1, "pw12345", "pw12345")
	if err != nil || v != 2 {
		t.Fatalf("EncryptNote: err=%v v=%d", err, v)
	}
	evs := changeEvents(t, 1)
	if len(evs) != 2 || evs[1].Op != "UPDATE" || evs[1].Ver != 2 {
		t.Fatalf("加密事件不符: %+v", evs)
	}
	if !strings.Contains(evs[1].Payload, `"is_encrypted":true`) ||
		!strings.Contains(evs[1].Payload, `"encrypted_content":"`) ||
		!strings.Contains(evs[1].Payload, `"content":""`) {
		t.Fatalf("加密 payload 应为密文态: %s", evs[1].Payload)
	}

	// 重复加密：业务拒绝，零新增事件。
	if _, err := svc.EncryptNote(n.ID, 1, "x", "x"); ErrorCodeFor(err) != "ALREADY_ENCRYPTED" {
		t.Fatalf("重复加密应 ALREADY_ENCRYPTED，得 %v", err)
	}
	if len(changeEvents(t, 1)) != 2 {
		t.Fatal("业务拒绝不得产事件")
	}

	// 解密 → UPDATE 事件 v3，payload 明文态。
	plain, err := svc.DecryptNote(n.ID, 1, "pw12345")
	if err != nil || plain != "<p>密</p>" {
		t.Fatalf("DecryptNote: %v %q", err, plain)
	}
	evs = changeEvents(t, 1)
	if len(evs) != 3 || evs[2].Ver != 3 || !strings.Contains(evs[2].Payload, `"is_encrypted":false`) {
		t.Fatalf("解密事件不符: %+v", evs[2])
	}

	// 加密态更新（CAS 基点=3）→ UPDATE 事件 v4。
	if _, err := svc.EncryptNote(n.ID, 1, "pw12345", "pw12345"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateEncrypted(n.ID, 1, "机密2", "", "<p>新密文</p>", "pw12345", 4); err != nil {
		t.Fatalf("UpdateEncrypted: %v", err)
	}
	evs = changeEvents(t, 1)
	if len(evs) != 5 || evs[4].Op != "UPDATE" || evs[4].Ver != 5 {
		t.Fatalf("加密更新事件不符: %+v", evs)
	}
	if !strings.Contains(evs[4].Payload, `"title":"机密2"`) {
		t.Fatalf("加密更新 payload 应含新标题: %s", evs[4].Payload)
	}
}

// TestBatchDeleteSingleTx —— 批量删除（SOURCE-FACT #6）：单事务逐行 DELETE 墓碑事件；
// 不存在 ID 跳过且零事件；桌面模式零事件。
func TestBatchDeleteSingleTx(t *testing.T) {
	setupDB(t)
	svc := NewServerNoteService(100)

	n1, _ := svc.CreateNote(1, "批1", "c", "", nil)
	n2, _ := svc.CreateNote(1, "批2", "c", "", nil)
	if _, err := svc.CreateNote(1, "批3", "c", "", nil); err != nil {
		t.Fatal(err)
	}
	// 删 2 个成功 + 1 个不存在 → deleted=2，事件 +2（CREATE×3 + DELETE×2）。
	deleted, err := svc.BatchDeleteNotes(1, []int64{n1.ID, n2.ID, 999999})
	if err != nil || deleted != 2 {
		t.Fatalf("BatchDeleteNotes: err=%v deleted=%d", err, deleted)
	}
	evs := changeEvents(t, 1)
	if len(evs) != 5 {
		t.Fatalf("事件总数应=5，得 %d", len(evs))
	}
	if evs[3].Op != "DELETE" || evs[3].Ver != 2 || evs[4].Op != "DELETE" || evs[4].Ver != 2 {
		t.Fatalf("批量 DELETE 事件不符: %+v %+v", evs[3], evs[4])
	}

	// 桌面模式：单事务删除但零事件零 bump。
	desktop := NewNoteService(100)
	d1, _ := desktop.CreateNote(1, "本地批", "c", "", nil)
	d2, _ := desktop.CreateNote(1, "本地批2", "c", "", nil)
	before := len(changeEvents(t, 1))
	if n, err := desktop.BatchDeleteNotes(1, []int64{d1.ID, d2.ID}); err != nil || n != 2 {
		t.Fatalf("桌面批量删除: %v n=%d", err, n)
	}
	if after := len(changeEvents(t, 1)); after != before {
		t.Fatalf("桌面批量删除不得产事件: %d→%d", before, after)
	}
}

// TestPurgePathsProduceNoEvents —— ADR-004 R1 豁免清单反向断言（§129 门禁）：
// E7（2026-09-23 用户产品决策，**推翻原「purge 不发事件」设计**）：服务端永久删
// （彻底删/清空回收站）**必须产 PURGE 事件**——不发事件会让客户端永远留僵尸行、
// 恢复必撞 ENTITY_GONE（真机六轮实锤的回收站不同步根因）。
// 桌面模式（NewNoteService）仍不产事件（本地库事件权威在服务端 push apply）。
func TestPurgePathsProducePurgeEvents(t *testing.T) {
	setupDB(t)
	svc := NewServerNoteService(100)

	n1, _ := svc.CreateNote(1, "purge1", "c", "", nil)
	n2, _ := svc.CreateNote(1, "purge2", "c", "", nil)
	// 软删两篇（各产 1 条 DELETE 墓碑）→ 2 CREATE + 2 DELETE = 4 事件。
	if _, err := svc.BatchDeleteNotes(1, []int64{n1.ID, n2.ID}); err != nil {
		t.Fatal(err)
	}
	base := len(changeEvents(t, 1))
	if base != 4 {
		t.Fatalf("前置断言：应 4 事件，得 %d", base)
	}
	var seqBefore int64
	if err := database.DB.QueryRow(`SELECT current_change_seq FROM sync_meta WHERE id = 1`).Scan(&seqBefore); err != nil {
		t.Fatal(err)
	}

	// 彻底删除单条（app Facade server 模式）→ +1 PURGE 事件。
	if err := svc.PermanentDelete(n1.ID, 1); err != nil {
		t.Fatalf("PermanentDelete: %v", err)
	}
	evs := changeEvents(t, 1)
	if len(evs) != base+1 || evs[len(evs)-1].Op != syncspec.OperationPurge {
		t.Fatalf("彻底删应产 PURGE 事件: %+v", evs[len(evs)-1])
	}
	// 清空回收站（app Facade server 模式）→ 每条 +1 PURGE。
	cnt, _, err := svc.EmptyTrash(1)
	if err != nil || cnt != 1 {
		t.Fatalf("EmptyTrash: cnt=%d err=%v", cnt, err)
	}
	evs2 := changeEvents(t, 1)
	if len(evs2) != base+2 || evs2[len(evs2)-1].Op != syncspec.OperationPurge {
		t.Fatalf("清空回收站应产 PURGE 事件: %+v", evs2[len(evs2)-1])
	}
	var seqAfter int64
	if err := database.DB.QueryRow(`SELECT current_change_seq FROM sync_meta WHERE id = 1`).Scan(&seqAfter); err != nil {
		t.Fatal(err)
	}
	if seqAfter != seqBefore+2 {
		t.Fatalf("purge 应推进 change_seq +2: %d→%d", seqBefore, seqAfter)
	}
}
