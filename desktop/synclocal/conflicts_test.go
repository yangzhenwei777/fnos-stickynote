// conflicts_test.go —— 冲突可见面与处置面（批次8-Alt 刀1）。
//
// 覆盖：旧库补列、处置矩阵（唯一事实源的定向测）、列表分页与挂账形状、
// keep-local 重投收敛（集成，经真服务端）、keep-remote 应用远端（含"未确认必拒"反向）、
// 遗留行不猜语义、二次处置幂等、重试耗尽/黑洞补记。
//
// 已知同语义副作用（与在线 pull 完全一致，非刀1 新增）：keep-remote 用远端引用集覆盖本机，
// 故该笔记**尚未推送**的附件引用会被丢弃（文件留在 uploads 目录，随 GC 回收）。
package synclocal

import (
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"stickynote-desktop/localstore"
	"stickynote/database"
)

// ---- 旧库补列 ----

const legacyConflictsDDL = `
CREATE TABLE sync_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE sync_outbox (
	seq INTEGER PRIMARY KEY AUTOINCREMENT, op_id TEXT NOT NULL UNIQUE, entity_type TEXT NOT NULL,
	entity_sync_id TEXT NOT NULL, operation TEXT NOT NULL, base_version INTEGER, payload TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'PENDING', retry_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE sync_conflicts (
	id INTEGER PRIMARY KEY AUTOINCREMENT, entity_sync_id TEXT NOT NULL, kind TEXT NOT NULL,
	local_payload TEXT NOT NULL DEFAULT '', remote_payload TEXT NOT NULL DEFAULT '',
	base_version INTEGER NOT NULL DEFAULT 0, remote_version INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);`

func TestConflictSchemaMigration(t *testing.T) {
	db, err := localstoreOpen(t)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(clientDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(legacyConflictsDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sync_conflicts (entity_sync_id, kind, remote_payload, remote_version)
		VALUES ('s-old','CONFLICT_ON_PULL','{}',5)`); err != nil {
		t.Fatal(err)
	}
	// 启动建表入口必须把六列补齐，且既有行按默认值变成可识别的未处置遗留行。
	if err := EnsureTables(db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTables(db); err != nil { // 幂等：第二次不得因列已存在而报错
		t.Fatalf("EnsureTables 二次运行必须幂等: %v", err)
	}
	for _, col := range []string{"entity_type", "operation", "op_id", "status", "resolution", "resolved_at"} {
		have, err := conflictColumns(db)
		if err != nil {
			t.Fatal(err)
		}
		if !have[col] {
			t.Fatalf("补列缺失 %s", col)
		}
	}
	var st string
	if err := db.QueryRow(`SELECT status FROM sync_conflicts WHERE entity_sync_id='s-old'`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "open" {
		t.Fatalf("遗留行 status 必须为 open，得 %q", st)
	}
	// 旧行没有操作类型 → 处置面必须拒（见 TestResolveLegacyRowRefusesToGuess）。
}

// ---- 处置矩阵（唯一事实源）----

func TestAllowedResolutionsMatrix(t *testing.T) {
	// 1.0.218：recreate 可用性看本机行——先种三份样本（活笔记 / 回收站笔记 / 活文件夹）。
	db := newStore(t)
	if _, err := db.Exec(`INSERT INTO notes (id, user_id, title, content, content_text, tags, sync_id)
		VALUES (100, 1, '活', '<p>x</p>', 'x', '', 's-live-note')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notes (id, user_id, title, content, content_text, tags, sync_id, deleted_at)
		VALUES (101, 1, '删', '<p>x</p>', 'x', '', 's-trash-note', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO note_folders (id, user_id, name, sort_order, sync_id, version)
		VALUES (100, 1, '夹', 1, 's-live-folder', 1)`); err != nil {
		t.Fatal(err)
	}

	noteRemote := `{"sync_id":"s1","title":"远端"}`
	cases := []struct {
		name, kind, etype, syncID, oper, remote string
		want                                    []string
	}{
		{"脏实体撞远端更新", KindConflictOnPull, "NOTE", "s-live-note", "UPDATE", noteRemote,
			[]string{ResolveKeepLocal, ResolveKeepRemote, ResolveDropLocal}},
		{"文件夹同理", KindConflictOnPull, "FOLDER", "s-live-folder", "UPDATE", noteRemote,
			[]string{ResolveKeepLocal, ResolveKeepRemote, ResolveDropLocal}},
		{"集合排序只许本地优先", KindConflictOnPull, "FOLDER_ORDER", "", "UPDATE", noteRemote,
			[]string{ResolveKeepLocal}},
		{"遗留行缺操作类型不给覆盖入口", KindConflictOnPull, "NOTE", "s-live-note", "", noteRemote,
			[]string{ResolveKeepLocal, ResolveDropLocal}},
		{"实体类型未知不给覆盖入口", KindConflictOnPull, "", "", "UPDATE", noteRemote,
			[]string{ResolveKeepLocal, ResolveDropLocal}},
		{"遗留行无远端内容不给覆盖入口", KindConflictOnPull, "NOTE", "s-live-note", "UPDATE", "",
			[]string{ResolveKeepLocal, ResolveDropLocal}},
		{"push 409 可重投可放弃", KindPushConflict, "NOTE", "s-live-note", "UPDATE", "",
			[]string{ResolveKeepLocal, ResolveDropLocal}},
		{"重试耗尽同上", KindRetryExhausted, "NOTE", "s-live-note", "UPDATE", "",
			[]string{ResolveKeepLocal, ResolveDropLocal}},
		// 1.0.218：ENTITY_GONE 面按"本机行在否"分流。
		{"实体已被删·本机行在→可重建可放弃", KindEntityGone, "NOTE", "s-live-note", "UPDATE", "",
			[]string{ResolveRecreate, ResolveDropLocal}},
		{"实体已被删·文件夹本机在→可重建", KindEntityGone, "FOLDER", "s-live-folder", "UPDATE", "",
			[]string{ResolveRecreate, ResolveDropLocal}},
		{"实体已被删·本机无行→只可放弃", KindEntityGone, "NOTE", "s-gone-note", "UPDATE", "",
			[]string{ResolveDropLocal}},
		{"实体已被删·本机行在回收站→只可放弃", KindEntityGone, "NOTE", "s-trash-note", "UPDATE", "",
			[]string{ResolveDropLocal}},
		{"实体已被删·无 sync_id 遗留行→只可放弃", KindEntityGone, "NOTE", "", "UPDATE", "",
			[]string{ResolveDropLocal}},
		{"op 身份冲突只可放弃（recreate 不得蔓延）", KindStale, "NOTE", "s-live-note", "UPDATE", "",
			[]string{ResolveDropLocal}},
		{"附件分歧重投必再撞", KindAttachmentDiverged, "NOTE", "s-live-note", "UPDATE", "",
			[]string{ResolveDropLocal}},
		{"未知 kind 保守只放弃", "WHATEVER", "NOTE", "s-live-note", "UPDATE", "",
			[]string{ResolveDropLocal}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allowedResolutions(db, tc.kind, tc.etype, tc.syncID, tc.oper, tc.remote)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("允许组合不符\n got=%v\nwant=%v", got, tc.want)
			}
		})
	}
}

// ---- 列表与挂账形状 ----

func TestListConflictsPaged(t *testing.T) {
	db := newStore(t)
	for i := 0; i < 3; i++ {
		if err := insertSampleConflict(db, "NOTE", "UPDATE", KindPushConflict); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertSampleConflict(db, "NOTE", "UPDATE", KindEntityGone); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sync_conflicts SET status='resolved', resolution='drop-local',
		resolved_at='2026-09-21 00:00:00' WHERE kind='ENTITY_GONE'`); err != nil {
		t.Fatal(err)
	}
	// 制造该实体 2 笔未推 op，验证 OpenOps 计数口径（含 CONFLICT 链）
	if _, err := db.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload, status)
		VALUES ('o1','NOTE','s-list','UPDATE',1,'{}','PENDING'),('o2','NOTE','s-list','DELETE',NULL,'{}','CONFLICT')`); err != nil {
		t.Fatal(err)
	}

	open, total, err := ListConflicts(db, true, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(open) != 3 {
		t.Fatalf("onlyOpen 必须排除已处置项：len=%d total=%d", len(open), total)
	}
	if open[0].CreatedAt == "" {
		t.Fatal("created_at 必须回传供 UI 排序展示")
	}
	if open[0].OpenOps != 2 {
		t.Fatalf("OpenOps 应含 PENDING+CONFLICT 两笔，得 %d", open[0].OpenOps)
	}
	if len(open[0].AllowedResolutions) == 0 {
		t.Fatal("allowed_resolutions 必须随行走，前端据此渲染按钮")
	}
	all, allTotal, err := ListConflicts(db, false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if allTotal != 4 || len(all) != 4 {
		t.Fatalf("含已处置应 4 条，得 len=%d total=%d", len(all), allTotal)
	}
	// 分页
	page2, _, err := ListConflicts(db, true, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 {
		t.Fatalf("offset 分页错乱，得 %d", len(page2))
	}
	// limit 上限钉死（防大 payload 一次过桥）
	if _, _, err := ListConflicts(db, false, 100000, 0); err != nil {
		t.Fatal(err)
	}
}

func TestListOutboxShapesAndNullBase(t *testing.T) {
	db := newStore(t)
	// base_version 允许 NULL（规范 CREATE 与 FOLDER_ORDER，§48）——批次6 的 NULL-base 事故同源。
	if _, err := db.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload, status, retry_count, last_error)
		VALUES ('n1','NOTE','','CREATE',NULL,'{}','PENDING',0,''),
		       ('f1','FOLDER_ORDER','','UPDATE',NULL,'{}','FAILED',3,'ATTACHMENT_NOT_FOUND'),
		       ('n2','NOTE','s-x','UPDATE',7,'{}','ACKED',0,'merged')`); err != nil {
		t.Fatal(err)
	}
	ops, counts, total, err := ListOutbox(db, nil, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(ops) != 3 {
		t.Fatalf("全量应 3 条，得 total=%d len=%d", total, len(ops))
	}
	if counts["PENDING"] != 1 || counts["FAILED"] != 1 || counts["ACKED"] != 1 {
		t.Fatalf("按状态计数错误：%+v", counts)
	}
	for _, o := range ops {
		if o.OpID == "n1" || o.OpID == "f1" {
			if o.BaseVersion != nil {
				t.Fatalf("NULL base 必须扫成 nil，得 %+v", o.BaseVersion)
			}
		}
		if o.OpID == "n2" && (o.BaseVersion == nil || *o.BaseVersion != 7) {
			t.Fatalf("base=7 未读出，得 %+v", o.BaseVersion)
		}
		if o.PayloadBytes == 0 {
			t.Fatal("payload 体积须回传（UI 提示用，不传 payload 本体）")
		}
	}
	only, _, n, err := ListOutbox(db, []string{"FAILED", "PENDING"}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(only) != 2 {
		t.Fatalf("状态过滤失效，得 n=%d len=%d", n, len(only))
	}
}

// ---- 处置：keep-remote（含反向验证）----

// pullDirtyConflict 造一条 CONFLICT_ON_PULL：B 脏、A 已把远端推到 v2。
type conflictFixture struct {
	db          *sql.DB
	w           *Worker
	ts          *httptest.Server
	remoteTitle string // A 推上去、B 尚未看到的内容
	id          int64  // 冲突行 id
}

func pullDirtyConflict(t *testing.T) conflictFixture {
	t.Helper()
	ts := newServer(t)
	a, bb := newStore(t), newStore(t)
	localCreate(t, a, "s-cr", "母版")
	mustSync(t, a, ts, "devA")
	mustSync(t, bb, ts, "devB")

	var bid int64
	if err := bb.QueryRow(`SELECT id FROM notes WHERE sync_id='s-cr'`).Scan(&bid); err != nil {
		t.Fatal(err)
	}
	localEdit(t, bb, bid, "B 本机未同步")
	localEdit(t, a, idOf(t, a, "s-cr"), "A 已上推")
	mustSync(t, a, ts, "devA")

	worker := NewWorker(bb, NewClient(ts.URL, "tok", "devB"))
	worker.AttachDir = t.TempDir()
	if _, err := worker.RunOnce(nilCtx()); err != nil {
		t.Fatal(err)
	}
	var cid int64
	var remote string
	if err := bb.QueryRow(`SELECT id FROM sync_conflicts WHERE kind='CONFLICT_ON_PULL'`).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, bb, `SELECT COUNT(*) FROM sync_conflicts`); n != 1 {
		t.Fatalf("应恰好 1 条冲突，得 %d", n)
	}
	if err := database.DB.QueryRow(`SELECT title FROM notes WHERE sync_id='s-cr'`).Scan(&remote); err != nil {
		t.Fatal(err)
	}
	return conflictFixture{db: bb, w: worker, ts: ts, remoteTitle: remote, id: cid}
}

func TestResolveKeepRemoteAppliesServerContent(t *testing.T) {
	f := pullDirtyConflict(t)
	db, w, cid, want := f.db, f.w, f.id, f.remoteTitle
	var curBefore string
	_ = db.QueryRow(`SELECT value FROM sync_state WHERE key='cursor'`).Scan(&curBefore)

	res, err := w.ResolveConflict(cid, ResolveKeepRemote, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AppliedRemote || res.TerminatedOps == 0 {
		t.Fatalf("处置回执异常: %+v", res)
	}
	var title string
	var ver int64
	if err := db.QueryRow(`SELECT title, version FROM notes WHERE sync_id='s-cr'`).Scan(&title, &ver); err != nil {
		t.Fatal(err)
	}
	if title != want {
		t.Fatalf("keep-remote 必须把远端内容落到本机，得 %q 期望 %q", title, want)
	}
	if ver != 2 {
		t.Fatalf("行版本须对齐远端 v2，得 %d", ver)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_outbox WHERE entity_sync_id='s-cr'
		AND status IN ('PENDING','FAILED','CONFLICT','SENDING')`); n != 0 {
		t.Fatalf("本机那笔未推 op 必须被终止，仍开着的有 %d", n)
	}
	// cursor 不得被处置动作推进（远端变更早在冲突时已消费）
	var curAfter string
	_ = db.QueryRow(`SELECT value FROM sync_state WHERE key='cursor'`).Scan(&curAfter)
	if curAfter != curBefore {
		t.Fatalf("处置不得二次消费事件流：cursor %q → %q", curBefore, curAfter)
	}
	// 冲突转已处置且计数口径改变
	var st, resolution string
	if err := db.QueryRow(`SELECT status, resolution FROM sync_conflicts WHERE id=?`, cid).Scan(&st, &resolution); err != nil {
		t.Fatal(err)
	}
	if st != "resolved" || resolution != ResolveKeepRemote {
		t.Fatalf("处置态未落表: %s/%s", st, resolution)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`); n != 0 {
		t.Fatalf("待处置计数应为 0，得 %d", n)
	}
	st2, err := Status(db)
	if err != nil {
		t.Fatal(err)
	}
	if st2["conflict_count"] != 0 || st2["conflict_total"] != 1 {
		t.Fatalf("conflict_count 须只算未处置： %+v", st2)
	}
	// 再同步一轮：既不得把已放弃的本地内容推回去，也不得改动服务端
	got := mustSync(t, db, f.ts, "devB")
	if got.Pushed != 0 {
		t.Fatalf("处置后不应再有推送，得 %+v", got)
	}
	if srv := serverTitle(t, "s-cr"); srv != want {
		t.Fatalf("服务端内容被处置改写：%q", srv)
	}
}

// TestResolveKeepRemoteRequiresConfirmation 是红线 1/6 的反向硬钉：未确认必拒且零改动。
func TestResolveKeepRemoteRequiresConfirmation(t *testing.T) {
	f := pullDirtyConflict(t)
	db, w, cid := f.db, f.w, f.id
	var title string
	if err := db.QueryRow(`SELECT title FROM notes WHERE sync_id='s-cr'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	before := countRows(t, db, `SELECT COUNT(*) FROM sync_outbox WHERE status IN ('PENDING','FAILED','CONFLICT')`)
	outboxRowsBefore := countRows(t, db, `SELECT COUNT(*) FROM sync_outbox`)

	_, err := w.ResolveConflict(cid, ResolveKeepRemote, false)
	if !errors.Is(err, ErrResolveRequiresConfirm) {
		t.Fatalf("未确认的覆盖型处置必须被拒，得 %v", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM notes WHERE sync_id='s-cr'`); got != 1 {
		t.Fatalf("笔记行数量不该被动过，得 %d", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM sync_outbox`); got != outboxRowsBefore {
		t.Fatalf("未确认处置不得增删 outbox 行：%d → %d", outboxRowsBefore, got)
	}
	var afterTitle string
	_ = db.QueryRow(`SELECT title FROM notes WHERE sync_id='s-cr'`).Scan(&afterTitle)
	if afterTitle != title {
		t.Fatalf("未确认处置竟改了内容：%q → %q", title, afterTitle)
	}
	if after := countRows(t, db, `SELECT COUNT(*) FROM sync_outbox WHERE status IN ('PENDING','FAILED','CONFLICT')`); after != before {
		t.Fatalf("未确认处置不得流转 outbox 态：%d → %d", before, after)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`); n != 1 {
		t.Fatalf("被拒的处置不该留下已处置记录，open=%d", n)
	}
}

// ---- 处置：keep-local（集成收敛）----

func TestResolveKeepLocalRequeuesAndConverges(t *testing.T) {
	f := pullDirtyConflict(t)
	db, w, cid := f.db, f.w, f.id
	res, err := w.ResolveConflict(cid, ResolveKeepLocal, false) // keep-local 非破坏性，无需确认
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeuedOps == 0 {
		t.Fatalf("keep-local 必须把脏链升回 PENDING: %+v", res)
	}
	// 基点必须已重定到远端 v2（否则重投必再撞 409 —— 与 §50 同一口径）
	var base sql.NullInt64
	if err := db.QueryRow(`SELECT base_version FROM sync_outbox WHERE entity_sync_id='s-cr' AND status='PENDING'`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if !base.Valid || base.Int64 != 2 {
		t.Fatalf("重投基点应为远端 v2，得 %+v", base)
	}
	// 下一轮同步：本机内容以 v3 胜出
	got := mustSync(t, db, f.ts, "devB")
	if got.Pushed == 0 {
		t.Fatalf("重投后应有推送： %+v", got)
	}
	if srv := serverTitle(t, "s-cr"); srv != "B 本机未同步" {
		t.Fatalf("keep-local 未让本机内容胜出，服务端=%q", srv)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`); n != 0 {
		t.Fatalf("处置后待处置计数应为 0，得 %d", n)
	}
}

// ---- 处置：幂等、矩阵拒绝、遗留行 ----

func TestResolveTwiceIsIdempotent(t *testing.T) {
	f := pullDirtyConflict(t)
	db, w, cid, want := f.db, f.w, f.id, f.remoteTitle
	if _, err := w.ResolveConflict(cid, ResolveKeepRemote, true); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ResolveConflict(cid, ResolveKeepRemote, true); !errors.Is(err, ErrConflictAlreadyResolved) {
		t.Fatalf("二次处置必须报已处置，得 %v", err)
	}
	if _, err := w.ResolveConflict(cid, ResolveKeepLocal, false); !errors.Is(err, ErrConflictAlreadyResolved) {
		t.Fatalf("已处置行换种方式仍须拒，得 %v", err)
	}
	// 二次处置（被拒）不得改写内容
	if title := localTitle(t, db, "s-cr"); title != want {
		t.Fatalf("被拒的二次处置改了本机内容：%q", title)
	}
}

func TestResolveUnsupportedAndUnknown(t *testing.T) {
	db := newStore(t)
	w := NewWorker(db, NewClient("", "", ""))
	insert := func(kind, etype, oper, remote string) int64 {
		if err := insertSampleConflict(db, etype, oper, kind); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE sync_conflicts SET remote_payload=?, operation=? WHERE id=(SELECT MAX(id) FROM sync_conflicts)`,
			remote, oper); err != nil {
			t.Fatal(err)
		}
		var id int64
		_ = db.QueryRow(`SELECT MAX(id) FROM sync_conflicts`).Scan(&id)
		return id
	}
	// 集合排序冲突不接受 keep-remote
	orderID := insert(KindConflictOnPull, "FOLDER_ORDER", "UPDATE", `{"order":["f1"],"versions":[{"sync_id":"f1","version":9}]}`)
	if _, err := w.ResolveConflict(orderID, ResolveKeepRemote, true); !errors.Is(err, ErrResolveUnsupported) {
		t.Fatalf("FOLDER_ORDER 不接受 keep-remote，得 %v", err)
	}
	// 实体已没不接受 keep-local（重投必再撞）
	goneID := insert(KindEntityGone, "NOTE", "UPDATE", "")
	if _, err := w.ResolveConflict(goneID, ResolveKeepLocal, false); !errors.Is(err, ErrResolveUnsupported) {
		t.Fatalf("ENTITY_GONE 不接受 keep-local，得 %v", err)
	}
	// 未知处置方式
	if _, err := w.ResolveConflict(goneID, "wing-wang", true); !errors.Is(err, ErrResolveUnknown) {
		t.Fatalf("未知处置方式必须拒，得 %v", err)
	}
	// 不存在的记录
	if _, err := w.ResolveConflict(9999, ResolveKeepLocal, false); !errors.Is(err, ErrConflictNotFound) {
		t.Fatalf("缺记录须报 ErrConflictNotFound，得 %v", err)
	}
	// 遗留行：有远端内容但无操作类型 → 拒，并说明真因
	legacy := insert(KindConflictOnPull, "", "UPDATE", `{"sync_id":"s9"}`)
	if _, err := db.Exec(`UPDATE sync_conflicts SET entity_sync_id='s-nosuch' WHERE id=?`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ResolveConflict(legacy, ResolveKeepRemote, true); !errors.Is(err, ErrResolveUnsupported) {
		t.Fatalf("信息不全的遗留行不得自动覆盖本机，得 %v", err)
	}
	// drop-local 兜底可用（放弃这笔，内容不动）
	if _, err := w.ResolveConflict(goneID, ResolveDropLocal, false); err != nil {
		t.Fatalf("ENTITY_GONE 应允许 drop-local: %v", err)
	}
}

// ---- 黑洞补记 ----

func TestReconcileExhaustedRecordsConflict(t *testing.T) {
	db := newStore(t)
	w := NewWorker(db, NewClient("", "", ""))
	if _, err := db.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload, status, retry_count, last_error)
		VALUES ('e1','NOTE','s-e','UPDATE',3,'{"sync_id":"s-e"}','FAILED',3,'ATTACHMENT_NOT_FOUND'),
		       ('e2','NOTE','s-e2','UPDATE',1,'{"sync_id":"s-e2"}','FAILED',1,'')`); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcileOutboxRetries(); err != nil {
		t.Fatal(err)
	}
	// 达上限的转 CONFLICT 并补记冲突；未达上限的仍按原逻辑升回 PENDING。
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_outbox WHERE op_id='e1' AND status='CONFLICT'`); n != 1 {
		t.Fatalf("e1 应转 CONFLICT，得 %d", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_outbox WHERE op_id='e2' AND status='PENDING'`); n != 1 {
		t.Fatalf("e2 应升回 PENDING，得 %d", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE kind='RETRY_EXHAUSTED' AND op_id='e1'`); n != 1 {
		t.Fatalf("重试耗尽必须补记冲突资产（可见面黑洞），得 %d", n)
	}
	var etype, oper string
	if err := db.QueryRow(`SELECT entity_type, operation FROM sync_conflicts WHERE op_id='e1'`).Scan(&etype, &oper); err != nil {
		t.Fatal(err)
	}
	if etype != "NOTE" || oper != "UPDATE" {
		t.Fatalf("补记须带实体类型与操作，得 %s/%s", etype, oper)
	}
	// 幂等：再跑 N 轮不得刷屏
	for i := 0; i < 3; i++ {
		if _, err := db.Exec(`UPDATE sync_outbox SET status='FAILED' WHERE op_id='e1'`); err != nil {
			t.Fatal(err)
		}
		if err := w.reconcileOutboxRetries(); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sync_conflicts WHERE kind='RETRY_EXHAUSTED'`); n != 1 {
		t.Fatalf("同一 op 同类冲突只能记一次，得 %d", n)
	}
}

// TestResolveKeepRemoteRefusesWhenLocalRowGone 钉住 applyXxxChange 的"缺失行即补建"语义
// 不能被处置面复用：用户已彻底删除该笔记时，接受远端不得凭空造出一条来。
func TestResolveKeepRemoteRefusesWhenLocalRowGone(t *testing.T) {
	f := pullDirtyConflict(t)
	if _, err := f.db.Exec(`DELETE FROM notes WHERE sync_id='s-cr'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.ResolveConflict(f.id, ResolveKeepRemote, true); !errors.Is(err, ErrConflictEntityMissing) {
		t.Fatalf("本机行已没了必须拒（不得凭空造笔记），得 %v", err)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM notes`); n != 0 {
		t.Fatalf("被拒的处置不得留下任何笔记行，得 %d", n)
	}
	// 同一冲突改走放弃：必须放行
	if _, err := f.w.ResolveConflict(f.id, ResolveDropLocal, false); err != nil {
		t.Fatalf("放弃这笔应放行: %v", err)
	}
}

// ---- 夹具 ----

func insertSampleConflict(db *sql.DB, etype, oper, kind string) error {
	_, err := db.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, op_id, kind,
		local_payload, remote_payload, base_version, remote_version)
		VALUES ('s-list', ?, ?, '', ?, '{"title":"本机"}', '{"title":"远端"}', 1, 2)`, etype, oper, kind)
	return err
}

func localTitle(t *testing.T, db *sql.DB, syncID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT title FROM notes WHERE sync_id=?`, syncID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func serverTitle(t *testing.T, syncID string) string {
	t.Helper()
	var s string
	if err := database.DB.QueryRow(`SELECT title FROM notes WHERE sync_id=?`, syncID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func localstoreOpen(t *testing.T) (*sql.DB, error) {
	t.Helper()
	return localstore.OpenGenDB(filepath.Join(t.TempDir(), "legacy.db"))
}
