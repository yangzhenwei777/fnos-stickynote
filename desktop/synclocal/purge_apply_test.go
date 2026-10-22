package synclocal

// purge_apply_test.go —— E7 回收站同步回归钉（2026-09-23 用户产品决策）：
// 服务端永久删（彻底删/清空回收站）发 PURGE 事件，客户端必须**物理清理本地行**、
// 撤销该实体挂账、并把 open 冲突自动收口——否则本地永远留僵尸行、恢复必撞 ENTITY_GONE。

import (
	"encoding/json"
	"testing"

	"stickynote/syncspec"
)

func TestApplyPurgeRemovesLocalRowAndSettles(t *testing.T) {
	a := newStore(t)
	// 本地一条"回收站里的"笔记 + 用户对它的一笔重试挂账 + 一条 open 冲突（真机第六轮形态）
	if _, err := a.Exec(`INSERT INTO notes (id, user_id, title, content, content_text, tags, sync_id, version, deleted_at)
		VALUES (101, 1, '被远端彻底删的', '<p>x</p>', 'x', '', 'purge-sid', 2, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload, status)
		VALUES ('op-purge', 'NOTE', 'purge-sid', 'RESTORE', 2, '{"sync_id":"purge-sid"}', 'FAILED')`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, kind, status, local_payload, remote_payload, base_version, remote_version)
		VALUES ('purge-sid', 'NOTE', 'RESTORE', 'ENTITY_GONE', 'open', '{\u0022sync_id\u0022:\u0022purge-sid\u0022}', '', 2, 0)`); err != nil {
		t.Fatal(err)
	}

	sid := "purge-sid"
	w := NewWorker(a, NewClient("", "", "devA"))
	raw, _ := json.Marshal(map[string]any{"sync_id": sid})
	if _, err := w.applyChange(syncspec.ChangeDTO{
		ChangeSeq: 99, EntityType: "NOTE", EntitySyncID: &sid,
		Operation: syncspec.OperationPurge, ServerVersion: 2, Payload: raw,
	}, 98); err != nil {
		t.Fatalf("applyChange(PURGE): %v", err)
	}

	var n int
	if err := a.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id = 'purge-sid'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("E7：PURGE 必须物理删除本地行（回收站不再留僵尸）")
	}
	var pend int
	if err := a.QueryRow(`SELECT COUNT(*) FROM sync_outbox
		WHERE entity_sync_id='purge-sid' AND status IN ('PENDING','FAILED','SENDING')`).Scan(&pend); err != nil {
		t.Fatal(err)
	}
	if pend != 0 {
		t.Fatal("E7：PURGE 必须撤销该实体的未收账挂账（否则空转重试直到冲突）")
	}
	var open int
	if err := a.QueryRow(`SELECT COUNT(*) FROM sync_conflicts
		WHERE entity_sync_id='purge-sid' AND status='open'`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatal("E7：远端已永久消失，open 冲突须自动收口")
	}
}
