package sync

import (
	"encoding/json"
	"net/http"
	"testing"

	"stickynote/database"
	"stickynote/syncspec"
)

// folder_push_test.go —— P1 批次2 服务端契约：FOLDER CREATE/UPDATE/DELETE +
// FOLDER_ORDER 集合级 op（§18/§55 + ADR-002 R4 + §142 原子性必测）。

func folderCreateOp(opID, syncID, name string) PushOp {
	base := int64(0)
	return PushOp{OpID: opID, EntityType: syncspec.EntityTypeFolder, EntitySyncID: syncID, Operation: "CREATE", BaseVersion: &base,
		Payload: mustJSON(syncspec.FolderPayload{SyncID: syncID, Name: name})}
}

func folderRenameOp(opID, syncID, name string, base int64) PushOp {
	b := base
	return PushOp{OpID: opID, EntityType: syncspec.EntityTypeFolder, EntitySyncID: syncID, Operation: "UPDATE", BaseVersion: &b,
		Payload: mustJSON(syncspec.FolderPayload{SyncID: syncID, Name: name})}
}

func folderVersion(t *testing.T, syncID string) (int64, int) {
	t.Helper()
	var v int64
	var so int
	if err := database.DB.QueryRow(`SELECT version, sort_order FROM note_folders WHERE sync_id = ?`, syncID).Scan(&v, &so); err != nil {
		t.Fatal(err)
	}
	return v, so
}

func countEntityChanges(t *testing.T, entityType string) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_changes WHERE entity_type = ?`, entityType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFolderPushLifecycle(t *testing.T) {
	r := setupSyncDB(t)

	// CREATE → ACKED v1 + FOLDER CREATE 事件。
	res := doPush(t, r, "devA", folderCreateOp("fop-1", "fs-1", "推送夹"))
	if len(res) != 1 || res[0].Code != CodeAcked || res[0].ServerVersion != 1 {
		t.Fatalf("folder create: %+v", res)
	}
	if countEntityChanges(t, syncspec.EntityTypeFolder) != 1 {
		t.Fatal("应有 1 条 FOLDER 事件")
	}

	// 同 sync_id 重发（不同 op_id）→ 二层幂等 §44。
	res = doPush(t, r, "devA", folderCreateOp("fop-1b", "fs-1", "推送夹"))
	if res[0].Code != CodeIdempotentReplay {
		t.Fatalf("二层幂等失效: %+v", res)
	}

	// 同 op_id 同 payload 重放 → IDEMPOTENT_REPLAY（§8）。
	res = doPush(t, r, "devA", folderCreateOp("fop-1", "fs-1", "推送夹"))
	if res[0].Code != CodeIdempotentReplay {
		t.Fatalf("op_id 幂等失效: %+v", res)
	}

	// UPDATE：错 base → CONFLICT + remote_version；对 base → ACKED v2。
	res = doPush(t, r, "devA", folderRenameOp("fop-2", "fs-1", "改名9", 9))
	if res[0].Code != CodeConflict || res[0].RemoteVersion != 1 {
		t.Fatalf("错 base 应 CONFLICT(rv=1): %+v", res)
	}
	res = doPush(t, r, "devA", folderRenameOp("fop-3", "fs-1", "改名", 1))
	if res[0].Code != CodeAcked || res[0].ServerVersion != 2 {
		t.Fatalf("对 base 应 ACKED v2: %+v", res)
	}
	if v, _ := folderVersion(t, "fs-1"); v != 2 {
		t.Fatalf("库内版本应为 2，得 %d", v)
	}

	// 重名冲突 → CONFLICT FOLDER_NAME_EXISTS（另一夹占名）。
	doPush(t, r, "devA", folderCreateOp("fop-4", "fs-2", "乙夹"))
	res = doPush(t, r, "devA", folderRenameOp("fop-5", "fs-2", "改名", 1))
	if res[0].Code != CodeConflict || res[0].Error != "FOLDER_NAME_EXISTS" {
		t.Fatalf("重名应 CONFLICT: %+v", res)
	}
}

func TestFolderOrderAtomic(t *testing.T) {
	r := setupSyncDB(t)
	doPush(t, r, "devA", folderCreateOp("o1", "sa", "A"), folderCreateOp("o2", "sb", "B"))

	orderOp := func(opID string, order []string, bases map[string]int64) PushOp {
		return PushOp{OpID: opID, EntityType: syncspec.EntityTypeFolderOrder, Operation: "UPDATE",
			Payload: mustJSON(syncspec.FolderOrderPayload{Order: order, Bases: bases})}
	}

	// §142 必测：任一 base 不符 → 整 op CONFLICT 且"另一个 folder 也不提交"。
	bad := orderOp("ord-bad", []string{"sb", "sa"}, map[string]int64{"sb": 1, "sa": 99})
	res := doPush(t, r, "devA", bad)
	if res[0].Code != CodeConflict {
		t.Fatalf("§142 任一 base 不符应整 op CONFLICT: %+v", res)
	}
	if _, soA := folderVersion(t, "sa"); soA != 1 {
		t.Fatalf("§142 冲突后 A 的 sort 不得变化（A 也不提交）")
	}
	if _, soB := folderVersion(t, "sb"); soB != 2 {
		t.Fatalf("§142 冲突后 B 的 sort 不得变化")
	}
	if countEntityChanges(t, syncspec.EntityTypeFolderOrder) != 0 {
		t.Fatal("冲突 op 不得产事件")
	}

	// 正确 order → ACKED + OrderVersions + 库内顺序重写 + 逐夹 version++。
	res = doPush(t, r, "devA", orderOp("ord-ok", []string{"sb", "sa"}, map[string]int64{"sa": 1, "sb": 1}))
	if res[0].Code != CodeAcked || len(res[0].OrderVersions) != 2 {
		t.Fatalf("FOLDER_ORDER 应 ACKED 且携带逐夹版本: %+v", res)
	}
	if _, soA := folderVersion(t, "sa"); soA != 2 {
		t.Fatalf("A 应排第二，得 %d", soA)
	}
	if _, soB := folderVersion(t, "sb"); soB != 1 {
		t.Fatalf("B 应排第一，得 %d", soB)
	}
	if vA, _ := folderVersion(t, "sa"); vA != 2 {
		t.Fatalf("A version 应 ++=2，得 %d", vA)
	}
	if countEntityChanges(t, syncspec.EntityTypeFolderOrder) != 1 {
		t.Fatal("整批应恰 1 条 FOLDER_ORDER 事件（单一 seq，§55）")
	}

	// pull 侧形状钉死：entity_sync_id=NULL + payload 含 order/versions。
	pull := doPull(t, r, 0, 0)
	var orderCh map[string]any
	for _, c := range pull["changes"].([]any) {
		ch := c.(map[string]any)
		if ch["entity_type"] == syncspec.EntityTypeFolderOrder {
			orderCh = ch
		}
	}
	if orderCh == nil || orderCh["entity_sync_id"] != nil {
		t.Fatalf("FOLDER_ORDER 事件 entity_sync_id 必须 NULL: %v", orderCh)
	}
	var pl syncspec.FolderOrderPayload
	b, _ := json.Marshal(orderCh["payload"])
	if err := json.Unmarshal(b, &pl); err != nil || len(pl.Order) != 2 || pl.Order[0] != "sb" {
		t.Fatalf("pull payload 全序不符: %v %v", err, orderCh["payload"])
	}
}

func TestFolderOrderQuirk(t *testing.T) {
	r := setupSyncDB(t)
	doPush(t, r, "devA", folderCreateOp("q1", "qa", "甲"), folderCreateOp("q2", "qb", "乙"))

	// ADR-002 R2/R4：同批双 FOLDER_ORDER → 400 整批拒绝零执行。
	body := `{"device_id":"devA","ops":[
		{"op_id":"x1","entity_type":"FOLDER_ORDER","entity_sync_id":"","operation":"UPDATE","payload":{"order":["qb","qa"],"bases":{"qa":1,"qb":1}}},
		{"op_id":"x2","entity_type":"FOLDER_ORDER","entity_sync_id":"","operation":"UPDATE","payload":{"order":["qa","qb"],"bases":{"qa":1,"qb":1}}}]}`
	w := doPushRaw(t, r, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("双 FOLDER_ORDER 应整批 400，得 %d %s", w.Code, w.Body.String())
	}
	if countEntityChanges(t, syncspec.EntityTypeFolderOrder) != 0 {
		t.Fatal("协议拒绝必须零执行")
	}

	// order 覆盖不全（缺一个夹）→ 该 op BATCH_PROTOCOL。
	res := doPush(t, r, "devA", PushOp{OpID: "x3", EntityType: syncspec.EntityTypeFolderOrder, Operation: "UPDATE",
		Payload: mustJSON(syncspec.FolderOrderPayload{Order: []string{"qa"}, Bases: map[string]int64{"qa": 1}})})
	if res[0].Code != CodeBatchProtocol {
		t.Fatalf("覆盖不全应 BATCH_PROTOCOL_ERROR: %+v", res)
	}
}

func TestFolderDeleteWithTrashedNoteEvents(t *testing.T) {
	r := setupSyncDB(t)
	doPush(t, r, "devA", folderCreateOp("d1", "sd", "弃夹"))

	// 造一条"夹内已软删"笔记（回收站悬空引用场景）。
	var fid int64
	if err := database.DB.QueryRow(`SELECT id FROM note_folders WHERE sync_id='sd'`).Scan(&fid); err != nil {
		t.Fatal(err)
	}
	var nid int64
	res, err := database.DB.Exec(`INSERT INTO notes (user_id, title, content, content_text, tags, folder_id, sync_id, deleted_at)
		VALUES (1,'弃笔记','c','c','', ?, 'sn-del', CURRENT_TIMESTAMP)`, fid)
	if err != nil {
		t.Fatal(err)
	}
	nid, _ = res.LastInsertId()

	base := int64(1)
	del := PushOp{OpID: "d2", EntityType: syncspec.EntityTypeFolder, EntitySyncID: "sd", Operation: "DELETE", BaseVersion: &base,
		Payload: mustJSON(syncspec.FolderPayload{SyncID: "sd"})}
	res2 := doPush(t, r, "devA", del)
	if res2[0].Code != CodeAcked {
		t.Fatalf("folder delete 应 ACKED: %+v", res2)
	}

	// 事件流：FOLDER DELETE ×1 + NOTE UPDATE ×1（§56），note payload 保墓碑态。
	if countEntityChanges(t, syncspec.EntityTypeFolder) != 2 { // CREATE + DELETE
		t.Fatalf("FOLDER 事件应为 CREATE+DELETE 共 2: %d", countEntityChanges(t, syncspec.EntityTypeFolder))
	}
	var payload string
	var ver int64
	if err := database.DB.QueryRow(`SELECT payload_json, server_version FROM sync_changes
		WHERE entity_sync_id='sn-del' AND operation='UPDATE'`).Scan(&payload, &ver); err != nil {
		t.Fatalf("受影响笔记 UPDATE 事件缺失: %v", err)
	}
	if ver != 2 {
		t.Fatalf("受影响笔记版本应 ++=2，得 %d", ver)
	}
	var np map[string]any
	_ = json.Unmarshal([]byte(payload), &np)
	if da, ok := np["deleted_at"]; !ok || da == nil {
		t.Fatalf("重指向 UPDATE 事件 payload 必须保持墓碑态: %s", payload)
	}
	if fs, ok := np["folder_sync_id"]; !ok || fs != nil {
		t.Fatalf("重指向后 folder_sync_id 应为 null: %s", payload)
	}
	var folderIDNull int
	if err := database.DB.QueryRow(`SELECT CASE WHEN folder_id IS NULL THEN 1 ELSE 0 END FROM notes WHERE id = ?`, nid).Scan(&folderIDNull); err != nil || folderIDNull != 1 {
		t.Fatalf("笔记悬空引用应已置 NULL: %v %d", err, folderIDNull)
	}
}
