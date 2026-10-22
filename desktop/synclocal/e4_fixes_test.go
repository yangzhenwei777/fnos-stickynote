package synclocal

// e4_fixes_test.go —— E4 三刀修复回归钉（2026-09-23 真机账本实锤的三缺陷）：
//   E4② op_id 不变量：任何 payload/base 改写必须同步换新 op_id（rebaseOrderPayloads 实证）；
//   E4③ 依赖排序：父夹未就绪（仍在同步链上）时，其下 NOTE/FOLDER_ORDER op 缓推不入批，
//        就绪后自动续推（消灭 unknown folder_sync_id 级联拒收）。
// 刀①（auto-bootstrap 只数未收账）为 scheduledRound 内 SQL 语义修正，由既有门测试与编译保护。

import (
	"strings"
	"testing"
)

func TestRebaseOrderPayloadsRewritesOpID(t *testing.T) {
	a := newStore(t)
	if _, err := a.Exec(`INSERT INTO note_folders (id, user_id, name, sync_id, version)
		VALUES (1, 1, '夹', 'f-rb', 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload)
		VALUES ('OLD-OP', 'FOLDER_ORDER', 'order-1', 'UPDATE', NULL, '{"bases":{"f-rb":1}}')`); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(a, NewClient("", "", ""))
	if err := w.rebaseOrderPayloads(); err != nil {
		t.Fatal(err)
	}
	var opID, payload string
	if err := a.QueryRow(`SELECT op_id, payload FROM sync_outbox WHERE entity_sync_id='order-1'`).Scan(&opID, &payload); err != nil {
		t.Fatal(err)
	}
	if opID == "OLD-OP" {
		t.Fatal("E4②：payload 改写后必须换新 op_id，否则重推必撞 OP_ID_REUSE → STALE")
	}
	if !strings.Contains(payload, `"f-rb":2`) {
		t.Fatalf("bases 应重定为权威版本 2，得 %s", payload)
	}
}

func TestBuildBatchDefersNotesOnUnreadyFolder(t *testing.T) {
	a := newStore(t)
	// 父夹未就绪：open 冲突挂着（真机 seq10 形态）
	if _, err := a.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, kind, status, local_payload, remote_payload, base_version, remote_version)
		VALUES ('f-dep', 'FOLDER', 'CREATE', 'PUSH_CONFLICT', 'open', '{}', '{}', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	// 其下笔记 CREATE 引用 f-dep（真机 seq11 形态：unknown folder_sync_id 的来源）
	if _, err := a.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload)
		VALUES ('op-n1', 'NOTE', 'n-dep', 'CREATE', NULL,
			'{"sync_id":"n-dep","title":"n","content":"","folder_sync_id":"f-dep"}')`); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(a, NewClient("", "", ""))
	b, err := w.buildBatch()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range b.ops {
		if op.EntitySyncID == "n-dep" {
			t.Fatal("E4③：父夹未就绪时 note op 必须缓推（不得进批被服务端级联拒收）")
		}
	}
	// 父夹就绪（冲突处置完毕、无挂账）→ 下轮自动入批，零人工。
	if _, err := a.Exec(`UPDATE sync_conflicts SET status='resolved' WHERE entity_sync_id='f-dep'`); err != nil {
		t.Fatal(err)
	}
	b2, err := w.buildBatch()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range b2.ops {
		if op.EntitySyncID == "n-dep" {
			found = true
		}
	}
	if !found {
		t.Fatal("父夹就绪后 note op 应自动入批（PENDING 续推语义）")
	}
}
