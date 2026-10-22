package sync

// purge_push_test.go —— E8 双向闭环回归钉（2026-09-23 用户实测缺口）：
// 桌面端彻底删/清空回收站推送 PURGE 的服务端应用面（此前只有服务端→客户端单向）。

import (
	"testing"

	"stickynote/database"
)

// purgeOp 构造 PURGE op（无 base——目标态删除不需要 CAS 基点；payload 只需 sync_id）。
func purgeOp(opID, syncID string) PushOp {
	return PushOp{OpID: opID, EntityType: "NOTE", EntitySyncID: syncID, Operation: "PURGE",
		Payload: mustJSON(noteP{"sync_id": syncID})}
}

func TestPushPurgeRemovesTrashedNote(t *testing.T) {
	r := setupSyncDB(t)
	// 建 → 软删（回收站）→ PURGE 物理删
	if res := doPush(t, r, "d1", createOp("op-c1", "s-purge", "将被彻底删")); res[0].Code != CodeAcked {
		t.Fatalf("create: %+v", res)
	}
	var ver int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE sync_id='s-purge'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	delBase := ver
	del := PushOp{OpID: "op-d1", EntityType: "NOTE", EntitySyncID: "s-purge", Operation: "DELETE",
		BaseVersion: &delBase, Payload: mustJSON(noteP{"sync_id": "s-purge"})}
	if res := doPush(t, r, "d1", del); res[0].Code != CodeAcked {
		t.Fatalf("soft delete: %+v", res)
	}
	res := doPush(t, r, "d1", purgeOp("op-p1", "s-purge"))
	if res[0].Code != CodeAcked {
		t.Fatalf("E8：PURGE 应 ACKED，得 %+v", res[0])
	}
	var n int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id='s-purge'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("E8：PURGE 必须物理删除服务端行（回收站不再保留）")
	}
	// 事件面：PURGE 事件已产出（供他端清理），operation=PURGE
	var evN int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_changes WHERE operation='PURGE'`).Scan(&evN); err != nil {
		t.Fatal(err)
	}
	if evN != 1 {
		t.Fatalf("E8：应产 1 条 PURGE 事件（他端据此清理），得 %d", evN)
	}
}

func TestPushPurgeRejectsLiveNote(t *testing.T) {
	r := setupSyncDB(t)
	if res := doPush(t, r, "d1", createOp("op-c2", "s-live", "活行")); res[0].Code != CodeAcked {
		t.Fatalf("create: %+v", res)
	}
	// 活行（未软删）直接 PURGE → 拒绝（防误删）
	res := doPush(t, r, "d1", purgeOp("op-p2", "s-live"))
	if res[0].Code != CodeConflict {
		t.Fatalf("E8：活行 PURGE 应 CONFLICT 拒收，得 %+v", res[0])
	}
	var n int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id='s-live'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("E8：被拒的 PURGE 不得删掉活行")
	}
}

func TestPushPurgeIdempotentOnMissing(t *testing.T) {
	r := setupSyncDB(t)
	// 行不存在（已删过）→ 幂等重放（目标态达成），不得报错
	res := doPush(t, r, "d1", purgeOp("op-p3", "s-never"))
	if res[0].Code != CodeIdempotentReplay {
		t.Fatalf("E8：不存在行的 PURGE 应 IDEMPOTENT_REPLAY，得 %+v", res[0])
	}
}
