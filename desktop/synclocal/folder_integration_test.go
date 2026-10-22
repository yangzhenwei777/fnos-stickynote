package synclocal

import (
	"database/sql"
	"testing"

	"stickynote/app"
)

// folder_integration_test.go —— P1 批次2 双桌面文件夹收敛验收（§18/§55/§56 + ADR-001 集合面 + §142 客户端面）。

func localFolderCreate(t *testing.T, db *sql.DB, syncID, name string, sort int) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO note_folders (user_id, name, sort_order, sync_id, version) VALUES (1, ?, ?, ?, 1)`, name, sort, syncID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := EnqueueFolderOp(db, id, "CREATE"); err != nil {
		t.Fatal(err)
	}
	return id
}

func folderNameSort(t *testing.T, db *sql.DB, syncID string) (string, int, int64) {
	t.Helper()
	var name string
	var sort int
	var ver int64
	if err := db.QueryRow(`SELECT name, sort_order, version FROM note_folders WHERE sync_id = ?`, syncID).Scan(&name, &sort, &ver); err != nil {
		return "", -1, -1
	}
	return name, sort, ver
}

func TestTwoDesktopsFolderConvergence(t *testing.T) {
	ts := newServer(t)
	a, b := newStore(t), newStore(t)

	// A 建夹 → B 可见。
	ida := localFolderCreate(t, a, "fs-1", "A夹", 1)
	mustSync(t, a, ts, "devA")
	if got := mustSync(t, b, ts, "devB"); got.Applied != 1 {
		t.Fatalf("B 应应用 1 条 FOLDER CREATE: %+v", got)
	}
	if name, _, _ := folderNameSort(t, b, "fs-1"); name != "A夹" {
		t.Fatalf("B 未收敛文件夹: %q", name)
	}

	// A 重命名 → B 收敛（UPDATE 事件 + 版本回写）。
	if _, err := a.Exec(`UPDATE note_folders SET name='A改名' WHERE sync_id='fs-1'`); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueFolderOp(a, ida, "UPDATE"); err != nil {
		t.Fatal(err)
	}
	mustSync(t, a, ts, "devA")
	mustSync(t, b, ts, "devB")
	if name, _, ver := folderNameSort(t, b, "fs-1"); name != "A改名" || ver != 2 {
		t.Fatalf("B 未收敛重命名: %q v%d", name, ver)
	}

	// B 建第二夹并同步回 A；A 本地重排 → FOLDER_ORDER 集合事件 → B 次序收敛 + OrderVersions rebase。
	localFolderCreate(t, b, "fs-2", "B夹", 2)
	mustSync(t, b, ts, "devB")
	mustSync(t, a, ts, "devA")
	if _, err := a.Exec(`UPDATE note_folders SET sort_order = CASE sync_id WHEN 'fs-2' THEN 1 ELSE 2 END`); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueFolderOrderOp(a); err != nil {
		t.Fatal(err)
	}
	mustSync(t, a, ts, "devA")
	mustSync(t, b, ts, "devB")
	if _, sortB, _ := folderNameSort(t, b, "fs-2"); sortB != 1 {
		t.Fatalf("B 未收敛远端重排（fs-2 应第一）: sort=%d", sortB)
	}
	if _, _, ver := folderNameSort(t, a, "fs-1"); ver < 2 {
		t.Fatalf("A 侧 folder 版本未被 OrderVersions rebase: v%d", ver)
	}

	// rebase 后 A 再重命名：base 取回写后版本 → ACK 无冲突。
	if _, err := a.Exec(`UPDATE note_folders SET name='再改' WHERE sync_id='fs-1'`); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueFolderOp(a, ida, "UPDATE"); err != nil {
		t.Fatal(err)
	}
	mustSync(t, a, ts, "devA")
	var conflicts int
	if err := a.QueryRow(`SELECT COUNT(*) FROM sync_conflicts`).Scan(&conflicts); err != nil {
		t.Fatal(err)
	}
	if conflicts != 0 {
		t.Fatalf("rebase 后的 rename 不应产生冲突: %d", conflicts)
	}
	mustSync(t, b, ts, "devB")
	if name, _, _ := folderNameSort(t, b, "fs-1"); name != "再改" {
		t.Fatalf("B 未收敛 rebase 后改名: %q", name)
	}
}

func TestFolderDirtyProtectionOnPull(t *testing.T) {
	ts := newServer(t)
	a, b := newStore(t), newStore(t)
	localFolderCreate(t, a, "fs-d", "初始", 1)
	mustSync(t, a, ts, "devA")
	mustSync(t, b, ts, "devB")

	// B 本地改名（脏、未推）；A 远端改名先到服务端。
	var bid int64
	if err := b.QueryRow(`SELECT id FROM note_folders WHERE sync_id='fs-d'`).Scan(&bid); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(`UPDATE note_folders SET name='B本地改' WHERE id = ?`, bid); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueFolderOp(b, bid, "UPDATE"); err != nil {
		t.Fatal(err)
	}
	var aid int64
	if err := a.QueryRow(`SELECT id FROM note_folders WHERE sync_id='fs-d'`).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(`UPDATE note_folders SET name='A远端改' WHERE id = ?`, aid); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueFolderOp(a, aid, "UPDATE"); err != nil {
		t.Fatal(err)
	}
	mustSync(t, a, ts, "devA")

	got := mustSync(t, b, ts, "devB")
	if got.Conflicts != 1 {
		t.Fatalf("脏文件夹撞远端改名应记 1 冲突（ADR-001）: %+v", got)
	}
	if name, _, _ := folderNameSort(t, b, "fs-d"); name != "B本地改" {
		t.Fatalf("冲突不得覆盖本地未同步修改（红线 1/6）: %q", name)
	}
	var kind string
	if err := b.QueryRow(`SELECT kind FROM sync_conflicts WHERE entity_sync_id='fs-d'`).Scan(&kind); err != nil || kind != "CONFLICT_ON_PULL" {
		t.Fatalf("冲突记录不符: %v %q", err, kind)
	}
	var status string
	if err := b.QueryRow(`SELECT status FROM sync_outbox WHERE entity_sync_id='fs-d' AND status != 'ACKED' ORDER BY seq DESC LIMIT 1`).Scan(&status); err != nil || status != "CONFLICT" {
		t.Fatalf("脏链应转 CONFLICT: %v %q", err, status)
	}
}

func TestFolderOrderStaleBaseAtomic(t *testing.T) {
	ts := newServer(t)
	a, b := newStore(t), newStore(t)
	localFolderCreate(t, a, "s1", "甲", 1)
	localFolderCreate(t, a, "s2", "乙", 2)
	mustSync(t, a, ts, "devA")
	mustSync(t, b, ts, "devB")

	// B 先重排（s2 顶）→ 服务端版本推进。
	if _, err := b.Exec(`UPDATE note_folders SET sort_order = CASE sync_id WHEN 's2' THEN 1 ELSE 2 END`); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueFolderOrderOp(b); err != nil {
		t.Fatal(err)
	}
	mustSync(t, b, ts, "devB")

	// A 以陈旧 base（收敛时的 v1）重排反向序 → 整 op CONFLICT 零提交（§142 客户端面）。
	if _, err := a.Exec(`UPDATE note_folders SET sort_order = CASE sync_id WHEN 's1' THEN 1 ELSE 2 END`); err != nil {
		t.Fatal(err)
	}
	stale := `{"order":["s1","s2"],"bases":{"s1":1,"s2":1}}`
	if _, err := a.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload)
		VALUES ('ord-stale','FOLDER_ORDER','','UPDATE',NULL,?)`, stale); err != nil {
		t.Fatal(err)
	}
	mustSync(t, a, ts, "devA")
	var status string
	if err := a.QueryRow(`SELECT status FROM sync_outbox WHERE op_id='ord-stale'`).Scan(&status); err != nil || status != "CONFLICT" {
		t.Fatalf("陈旧 base 的 order op 应 CONFLICT: %v %q", err, status)
	}
	// pull 收敛 B 的权威序（order 链已转 CONFLICT 不再脏 → 事件可应用）。
	mustSync(t, a, ts, "devA")
	if _, sortA, _ := folderNameSort(t, a, "s2"); sortA != 1 {
		t.Fatalf("A 应经 pull 收敛服务端权威序: sort=%d", sortA)
	}
}

// TestWebFolderDeleteKeepsTombstoneOnDesktop —— §56 重指向事件 × 桌面墓碑保持：
// Web 删文件夹时回收站笔记的 UPDATE 事件不得把本地回收站行"复活"（applyNoteChange 修复实锤）。
func TestWebFolderDeleteKeepsTombstoneOnDesktop(t *testing.T) {
	ts := newServer(t)
	b := newStore(t)
	web := app.NewServerNoteService(100)
	fs := app.NewServerFolderService()

	f, err := fs.Create(1, "弃夹")
	if err != nil || f.ID == nil {
		t.Fatal(err)
	}
	n, err := web.CreateNote(1, "弃笔记", "c", "", f.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 桌面 B 先收敛建夹+建笔记。
	mustSync(t, b, ts, "devB")

	// Web 软删笔记 → 再删文件夹（触发 §56 重指向 UPDATE）。
	if err := web.DeleteNote(n.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete(*f.ID, 1); err != nil {
		t.Fatal(err)
	}
	// 首轮已收敛 FOLDER/NOTE CREATE 两事件；本轮剩 NOTE 墓碑 + 重指向 UPDATE + FOLDER DELETE。
	got := mustSync(t, b, ts, "devB")
	if got.Applied != 3 || got.Conflicts != 0 {
		t.Fatalf("B 本轮应应用 3 事件（墓碑+重指向+夹删除）: %+v", got)
	}
	var trashed int
	if err := b.QueryRow(`SELECT COUNT(*) FROM notes WHERE title='弃笔记' AND deleted_at IS NOT NULL AND folder_id IS NULL`).Scan(&trashed); err != nil || trashed != 1 {
		t.Fatalf("重指向 UPDATE 后笔记必须仍在回收站且改指默认夹（墓碑保持）: %v %d", err, trashed)
	}
	var folders int
	if err := b.QueryRow(`SELECT COUNT(*) FROM note_folders`).Scan(&folders); err != nil || folders != 0 {
		t.Fatalf("FOLDER DELETE 后本地夹应移除: %v %d", err, folders)
	}
}
