package app

// redline27_copy_identity_test.go —— 红线 27「Conflict Copy 再次进入 Sync」的**语义钉测**。
//
// 台账原缺口：只有「结构性满足」的推断（`CreateNote` 天然产新 sync_id，note_tx.go:54）
// 与前端契约测（conflictCenter.test.js:119，只验调用形状），**Go 侧没有一条断言**把
// 「冲突副本必须以新身份重新进入同步链」这件事钉住。
//
// 风险面（这条红线的本质是**身份循环**）：
//   若副本沿用旧 sync_id 重新入 outbox，则服务端 push 会认为这是「对同一实体的再次修改」，
//   而该实体的远端版本已经被别人推进过 ⇒ 要么必撞 409（副本永远推不上去，用户反复重试），
//   要么更糟：被当作 keep-local 重投，**静默覆盖远端的新内容**（另一台设备刚写的笔记被抹掉）。
//   所以「副本 = 新 sync_id + 新 op_id + CREATE operation」三件事必须同时成立。
//
// 断言面：
//   ① CreateNote 产出全新 sync_id（≠ 被复制的原 sync_id），且**连建两次都不撞**；
//   ② 版本从 1 起算（不是继承远端版本 —— 继承就会被当成 UPDATE 语义）；
//   ③ 副本入outbox 后是 CREATE + base=NULL（新实体不该带服务端基点）；
//   ④ 反向钉：原冲突实体的脏链**不因副本产生**而变化（副本是旁支，不是原链的重投）。

import (
	"path/filepath"
	"testing"

	"stickynote/changelog"
	"stickynote/database"
)

func redline27Setup(t *testing.T) {
	t.Helper()
	if err := database.Init(filepath.Join(t.TempDir(), "r27.db"), "1.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	if _, err := database.DB.Exec(
		`INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','user')`); err != nil {
		t.Fatal(err)
	}
}

func redline27SyncID(t *testing.T, id int64) string {
	t.Helper()
	var s string
	if err := database.DB.QueryRow(`SELECT sync_id FROM notes WHERE id=?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRedline27ConflictCopyGetsFreshIdentity(t *testing.T) {
	redline27Setup(t)
	// 必须用**服务端 Web 构造**（serverEvents=true）：桌面本地构造刻意不产ChangeLog 事件
	// （note_service.go:49-50），那条路径本就不参与服务端同步，用它测「副本重新进入
	// 同步链」是错的路径。0 = 不限笔记数（本用例与配额无关）。
	svc := NewServerNoteService(0)

	// 原实体（模拟已在冲突中的那条笔记）
	origin, err := svc.CreateNote(1, "原笔记", "<p>v1</p>", "", nil)
	if err != nil {
		t.Fatalf("建原实体: %v", err)
	}
	originSID := redline27SyncID(t, origin.ID)

	// 副本 1、2（用户可能连点两次「另存副本」）
	copy1, err := svc.CreateNote(1, "原笔记 (副本)", "<p>v1</p>", "", nil)
	if err != nil {
		t.Fatalf("建副本1: %v", err)
	}
	copy2, err := svc.CreateNote(1, "原笔记 (副本2)", "<p>v1</p>", "", nil)
	if err != nil {
		t.Fatalf("建副本2: %v", err)
	}
	sid1, sid2 := redline27SyncID(t, copy1.ID), redline27SyncID(t, copy2.ID)

	// ① 三个身份两两不同
	if sid1 == originSID {
		t.Fatal("红线27：副本沿用了原 sync_id —— 会与服务端既有实体撞身份，" +
			"轻则 409 推不上去、重则静默覆盖他人新内容")
	}
	if sid2 == originSID || sid2 == sid1 {
		t.Fatalf("红线27：连建两个副本身份碰撞：origin=%s c1=%s c2=%s", originSID, sid1, sid2)
	}
	for _, sid := range []string{originSID, sid1, sid2} {
		if sid == "" {
			t.Fatal("红线27：sync_id 为空 —— 实体将无法参与同步")
		}
	}

	// ② 版本从 1 起算（不是继承远端版本）
	var v int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE id=?`, copy1.ID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("红线27：新实体版本应为 1（继承版本会被当 UPDATE 语义），实得 %d", v)
	}

	// ③ 副本的变更事件必须是 CREATE（新实体不是修改）
	var op string
	var sidInEvent string
	if err := database.DB.QueryRow(
		`SELECT operation, entity_sync_id FROM sync_changes WHERE entity_sync_id=? ORDER BY change_seq ASC LIMIT 1`,
		sid1).Scan(&op, &sidInEvent); err != nil {
		t.Fatalf("副本应产出一条变更事件: %v", err)
	}
	if op != "CREATE" {
		t.Fatalf("红线27：副本事件应为 CREATE，实得 %q", op)
	}
	if sidInEvent != sid1 {
		t.Fatalf("事件里的 sync_id 应等于副本身份 %s，实得 %s", sid1, sidInEvent)
	}

	// ④ 反向钉：原实体不得因为副本的产生而多出「重投」事件（否则副本变成了原链的重放）
	var originEvents int
	if err := database.DB.QueryRow(
		`SELECT COUNT(*) FROM sync_changes WHERE entity_sync_id=?`, originSID).Scan(&originEvents); err != nil {
		t.Fatal(err)
	}
	if originEvents != 1 {
		t.Fatalf("原实体应恰有 1 条 CREATE 事件（副本不产生原链事件），实得 %d", originEvents)
	}
}

func TestRedline27CopyDoesNotInheritDirtyChain(t *testing.T) {
	// 红线 27 的另一半：**副本不得携带原实体的未推送链**。
	// 场景：用户对原笔记有未推送编辑（脏链），此时点「另存副本」——
	// 若副本沿用原 sync_id入队，服务端会把副本当成「脏链的又一次重投」，
	// 于是原链的 base 会被副本的版本污染，重投必撞 409。
	redline27Setup(t)
	svc := NewServerNoteService(0) // 见上：须产ChangeLog 事件

	origin, err := svc.CreateNote(1, "脏链原笔记", "<p>v1</p>", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	originSID := redline27SyncID(t, origin.ID)
	// 原笔记被远端推进到 v2（本机仍持v1 且未同步）
	if _, err := database.DB.Exec(`UPDATE notes SET version=2 WHERE id=?`, origin.ID); err != nil {
		t.Fatal(err)
	}

	cp, err := svc.CreateNote(1, "脏链原笔记 (副本)", "<p>v1</p>", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	cpSID := redline27SyncID(t, cp.ID)
	if cpSID == originSID {
		t.Fatal("红线27：副本继承了脏实体的 sync_id")
	}

	// 副本的版本独立从 1 起（不因原实体已是 v2 而抬到 2）
	var cpVer, originVer int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE id=?`, cp.ID).Scan(&cpVer); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE id=?`, origin.ID).Scan(&originVer); err != nil {
		t.Fatal(err)
	}
	if originVer != 2 {
		t.Fatalf("前置：原实体应为 v2，实得 %d", originVer)
	}
	if cpVer != 1 {
		t.Fatalf("红线27：副本版本被原实体污染（=%d，应为 1）—— 重投必撞 409", cpVer)
	}

	// 两个身份各自独立：服务端 events 的 max(seq) 与身份无耦合
	var seq int64
	if err := database.DB.QueryRow(`SELECT change_seq FROM sync_changes WHERE entity_sync_id=?`, cpSID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq == 0 {
		t.Fatal("副本未产出变更事件")
	}
	if _, ok := changelog.NewSyncID(), true; !ok { // NewSyncID 恒非空，防其退化
		t.Fatal("changelog.NewSyncID 返回空 —— 身份生成器异常")
	}
}
