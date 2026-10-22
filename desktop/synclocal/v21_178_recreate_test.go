package synclocal

// v21_178_recreate_test.go —— 1.0.218 新处置「以本机内容重建到服务器」回归钉。
//
// 背景（217 内测实锤的出路面）：离线新建/导入的笔记在旧缺陷下卡成 ENTITY_GONE 冲突后，
// 用户此前只有两条路——「另存副本」（换身份新建 ⇒ 重复笔记 + 落错文件夹）或「放弃本机这笔」
// （内容永不上云）。recreate 补上第三条：**不换身份、不产生副本**，以本机当前行内容
// 新排 CREATE（换新 op_id）推上去，服务端本无此实体 ⇒ 建回即收敛。
//
// 本文件钉：可用性矩阵（有行可重建 / 无行拒绝 / 回收站拒绝）与真实上云收敛，
// 防"顺手把 recreate 扩散到 STALE 或无行场景"的过度开放。

import (
	"context"
	"errors"
	"testing"

	"stickynote-desktop/internal/bootptest"
)

// ⭐ 主场景（红→绿→上云）：ENTITY_GONE + 本机行在 ⇒ recreate 后真实收敛到服务端。
func TestRecreateEntityGoneConvergesToServer(t *testing.T) {
	db := newStore(t)
	ts := bootptest.Start(t)
	w := NewWorker(db, NewClient(ts.URL, ts.Token(t), "devA"))

	id := localCreate(t, db, "s-recreate", "重建稿")
	localEdit(t, db, id, "重建稿-改")

	// 复刻 217 现场：op 链全部转 CONFLICT + 登记 ENTITY_GONE 冲突（与 applyResult 分支同形状）。
	var opID, payload string
	if err := db.QueryRow(`SELECT op_id, payload FROM sync_outbox WHERE entity_sync_id='s-recreate'
		AND operation='UPDATE' ORDER BY seq DESC LIMIT 1`).Scan(&opID, &payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sync_outbox SET status='CONFLICT', last_error='ENTITY_GONE'
		WHERE entity_sync_id='s-recreate'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, op_id, kind,
		local_payload, remote_payload, base_version, remote_version)
		VALUES ('s-recreate', 'NOTE', 'UPDATE', ?, 'ENTITY_GONE', ?, '', 0, 0)`, opID, payload); err != nil {
		t.Fatal(err)
	}

	list, _, err := ListConflicts(db, true, 10, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("前置：应有 1 条 open 冲突，得 len=%d err=%v", len(list), err)
	}
	c := list[0]
	if len(c.AllowedResolutions) != 2 || c.AllowedResolutions[0] != ResolveRecreate || c.AllowedResolutions[1] != ResolveDropLocal {
		t.Fatalf("ENTITY_GONE + 本机行在应开放 [recreate, drop-local]，得 %v", c.AllowedResolutions)
	}

	res, err := w.ResolveConflict(c.ID, ResolveRecreate, false)
	if err != nil {
		t.Fatalf("recreate 处置失败: %v", err)
	}
	if res.RequeuedOps != 1 || res.TerminatedOps < 1 {
		t.Fatalf("应终止旧链并新排 1 笔 CREATE，得 %+v", res)
	}
	// 新 op：CREATE + 新身份（op_id 必须换新——内容为新构建，沿用旧 op_id 会撞 REUSE）
	var newOpID, oper, status string
	if err := db.QueryRow(`SELECT op_id, operation, status FROM sync_outbox
		WHERE entity_sync_id='s-recreate' AND status='PENDING' ORDER BY seq DESC LIMIT 1`).
		Scan(&newOpID, &oper, &status); err != nil {
		t.Fatalf("应存在一笔 PENDING 重建 op: %v", err)
	}
	if oper != "CREATE" {
		t.Fatalf("重建必须以 CREATE 重排队（UPDATE 对不存在实体必再撞 GONE），得 %s", oper)
	}
	if newOpID == opID {
		t.Fatal("重建是内容改写，必须换新 op_id（E4② 不变量）")
	}

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := ts.HasNote(t, "重建稿-改"); got != 1 {
		t.Fatalf("重建后笔记必须上云（以本机当前内容），服务端 count=%d", got)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`); c != 0 {
		t.Fatalf("处置后待处置应归零，得 %d", c)
	}
	if n := openCount(t, db); n != 0 {
		t.Fatalf("收敛后不得留开账，得 %d", n)
	}
}

// fail-closed ①：本机已无该实体 ⇒ 矩阵不开放 recreate，强投给真因（不是笼统“不支持”）。
func TestRecreateRefusedWhenLocalRowMissing(t *testing.T) {
	db := newStore(t)
	if _, err := db.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, op_id, kind,
		local_payload, remote_payload, base_version, remote_version)
		VALUES ('s-ghost', 'NOTE', 'UPDATE', 'op-g', 'ENTITY_GONE', '{"sync_id":"s-ghost","title":"x","content":"y"}', '', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	list, _, err := ListConflicts(db, true, 10, 0)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	if len(list[0].AllowedResolutions) != 1 || list[0].AllowedResolutions[0] != ResolveDropLocal {
		t.Fatalf("本机无行应只开放 [drop-local]，得 %v", list[0].AllowedResolutions)
	}
	w := NewWorker(db, NewClient("", "", "devA"))
	_, err = w.ResolveConflict(list[0].ID, ResolveRecreate, false)
	if !errors.Is(err, ErrRecreateEntityMissing) {
		t.Fatalf("应给 ErrRecreateEntityMissing（指引改选放弃），得 %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_outbox WHERE status='PENDING'`); n != 0 {
		t.Fatalf("拒绝时不得留下任何新 op，得 %d", n)
	}
}

// fail-closed ②：本机行在回收站 ⇒ 不开放 recreate，强投给"先还原或放弃"的真因。
func TestRecreateRefusedWhenNoteInTrash(t *testing.T) {
	db := newStore(t)
	if _, err := db.Exec(`INSERT INTO notes (id, user_id, title, content, content_text, tags, sync_id, deleted_at)
		VALUES (200, 1, '删稿', '<p>x</p>', 'x', '', 's-trash2', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, op_id, kind,
		local_payload, remote_payload, base_version, remote_version)
		VALUES ('s-trash2', 'NOTE', 'UPDATE', 'op-t', 'ENTITY_GONE', '{"sync_id":"s-trash2","title":"删稿","content":"y"}', '', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	list, _, err := ListConflicts(db, true, 10, 0)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	if len(list[0].AllowedResolutions) != 1 || list[0].AllowedResolutions[0] != ResolveDropLocal {
		t.Fatalf("回收站行应只开放 [drop-local]，得 %v", list[0].AllowedResolutions)
	}
	w := NewWorker(db, NewClient("", "", "devA"))
	_, err = w.ResolveConflict(list[0].ID, ResolveRecreate, false)
	if !errors.Is(err, ErrRecreateInTrash) {
		t.Fatalf("应给 ErrRecreateInTrash（指引先还原），得 %v", err)
	}
}

// 文件夹实体同享 recreate：终止旧链并新排一笔 FOLDER CREATE。
func TestRecreateFolderRequeuesCreate(t *testing.T) {
	db := newStore(t)
	if _, err := db.Exec(`INSERT INTO note_folders (id, user_id, name, sort_order, sync_id, version)
		VALUES (300, 1, '离线夹', 1, 's-fold-r', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, op_id, kind,
		local_payload, remote_payload, base_version, remote_version)
		VALUES ('s-fold-r', 'FOLDER', 'UPDATE', 'op-f', 'ENTITY_GONE', '{"sync_id":"s-fold-r","name":"离线夹","version":1}', '', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	list, _, err := ListConflicts(db, true, 10, 0)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	if len(list[0].AllowedResolutions) != 2 || list[0].AllowedResolutions[0] != ResolveRecreate {
		t.Fatalf("文件夹本机行在应开放 recreate，得 %v", list[0].AllowedResolutions)
	}
	w := NewWorker(db, NewClient("", "", "devA"))
	res, err := w.ResolveConflict(list[0].ID, ResolveRecreate, false)
	if err != nil || res.RequeuedOps != 1 {
		t.Fatalf("文件夹 recreate 应新排 1 笔 CREATE，得 res=%+v err=%v", res, err)
	}
	var oper, status, payload string
	if err := db.QueryRow(`SELECT operation, status, payload FROM sync_outbox
		WHERE entity_sync_id='s-fold-r' ORDER BY seq DESC LIMIT 1`).Scan(&oper, &status, &payload); err != nil {
		t.Fatal(err)
	}
	if oper != "CREATE" || status != "PENDING" {
		t.Fatalf("应为 PENDING FOLDER CREATE，得 %s/%s", oper, status)
	}
	if payload == "" || payload[0] != '{' {
		t.Fatalf("载荷应为 FolderPayload JSON，得 %q", payload)
	}
}
