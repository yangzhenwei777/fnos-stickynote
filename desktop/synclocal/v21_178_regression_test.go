package synclocal

// v21_178_regression_test.go —— 217 内测实锤·离线「新建/导入 + 编辑」链不收敛的根治回归钉。
//
// ════════════════════ 缺陷现场（217 用户实测）════════════════════
//
// 离线新建/导入一条笔记，其间发生过任意一次后续编辑（额外产生一笔 UPDATE op）⇒
// 恢复联网后该笔记**永不收敛**：服务端没有它，冲突中心留下 kind=ENTITY_GONE 的
// 「远端该实体已被删除」（用户截图三要素：远端版本 0 / 远端内容（无内容）/ 未推送改动 1 笔）。
//
// 根因（探针实测坐实，2026-10-14）：buildBatch §49 链合并「取链尾 op」把
// [CREATE, UPDATE] 合成单条 **UPDATE** 推出去；服务端 opCasUpdate 对不存在的实体
// 回 CodeEntityGone（协议设计的自愈道是 CREATE 重发：sync_id 不存在→INSERT、
// 已存在→IDEMPOTENT_REPLAY，见 server/sync/push.go opCreate 第二层幂等）。
//
// 修法（链头优先）：链头是 CREATE 且链尾非 DELETE/PURGE ⇒ 本批只推链头 CREATE
// （op_id/payload/base 保持原生身份），链尾原样留 PENDING，下一轮自动合并续推。
// [.., DELETE/PURGE] 链维持既有合并语义（v21.221：目标态"不存在"已达成即清账）。
//
// 本文件同钉：文件夹实体同型链（[F-CREATE, F-UPDATE]）、"ACK 丢失"重放路径、
// 以及不得被过度修复破坏的"先建后删"清账语义。

import (
	"context"
	"strings"
	"testing"

	"stickynote-desktop/internal/bootptest"
)

// ⭐ 主场景（红→绿）：离线新建 + 两次编辑 ⇒ 恢复后以 CREATE 落地并收敛到最终内容。
func TestOfflineCreateEditChainConvergesAsCreate(t *testing.T) {
	db := newStore(t)
	ts := bootptest.Start(t)
	w := NewWorker(db, NewClient(ts.URL, ts.Token(t), "devA"))

	id := localCreate(t, db, "s-chain", "初稿")
	localEdit(t, db, id, "第一次编辑")
	localEdit(t, db, id, "最终稿")

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := ts.HasNote(t, "最终稿"); got != 1 {
		t.Fatalf("离线新建+编辑链必须以 CREATE 落地并收敛到最终内容，服务端 count=%d（不得转 ENTITY_GONE 冲突）", got)
	}
	if got := ts.HasNote(t, "初稿"); got != 0 {
		t.Fatalf("链尾 UPDATE 应把服务端内容推到最终稿，不应残留在初稿（count=%d）", got)
	}
	if n := openCount(t, db); n != 0 {
		t.Fatalf("收敛后不得留开账（PENDING/CONFLICT 等），得 %d", n)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`); c != 0 {
		t.Fatalf("本场景不得产生任何待处置冲突，得 %d", c)
	}
}

// 文件夹同型缺陷：离线[新建文件夹 + 改名] + [夹内新建笔记 + 编辑]，一并收敛。
func TestOfflineCreateEditChainWithFolderConverges(t *testing.T) {
	db := newStore(t)
	ts := bootptest.Start(t)
	w := NewWorker(db, NewClient(ts.URL, ts.Token(t), "devA"))

	res, err := db.Exec(`INSERT INTO note_folders (user_id, name, sort_order, sync_id, version)
		VALUES (1, '离线夹', 1, 'f-chain', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	fid, _ := res.LastInsertId()
	if err := EnqueueFolderOp(db, fid, "CREATE"); err != nil {
		t.Fatal(err)
	}
	res2, err := db.Exec(`INSERT INTO notes (user_id, title, content, content_text, tags, sync_id, folder_id)
		VALUES (1, '夹内初稿', '<p>x</p>', 'x', '', 's-fnote', ?)`, fid)
	if err != nil {
		t.Fatal(err)
	}
	nid, _ := res2.LastInsertId()
	if err := EnqueueNoteOp(db, nid, "CREATE"); err != nil {
		t.Fatal(err)
	}
	localEdit(t, db, nid, "夹内终稿")
	// 文件夹离线改名 → [F-CREATE, F-UPDATE] 同型链
	if _, err := db.Exec(`UPDATE note_folders SET name = '离线夹改名' WHERE id = ?`, fid); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueFolderOp(db, fid, "UPDATE"); err != nil {
		t.Fatal(err)
	}

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := ts.HasNote(t, "夹内终稿"); got != 1 {
		t.Fatalf("夹内笔记（含文件夹链）必须收敛，服务端 count=%d", got)
	}
	if n := openCount(t, db); n != 0 {
		t.Fatalf("收敛后不得留开账，得 %d", n)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`); c != 0 {
		t.Fatalf("本场景不得产生待处置冲突，得 %d", c)
	}
}

// ACK 丢失重放路径：CREATE 已到服务端但 ACK 丢（行回 PENDING），用户又编辑 ⇒
// 链头 CREATE 原身份重推（服务端按 sync_id 幂等回放）→ 链尾 UPDATE 以重定基准入 CAS。
// 该路径旧新代码都应收敛——作为"链头优先不得破坏重放"的回归网常绿。
func TestOfflineCreateChainReplayThenTailApplies(t *testing.T) {
	db := newStore(t)
	ts := bootptest.Start(t)
	w := NewWorker(db, NewClient(ts.URL, ts.Token(t), "devA"))

	id := localCreate(t, db, "s-replay", "r0")
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ts.HasNote(t, "r0") != 1 {
		t.Fatal("前置：首轮应把 CREATE 推上服务端")
	}

	// 模拟 ACK 丢失：行回 PENDING（op_id/payload/base 均不变 = 与已登记请求同身份）
	if _, err := db.Exec(`UPDATE sync_outbox SET status='PENDING' WHERE entity_sync_id='s-replay'`); err != nil {
		t.Fatal(err)
	}
	localEdit(t, db, id, "r1")

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := ts.HasNote(t, "r1"); got != 1 {
		t.Fatalf("重放后链尾应把最终内容推上服务端，count=%d", got)
	}
	if n := openCount(t, db); n != 0 {
		t.Fatalf("重放路径收敛后不得留开账，得 %d", n)
	}
}

// 反向保护（v21.221 裁决不得被本轮破坏）：「先建后删」[CREATE, DELETE] 仍走合并 →
// 服务端回"目标态已达成"→ 清账；**不得**在服务端复活一条笔记。
func TestOfflineCreateThenDeleteChainStaysMergedAndClean(t *testing.T) {
	db := newStore(t)
	ts := bootptest.Start(t)
	w := NewWorker(db, NewClient(ts.URL, ts.Token(t), "devA"))

	id := localCreate(t, db, "s-cd", "先建后删")
	if _, err := db.Exec(`UPDATE notes SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueNoteOp(db, id, "DELETE"); err != nil {
		t.Fatal(err)
	}

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := ts.HasNote(t, "先建后删"); got != 0 {
		t.Fatalf("先建后删不得在服务端复活（count=%d）", got)
	}
	if n := openCount(t, db); n != 0 {
		t.Fatalf("清账后不得留开账，得 %d", n)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`); c != 0 {
		t.Fatalf("撤单类目标态已达成，不得产生冲突，得 %d", c)
	}
}

// 单元级（不发网络）：批构造两轮行为逐字段坐实——第一轮只推链头 CREATE（原生身份），
// 链尾留 PENDING；第二轮（模拟 ACK 后）链尾合并为 UPDATE 续推。
func TestBuildBatchHeadFirstUnit(t *testing.T) {
	db := newStore(t)
	id := localCreate(t, db, "s-unit", "u0")
	localEdit(t, db, id, "u1")
	w := NewWorker(db, NewClient("", "", "devA"))

	b1, err := w.buildBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(b1.ops) != 1 {
		t.Fatalf("第一轮应只推 1 笔（链头），得 %d", len(b1.ops))
	}
	head := b1.ops[0]
	if head.Operation != "CREATE" || head.EntitySyncID != "s-unit" {
		t.Fatalf("链头必须是原生 CREATE（否则服务端 opCasUpdate 必回 ENTITY_GONE），得 %s/%s",
			head.Operation, head.EntitySyncID)
	}
	if !strings.Contains(string(head.Payload), "u0") {
		t.Fatalf("链头 payload 应保持创建时内容（原生身份），得 %s", string(head.Payload))
	}
	// 链尾行必须原样留 PENDING（不得被标 merged/ACKED）——否则编辑内容丢失
	var tailStatus string
	if err := db.QueryRow(`SELECT status FROM sync_outbox WHERE entity_sync_id='s-unit' AND operation='UPDATE'
		ORDER BY seq DESC LIMIT 1`).Scan(&tailStatus); err != nil {
		t.Fatal(err)
	}
	if tailStatus != "PENDING" {
		t.Fatalf("链尾 UPDATE 行应留 PENDING 下轮续推，得 %q", tailStatus)
	}

	// 模拟第一轮 ACK（真实路径由 applyResult 完成）→ 第二轮链尾合并为 UPDATE
	if _, err := db.Exec(`UPDATE sync_outbox SET status='ACKED' WHERE seq = ?`, b1.ids[0]); err != nil {
		t.Fatal(err)
	}
	b2, err := w.buildBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(b2.ops) != 1 || b2.ops[0].Operation != "UPDATE" {
		t.Fatalf("第二轮应把链尾合并为单笔 UPDATE 续推，得 %+v", b2.ops)
	}
	if !strings.Contains(string(b2.ops[0].Payload), "u1") {
		t.Fatalf("续推 payload 应取链尾（最终内容），得 %s", string(b2.ops[0].Payload))
	}
}

// 单元级：[CREATE, DELETE] 链尾为 DELETE ⇒ 保持合并（只推 DELETE），链头不得单独先推。
func TestBuildBatchDeleteTailStillMerged(t *testing.T) {
	db := newStore(t)
	id := localCreate(t, db, "s-cd2", "c")
	if _, err := db.Exec(`UPDATE notes SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueNoteOp(db, id, "DELETE"); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, NewClient("", "", "devA"))
	b, err := w.buildBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.ops) != 1 || b.ops[0].Operation != "DELETE" {
		t.Fatalf("先建后删必须合并为单笔 DELETE（目标态：服务端不应存在该笔记），得 %+v", b.ops)
	}
}
