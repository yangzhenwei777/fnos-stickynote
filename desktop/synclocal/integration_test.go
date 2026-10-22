package synclocal

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"stickynote-desktop/localstore"
	"stickynote/app"
	"stickynote/database"
	"stickynote/model"
	syncsrv "stickynote/sync"
)

// integration_test.go —— P0-D 验收：双桌面经真服务端 sync API 收敛。
// 覆盖：A 建 → B 见（最小闭环）；双端编辑 → 后推者 CONFLICT 且本地内容不丢（红线 1/6）；
// op 重放幂等（红线 3）；pull 撞脏 → CONFLICT_ON_PULL 且 cursor 照推（ADR-001）；
// epoch 变化 → 停止普通 Push、Outbox 原样保留（红线 10）。

// ---- 服务端工装（真 sync 处理器 + httptest）----

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srvDir := t.TempDir()
	if err := database.Init(filepath.Join(srvDir, "srv.db"), "99.0.0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	if _, err := database.DB.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','user')`); err != nil {
		t.Fatal(err)
	}
	syncsrv.Configure(srvDir)
	// P1 批次3：附件通道测试面（/api/sync/upload + 受保护 GET /uploads/:name）。
	attachDir := filepath.Join(srvDir, "attach")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatal(err)
	}
	syncsrv.SetAttachmentsDir(attachDir)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := r.Group("/api", func(c *gin.Context) { c.Set("userID", int64(1)); c.Next() })
	auth.POST("/sync/push", syncsrv.Push)
	auth.GET("/sync/pull", syncsrv.Pull)
	auth.GET("/sync/state", syncsrv.State)
	auth.POST("/sync/upload", syncsrv.SyncUpload)
	r.GET("/uploads/:name", func(c *gin.Context) { // 真服务同为顶层受保护路由（非 /api 前缀）
		if c.GetHeader("Authorization") == "" {
			c.JSON(401, gin.H{"error": "auth required"})
			return
		}
		b, err := os.ReadFile(filepath.Join(attachDir, filepath.Base(c.Param("name"))))
		if err != nil {
			c.JSON(404, gin.H{"error": "not found"})
			return
		}
		c.Data(200, "image/png", b)
	})
	t.Cleanup(func() { syncsrv.SetAttachmentsDir("") })
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

// ---- 桌面侧工装（独立 *sql.DB + 最小 client schema，§95 core/client 分层种子）----

const clientDDL = `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password_hash TEXT, role TEXT, token_version INTEGER DEFAULT 0);
CREATE TABLE note_folders (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, name TEXT, sort_order INTEGER DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP, sync_id TEXT, version INTEGER DEFAULT 1);
CREATE TABLE notes (
	id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, title TEXT DEFAULT '', content TEXT DEFAULT '',
	content_text TEXT DEFAULT '', tags TEXT DEFAULT '', is_encrypted INTEGER DEFAULT 0,
	encrypt_password_hash TEXT DEFAULT '', encrypted_content TEXT DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	deleted_at DATETIME, version INTEGER NOT NULL DEFAULT 1, folder_id INTEGER, trash_upload_bytes INTEGER DEFAULT 0,
	sync_id TEXT);
CREATE TABLE note_tags (note_id INTEGER NOT NULL, tag TEXT NOT NULL);
CREATE TABLE uploads (filename TEXT PRIMARY KEY, user_id INTEGER, size INTEGER DEFAULT 0, sha256 TEXT DEFAULT '', status TEXT DEFAULT 'attached');
CREATE TABLE note_uploads (note_id INTEGER, filename TEXT);`

func newStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := localstore.OpenGenDB(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(clientDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO users (id, username, password_hash, role) VALUES (1,'local','!','user')`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTables(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// localCreate 模拟本地新建（含 sync_id 客户端生成，§44）并入队。
func localCreate(t *testing.T, db *sql.DB, syncID, title string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO notes (user_id, title, content, content_text, tags, sync_id) VALUES (1, ?, '<p>x</p>', 'x', '', ?)`, title, syncID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := EnqueueNoteOp(db, id, "CREATE"); err != nil {
		t.Fatal(err)
	}
	return id
}

func localEdit(t *testing.T, db *sql.DB, id int64, title string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE notes SET title = ? WHERE id = ?`, title, id); err != nil {
		t.Fatal(err)
	}
	var syncID string
	if err := db.QueryRow(`SELECT sync_id FROM notes WHERE id = ?`, id).Scan(&syncID); err != nil {
		t.Fatal(err)
	}
	_ = syncID
	if err := EnqueueNoteOp(db, id, "UPDATE"); err != nil {
		t.Fatal(err)
	}
}

func mustSync(t *testing.T, db *sql.DB, ts *httptest.Server, dev string) Summary {
	t.Helper()
	w := NewWorker(db, NewClient(ts.URL, "tok", dev))
	s, err := w.RunOnce(nilCtx())
	if err != nil {
		t.Fatalf("RunOnce(%s): %v", dev, err)
	}
	return s
}

func countRows(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ---- 场景 ----

func TestTwoDesktopsConverge(t *testing.T) {
	ts := newServer(t)
	a, b := newStore(t), newStore(t)

	localCreate(t, a, "s-conv-1", "A 的笔记")
	mustSync(t, a, ts, "devA")
	got := mustSync(t, b, ts, "devB")
	if got.Applied != 1 {
		t.Fatalf("B 应应用 1 条: %+v", got)
	}
	var title string
	if err := b.QueryRow(`SELECT title FROM notes WHERE sync_id='s-conv-1'`).Scan(&title); err != nil || title != "A 的笔记" {
		t.Fatalf("B 收敛失败: %v %q", err, title)
	}
	// B 反向编辑 → A 收敛。
	var bid int64
	_ = b.QueryRow(`SELECT id FROM notes WHERE sync_id='s-conv-1'`).Scan(&bid)
	localEdit(t, b, bid, "B 改名")
	mustSync(t, b, ts, "devB")
	mustSync(t, a, ts, "devA")
	var atitle string
	_ = a.QueryRow(`SELECT title FROM notes WHERE sync_id='s-conv-1'`).Scan(&atitle)
	if atitle != "B 改名" {
		t.Fatalf("A 未收敛远端改名: %q", atitle)
	}
}

func TestConcurrentEditConflictKeepsLocal(t *testing.T) {
	ts := newServer(t)
	a, b := newStore(t), newStore(t)
	localCreate(t, a, "s-cc", "初始")
	mustSync(t, a, ts, "devA")
	mustSync(t, b, ts, "devB") // B 拿到 v1

	localEdit(t, a, idOf(t, a, "s-cc"), "A 版")
	sa := mustSync(t, a, ts, "devA") // server v2
	var srvVer int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE sync_id='s-cc'`).Scan(&srvVer); err != nil {
		t.Fatal(err)
	}
	t.Logf("A#2 summary=%+v server_version=%d", sa, srvVer)
	if srvVer != 2 {
		t.Fatalf("前置断言：A 的 UPDATE 应使服务端 v2，得 %d", srvVer)
	}

	var bid int64
	_ = b.QueryRow(`SELECT id FROM notes WHERE sync_id='s-cc'`).Scan(&bid)
	localEdit(t, b, bid, "B 版（基于 v1）") // base=1 → push 必 409
	got := mustSync(t, b, ts, "devB")
	if got.Conflicts == 0 {
		t.Fatalf("应产生冲突记录: %+v", got)
	}
	var localTitle string
	_ = b.QueryRow(`SELECT title FROM notes WHERE sync_id='s-cc'`).Scan(&localTitle)
	if localTitle != "B 版（基于 v1）" {
		t.Fatalf("红线1：冲突不得覆盖本地未同步修改，得 %q", localTitle)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM sync_conflicts`); n == 0 {
		t.Fatal("冲突恢复资产必须落表")
	}
}

func TestOpReplayIdempotent(t *testing.T) {
	ts := newServer(t)
	a := newStore(t)
	localCreate(t, a, "s-rep", "重放")
	mustSync(t, a, ts, "devA")
	// 人为把已 ACK 的 op 重置为 PENDING（模拟网络重试/状态回滚）→ 服务端必须幂等重放。
	if _, err := a.Exec(`UPDATE sync_outbox SET status='PENDING' WHERE entity_sync_id='s-rep'`); err != nil {
		t.Fatal(err)
	}
	mustSync(t, a, ts, "devA")
	srvDB := database.DB
	var n int
	if err := srvDB.QueryRow(`SELECT COUNT(*) FROM sync_changes WHERE entity_sync_id='s-rep'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("红线3：重放不得产生第二条事件，得 %d", n)
	}
}

func TestPullDirtyGuardADR001(t *testing.T) {
	ts := newServer(t)
	a, b := newStore(t), newStore(t)
	localCreate(t, a, "s-dirty", "母版")
	mustSync(t, a, ts, "devA")
	mustSync(t, b, ts, "devB")

	// B 本地编辑未推送（脏）；A 推送远端新版本。
	var bid int64
	_ = b.QueryRow(`SELECT id FROM notes WHERE sync_id='s-dirty'`).Scan(&bid)
	localEdit(t, b, bid, "B 本地未同步")
	localEdit(t, a, idOf(t, a, "s-dirty"), "A 已上推")
	mustSync(t, a, ts, "devA")

	got := mustSync(t, b, ts, "devB") // pull 撞脏
	if got.Conflicts == 0 {
		t.Fatalf("ADR-001：pull 撞脏必须冲突，得 %+v", got)
	}
	var localTitle string
	_ = b.QueryRow(`SELECT title FROM notes WHERE sync_id='s-dirty'`).Scan(&localTitle)
	if localTitle != "B 本地未同步" {
		t.Fatalf("红线1/6：远端不得静默覆盖脏实体，得 %q", localTitle)
	}
	// cursor 必须照常推进（冲突不阻塞同步链）。
	cur, _ := getState(b, "cursor")
	if cur == "" || cur == "0" {
		t.Fatalf("冲突时 cursor 仍须推进，得 %q", cur)
	}
}

func TestEpochChangeStopsPush(t *testing.T) {
	ts := newServer(t)
	a := newStore(t)
	localCreate(t, a, "s-ep", "代际")
	w := NewWorker(a, NewClient(ts.URL, "tok", "devA"))
	if _, err := w.RunOnce(nilCtx()); err != nil {
		t.Fatal(err)
	}
	// 服务端整库恢复语义：epoch++（R0 落库面）。红线 10：客户端必须停普通 Push。
	if err := model.SetServerEpoch(42); err != nil {
		t.Fatal(err)
	}
	localEdit(t, a, idOf(t, a, "s-ep"), "离线继续编辑")
	_, err := w.RunOnce(nilCtx())
	if !errors.Is(err, ErrEpochChanged) {
		t.Fatalf("应 SERVER_EPOCH_CHANGED，得 %v", err)
	}
	if n := countRows(t, a, `SELECT COUNT(*) FROM sync_outbox WHERE status='PENDING'`); n != 1 {
		t.Fatalf("epoch 变化不得消费/破坏 Outbox，PENDING=%d", n)
	}
}

func idOf(t *testing.T, db *sql.DB, syncID string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM notes WHERE sync_id = ?`, syncID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// nilCtx 集成测试统一用 background context。
func nilCtx() context.Context { return context.Background() }

// TestWebWritesVisibleToDesktopPull —— P2 批次1 门禁（§129）：Web 写路径（app Facade
// 服务端模式，与 handler 同源）产的 ChangeLog 事件，桌面 pull 全量可见——
// 闭合"Web 写、桌面永远看不到"的同步语义洞（CREATE/UPDATE/DELETE/RESTORE 四态收敛）。
func TestWebWritesVisibleToDesktopPull(t *testing.T) {
	ts := newServer(t)
	b := newStore(t)
	web := app.NewServerNoteService(100)

	// Web 建 → 桌面 pull 可见（title/version/sync_id 一致）。
	n, err := web.CreateNote(1, "Web 建", "<p>web</p>", "webtag", nil)
	if err != nil || n.ID == 0 {
		t.Fatalf("web create: %v %+v", err, n)
	}
	var syncID string
	if err := database.DB.QueryRow(`SELECT sync_id FROM notes WHERE id = ?`, n.ID).Scan(&syncID); err != nil || syncID == "" {
		t.Fatalf("web 建的笔记应有 sync_id: %v %q", err, syncID)
	}
	if got := mustSync(t, b, ts, "devB"); got.Applied != 1 {
		t.Fatalf("B 应应用 1 条: %+v", got)
	}
	var title string
	var ver int64
	if err := b.QueryRow(`SELECT title, version FROM notes WHERE sync_id = ?`, syncID).Scan(&title, &ver); err != nil || title != "Web 建" || ver != 1 {
		t.Fatalf("B 未收敛 Web 创建: %v %q v%d", err, title, ver)
	}

	// Web 改 → 桌面收敛。
	if _, err := web.UpdateNote(n.ID, 1, "Web 改", "<p>web2</p>", "", 1); err != nil {
		t.Fatalf("web update: %v", err)
	}
	if got := mustSync(t, b, ts, "devB"); got.Applied != 1 {
		t.Fatalf("B 应应用 UPDATE: %+v", got)
	}
	if err := b.QueryRow(`SELECT title, version FROM notes WHERE sync_id = ?`, syncID).Scan(&title, &ver); err != nil || title != "Web 改" || ver != 2 {
		t.Fatalf("B 未收敛 Web 更新: %v %q v%d", err, title, ver)
	}

	// Web 删（软删墓碑 §58）→ 桌面进入回收站态。
	if err := web.DeleteNote(n.ID, 1); err != nil {
		t.Fatalf("web delete: %v", err)
	}
	if got := mustSync(t, b, ts, "devB"); got.Applied != 1 {
		t.Fatalf("B 应应用 DELETE: %+v", got)
	}
	var deleted int
	if err := b.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id = ? AND deleted_at IS NOT NULL`, syncID).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("B 未收敛 Web 删除: %v deleted=%d", err, deleted)
	}

	// Web 恢复（§59）→ 桌面回到活动态，version 前进。
	if err := web.RestoreNote(n.ID, 1); err != nil {
		t.Fatalf("web restore: %v", err)
	}
	if got := mustSync(t, b, ts, "devB"); got.Applied != 1 {
		t.Fatalf("B 应应用 RESTORE: %+v", got)
	}
	var live int
	if err := b.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id = ? AND deleted_at IS NULL`, syncID).Scan(&live); err != nil || live != 1 {
		t.Fatalf("B 未收敛 Web 恢复: %v live=%d", err, live)
	}
	if err := b.QueryRow(`SELECT version FROM notes WHERE sync_id = ?`, syncID).Scan(&ver); err != nil || ver != 4 {
		t.Fatalf("恢复后桌面 version 应=4: %v v%d", err, ver)
	}
}

// TestAckRebasesPendingNoteBase —— 红线 8 自动路径基线测（批次8-Alt 刀3 的前置门禁）。
//
// 场景是"在途单飞 + 队列里还排着同实体的下一笔"：op A 已 buildBatch 标为 SENDING（真推），
// 用户又改了一次产生 op B（PENDING，base 仍是本地旧版本）。A 拿到 ACK 后服务端权威版本前进，
// §50 必须把 B 的 CAS 基点重写为该新版本——否则 B 下一轮必带陈旧 base 撞 409，
// 表现就是"改了两下就永远同步不动"。NOTE 面此前只有回写路径被间接经过，无直接断言
// （阶段 0 台账记为红线 8 缺口），此测把它钉死；调度器上线后本测即为回归网。
func TestAckRebasesPendingNoteBase(t *testing.T) {
	ts := newServer(t)
	a := newStore(t)
	localCreate(t, a, "s-rb", "母版")
	mustSync(t, a, ts, "devA") // CREATE ACK → 服务端 v1，本地 version=1

	nid := idOf(t, a, "s-rb")
	localEdit(t, a, nid, "改动一") // op A：PENDING base=1
	w := NewWorker(a, NewClient(ts.URL, "tok", "devA"))
	b, err := w.buildBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.ops) != 1 {
		t.Fatalf("op A 应独占本批（同实体合并），得 %d", len(b.ops))
	}

	localEdit(t, a, nid, "改动二") // 在途期间再改一次 → op B：PENDING base=本地旧版本
	var baseBefore sql.NullInt64
	if err := a.QueryRow(`SELECT base_version FROM sync_outbox
		WHERE entity_sync_id='s-rb' AND status='PENDING'`).Scan(&baseBefore); err != nil {
		t.Fatal(err)
	}
	if !baseBefore.Valid || baseBefore.Int64 != 1 {
		t.Fatalf("前置：op B 基点应为陈旧 v1，得 %+v", baseBefore)
	}

	results, err := w.cl.Push(b.ops) // 真推 op A → 服务端 v2
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if _, aerr := w.applyResult(r, b); aerr != nil {
			t.Fatal(aerr)
		}
	}

	var baseAfter sql.NullInt64
	if err := a.QueryRow(`SELECT base_version FROM sync_outbox
		WHERE entity_sync_id='s-rb' AND status='PENDING'`).Scan(&baseAfter); err != nil {
		t.Fatal(err)
	}
	if !baseAfter.Valid || baseAfter.Int64 != 2 {
		t.Fatalf("红线8：ACK 后队列中 PENDING 的基点必须重定为远端 v2，得 %+v", baseAfter)
	}

	// 收敛终证：下一轮真实同步必须把 op B 推上去且零冲突（服务端 v3、内容为"改动二"）。
	got := mustSync(t, a, ts, "devA")
	if got.Conflicts != 0 {
		t.Fatalf("重定基后不应再冲突： %+v", got)
	}
	var srvVer int64
	var srvTitle string
	if err := database.DB.QueryRow(`SELECT version, title FROM notes WHERE sync_id='s-rb'`).Scan(&srvVer, &srvTitle); err != nil {
		t.Fatal(err)
	}
	if srvVer != 3 || srvTitle != "改动二" {
		t.Fatalf("服务端应停在 v3/改动二，得 v%d/%q", srvVer, srvTitle)
	}
	if n := countRows(t, a, `SELECT COUNT(*) FROM sync_outbox
		WHERE entity_sync_id='s-rb' AND status IN ('PENDING','FAILED','CONFLICT','SENDING')`); n != 0 {
		t.Fatalf("两笔都应收账，仍开着的有 %d", n)
	}
	if n := countRows(t, a, `SELECT COUNT(*) FROM sync_conflicts`); n != 0 {
		t.Fatalf("本测不得产生冲突记录（那是另一条路径），得 %d", n)
	}
}
