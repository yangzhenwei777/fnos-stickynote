package bootstrap

// bootstrap_test.go —— 批次 5 · 刀 3：Bootstrap/RESYNC 集成 + 单元。
// 真服务端（sync 处理器 + 快照三端点）+ 真 localstore 激活。
// 覆盖：§75/§140 cursor=watermark、W 后事件 pull 补差（§140 另一半）、§39/§90 Outbox
// 搬运重定基/CONFLICT/FOLDER_ORDER 丢弃、失败零破坏守卫、zip 恶意结构防线、sha 防篡改。

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"stickynote-desktop/internal/bootptest"
	"stickynote-desktop/localauth"
	"stickynote-desktop/localstore"
	"stickynote-desktop/synclocal"
)

func readState(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(`SELECT value FROM sync_state WHERE key=?`, key).Scan(&v); err != nil {
		t.Fatalf("state %s: %v", key, err)
	}
	return v
}

func TestBootstrapEpochChangeResync(t *testing.T) {
	ts := bootptest.Start(t)
	ts.Seed(t, "pre-epoch")
	profile := t.TempDir()
	rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
	sess := &localauth.Session{ServerURL: ts.URL, AccessToken: "t", DeviceID: "dev-e"}
	res, err := rn.Run(sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	lay := localstore.Layout{ProfileDir: profile}
	db, err := localstore.OpenGenDB(lay.GenDB(res.Generation))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := synclocal.EnsureTables(db); err != nil {
		t.Fatal(err)
	}
	w := synclocal.NewWorker(db, synclocal.NewClient(ts.URL, "t", "dev-e"))
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 服务端模拟恢复换代（epoch+1）→ 锚校验必须停摆（红线 10/§89）。
	ts.ResetEpoch(t)
	if _, err := w.RunOnce(context.Background()); !errors.Is(err, synclocal.ErrEpochChanged) {
		t.Fatalf("want ErrEpochChanged, got %v", err)
	}
	// RESYNC：带旧代 Outbox 重 Bootstrap（本地未推送编辑必须搬运存活）。
	if _, err := db.Exec(`INSERT INTO sync_outbox (op_id,entity_type,entity_sync_id,operation,base_version,payload,status,retry_count)
		VALUES ('keep-op','NOTE','sid-local-x','CREATE',0,'{"sync_id":"sid-local-x","title":"local-x"}','PENDING',0)`); err != nil {
		t.Fatal(err)
	}
	oldDB2, err := localstore.OpenGenDB(lay.GenDB(res.Generation))
	if err != nil {
		t.Fatal(err)
	}
	defer oldDB2.Close()
	res2, err := rn.Run(sess, oldDB2)
	if err != nil {
		t.Fatalf("resync bootstrap: %v", err)
	}
	if res2.Generation == res.Generation {
		t.Fatal("resync must land on a new generation")
	}
	newDB, err := localstore.OpenGenDB(lay.GenDB(res2.Generation))
	if err != nil {
		t.Fatal(err)
	}
	defer newDB.Close()
	var st, cnt string
	var n int
	if err := newDB.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE op_id='keep-op' AND status='PENDING'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("outbox op must survive resync (红线 1/6/9): %d %v", n, err)
	}
	_ = st
	_ = cnt
	// 换代后重放：本地 CREATE 推送成功、服务端可见。
	w2 := synclocal.NewWorker(newDB, synclocal.NewClient(ts.URL, "t", "dev-e"))
	if _, err := w2.RunOnce(context.Background()); err != nil {
		t.Fatalf("post-resync run: %v", err)
	}
	if ts.HasNote(t, "local-x") != 1 {
		t.Fatal("carried CREATE must reach server after resync")
	}
}

func TestBootstrapHappyPathAndWatermark(t *testing.T) {
	ts := bootptest.Start(t)
	ts.Seed(t, "snap-note")
	profile := t.TempDir()
	rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
	res, err := rn.Run(&localauth.Session{ServerURL: ts.URL, AccessToken: "t", DeviceID: "dev-bt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Watermark < 1 || res.Generation == "" {
		t.Fatalf("res=%+v", res)
	}
	lay := localstore.Layout{ProfileDir: profile}
	db, err := localstore.OpenGenDB(lay.GenDB(res.Generation))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := readState(t, db, "cursor"); got != itoa(res.Watermark) {
		t.Fatalf("§75 cursor must equal watermark: %s vs %d", got, res.Watermark)
	}
	if got := readState(t, db, "server_epoch"); got != itoa(res.Epoch) {
		t.Fatalf("anchor epoch=%s want %d", got, res.Epoch)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM notes WHERE title='snap-note'`).Scan(&n)
	if n != 1 {
		t.Fatal("snapshot note missing")
	}

	// §140 另一半：W 之后的新事件必须经 pull 完整补差（fixed watermark 语义由 worker 承担）。
	ts.Seed(t, "after-w")
	cl := synclocal.NewClient(ts.URL, "t", "dev-bt")
	w := synclocal.NewWorker(db, cl)
	if _, err := w.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM notes WHERE title='after-w'`).Scan(&n)
	if n != 1 {
		t.Fatal("post-watermark event not applied")
	}
	if got := readState(t, db, "cursor"); got == itoa(res.Watermark) {
		t.Fatal("cursor did not advance past watermark")
	}
}

func TestBootstrapGuardsNoLocalChange(t *testing.T) {
	profile := t.TempDir()
	rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
	if _, err := rn.Run(&localauth.Session{ServerURL: "http://127.0.0.1:9/", AccessToken: "t", DeviceID: "d"}, nil); err == nil {
		t.Fatal("dead server must error")
	}
	if _, err := rn.Run(&localauth.Session{}, nil); err == nil {
		t.Fatal("tokenless session must error (红线：绝不在无票时动本地)")
	}
	ents, _ := os.ReadDir(filepath.Join(profile, "generations"))
	if len(ents) != 0 {
		t.Fatalf("failed bootstrap must not create generations: %v", ents)
	}
}

func TestUnpackGuards(t *testing.T) {
	writeZip := func(t *testing.T, dir string, files map[string]string) string {
		t.Helper()
		p := filepath.Join(dir, "s.zip")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for name, content := range files {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(content))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	meta := func() string {
		b, _ := json.Marshal(Meta{SnapshotID: "snap-x", Watermark: 1, ServerEpoch: 1})
		return string(b)
	}

	// 路径穿越成员
	p := writeZip(t, t.TempDir(), map[string]string{"notes.db": "junk", "meta.json": meta(), "../evil": "x"})
	if _, err := unpack(p, t.TempDir(), t.TempDir()); !isBadZip(err) {
		t.Fatalf("traversal must be rejected, got %v", err)
	}
	// 缺 meta
	p = writeZip(t, t.TempDir(), map[string]string{"notes.db": "junk"})
	if _, err := unpack(p, t.TempDir(), t.TempDir()); !isBadZip(err) {
		t.Fatalf("missing meta must be rejected, got %v", err)
	}
	// 合法成员但库是垃圾 → integrity_check 拒
	dir := t.TempDir()
	p = writeZip(t, dir, map[string]string{"notes.db": "not-a-sqlite-file", "meta.json": meta()})
	if _, err := unpack(p, dir, dir); err == nil {
		t.Fatal("corrupt notes.db must be rejected")
	}
}

func TestCarryOverOutboxSemantics(t *testing.T) {
	oldDB, newDB := memOutboxDB(t), memOutboxDB(t)
	if _, err := newDB.Exec(`INSERT INTO notes (user_id,title,content,content_text,sync_id,version)
		VALUES (1,'n','c','c','sid-note',7)`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO sync_outbox (op_id,entity_type,entity_sync_id,operation,base_version,payload,status,retry_count)
		VALUES (?,?,?,?,?,?,?,0)`
	rows := [][7]string{
		{"o1", "NOTE", "sid-note", "UPDATE", "3", "{}", "PENDING"},
		{"o2", "NOTE", "sid-gone", "UPDATE", "1", "{}", "PENDING"},
		{"o3", "FOLDER_ORDER", "", "ORDER", "0", "[]", "PENDING"},
		{"o4", "NOTE", "sid-new", "CREATE", "0", "{}", "PENDING"},
	}
	for _, r := range rows {
		if _, err := oldDB.Exec(ins, r[0], r[1], r[2], r[3], r[4], r[5], r[6]); err != nil {
			t.Fatal(err)
		}
	}
	carried, conflicts, dropped, err := carryOverOutbox(oldDB, newDB)
	if err != nil {
		t.Fatal(err)
	}
	if carried != 2 || conflicts != 1 || dropped != 1 {
		t.Fatalf("carried=%d conflicts=%d dropped=%d", carried, conflicts, dropped)
	}
	var base int64
	var status string
	newDB.QueryRow(`SELECT base_version,status FROM sync_outbox WHERE op_id='o1'`).Scan(&base, &status)
	if base != 7 || status != "PENDING" {
		t.Fatalf("o1 rebase wrong: base=%d status=%s", base, status)
	}
	newDB.QueryRow(`SELECT base_version,status FROM sync_outbox WHERE op_id='o2'`).Scan(&base, &status)
	if status != "CONFLICT" {
		t.Fatalf("missing entity must be CONFLICT, got %s", status)
	}
	var n int
	newDB.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE op_id='o3'`).Scan(&n)
	if n != 0 {
		t.Fatal("FOLDER_ORDER must be dropped")
	}
	// 幂等：重跑不翻倍
	if _, _, _, err := carryOverOutbox(oldDB, newDB); err != nil {
		t.Fatal(err)
	}
	newDB.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&n)
	if n != 3 {
		t.Fatalf("carry must be idempotent, rows=%d", n)
	}
}

// memOutboxDB 最小 client schema（复用 synclocal 集成工装同构 DDL 的最小子集）。
func memOutboxDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := localstore.OpenGenDB(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password_hash TEXT, role TEXT);
		INSERT INTO users (id, username, password_hash, role) VALUES (1,'local','!','user');
		CREATE TABLE note_folders (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, name TEXT, sort_order INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, sync_id TEXT, version INTEGER DEFAULT 1);
		CREATE TABLE notes (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, title TEXT DEFAULT '',
			content TEXT DEFAULT '', content_text TEXT DEFAULT '', tags TEXT DEFAULT '', is_encrypted INTEGER DEFAULT 0,
			encrypt_password_hash TEXT DEFAULT '', encrypted_content TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME, version INTEGER NOT NULL DEFAULT 1, folder_id INTEGER, trash_upload_bytes INTEGER DEFAULT 0, sync_id TEXT);
		CREATE TABLE uploads (filename TEXT PRIMARY KEY, user_id INTEGER, size INTEGER DEFAULT 0, status TEXT DEFAULT 'attached');
		CREATE TABLE note_uploads (note_id INTEGER, filename TEXT);`); err != nil {
		t.Fatal(err)
	}
	if err := synclocal.EnsureTables(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func isBadZip(err error) bool { return errors.Is(err, ErrBadZip) }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// Run 级防篡改：账本 sha 与实际下载内容不符 → ErrSHAMismatch 且零破坏（无新代生成）。
func TestBootstrapSHAMismatch(t *testing.T) {
	ts := bootptest.Start(t)
	ts.Seed(t, "sha-note")
	profile := t.TempDir()
	rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
	sess := &localauth.Session{ServerURL: ts.URL, AccessToken: ts.Token(t), DeviceID: "dev-sha"}
	// 先正常构建一枚快照，再用假 id 让下载与账本错位不可行——改为直接注入错误 sha 的
	// 最小服务端替代：用 httptest 反代？过度。取简单路径：临时把 Runner 指向**另一台**
	// 干净服务端（无该快照）→ 下载 404 报错路径已被 Guards 覆盖；此处专测 sha 分支：
	// 复用同服务端拿真 READY，然后手工把 zip 换脏（模拟传输/存储篡改）再触发校验。
	// 实现：直接走底层——请求+轮询拿 READY 元数据，下载后篡改 staging 文件不可行（Run 内部自下载）。
	// 因此以本地假服务器精确回放 READY+坏 zip。
	bad := newTamperedServer(t, sess.ServerURL)
	sess2 := &localauth.Session{ServerURL: bad, AccessToken: "t", DeviceID: "dev-sha"}
	_, err := rn.Run(sess2, nil)
	if !errors.Is(err, ErrSHAMismatch) {
		t.Fatalf("want ErrSHAMismatch, got %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(profile, "generations")); len(ents) != 0 {
		t.Fatal("sha mismatch must not activate any generation")
	}
}

// TestUnpackAcceptsUploadsEntries v21.152 P0 回归（生产实锤）：
// 合法成员 `uploads/<附件名>` 曾被旧判据（filepath.Base(rel) == rel，恒假）误判为不安全 →
// 含附件的快照整包被拒 → 首装/新设备的自动快照同步永远失败（folders 与窗口外历史实体拉不到）。
// 真机日志物证：`自动快照失败: bootstrap: snapshot zip invalid: "uploads/2426b163-….png"`。
func TestUnpackAcceptsUploadsEntries(t *testing.T) {
	writeZipBytes := func(t *testing.T, dir string, files [][2][]byte) string {
		t.Helper()
		p := filepath.Join(dir, "s.zip")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for _, kv := range files {
			w, err := zw.Create(string(kv[0]))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(kv[1]); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}
	realDB := func(t *testing.T, dir string) []byte {
		t.Helper()
		p := filepath.Join(dir, "src.db")
		db, err := sql.Open("sqlite", "file:"+p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT)`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	meta := func() []byte {
		b, _ := json.Marshal(Meta{SnapshotID: "snap-uploads", Watermark: 7, ServerEpoch: 1})
		return b
	}
	src := t.TempDir()
	attBody := []byte("\x89PNG\r\n\x1a\n-fake-attachment-bytes")

	// 正例：notes.db + meta.json + uploads/<附件>（含中文名/宽字符名同样合法）→ 必须 unpack 成功
	staging, up := t.TempDir(), t.TempDir()
	p := writeZipBytes(t, src, [][2][]byte{
		{[]byte("notes.db"), realDB(t, t.TempDir())},
		{[]byte("meta.json"), meta()},
		{[]byte("uploads/2426b163-8f22-4b97-a0c5-473a280ae14a.png"), attBody},
		{[]byte("uploads/中文 附件.png"), attBody},
	})
	m, err := unpack(p, staging, up)
	if err != nil {
		t.Fatalf("含 uploads/<附件> 的合法快照必须 unpack 成功（v21.152 回归），got %v", err)
	}
	if m == nil || m.SnapshotID != "snap-uploads" {
		t.Fatalf("meta 解析异常: %+v", m)
	}
	got, err := os.ReadFile(filepath.Join(up, "uploads", "2426b163-8f22-4b97-a0c5-473a280ae14a.png"))
	if err != nil || string(got) != string(attBody) {
		t.Fatalf("附件未按原字节落地: %v", err)
	}

	// 反例 1：uploads/ 下再嵌套子目录 → 拒绝
	p = writeZipBytes(t, src, [][2][]byte{
		{[]byte("notes.db"), realDB(t, t.TempDir())},
		{[]byte("meta.json"), meta()},
		{[]byte("uploads/sub/x.png"), attBody},
	})
	if _, err := unpack(p, t.TempDir(), t.TempDir()); !isBadZip(err) {
		t.Fatalf("uploads 深层嵌套必须拒绝, got %v", err)
	}
	// 反例 2：裸 uploads/ 目录条目 → 拒绝
	p = writeZipBytes(t, src, [][2][]byte{
		{[]byte("notes.db"), realDB(t, t.TempDir())},
		{[]byte("meta.json"), meta()},
		{[]byte("uploads/"), []byte("")},
	})
	if _, err := unpack(p, t.TempDir(), t.TempDir()); !isBadZip(err) {
		t.Fatalf("裸 uploads/ 条目必须拒绝, got %v", err)
	}
}

// TestBootstrapFirstInstallNewGenHasClientProfileTable v21.165 回归钉（204 轮桌面走查定罪）：
//
// 生产根因：`client_profile` 是 desktop 专属表，**服务端快照天然不含**。此前建表动作
// 只存在于步骤 9.5 `CarryClientIdentity` 的内层，而该步在 `if oldGenDB != nil` 分支内。
// 首装路径（oldGenDB == nil）⇒ 新代**永久缺这张表** ⇒ 换代后续接管面（含
// database.SetDB(newDB)）不完成 ⇒ 前端继续读旧代句柄 ⇒ 库里明明有服务端同步来的笔记，
// UI 恒显「共 0 条笔记」且交互无响应（真机铁证：两代库均 notes>0，active 代无 client_profile）。
//
// 本钉锁死不变量：**无论有无旧代，换代后新代必须有 client_profile 表**（表在不必有行——
// 离线单机换代无身份可迁属正常，但 schema 必须完备）。
func TestBootstrapFirstInstallNewGenHasClientProfileTable(t *testing.T) {
	ts := bootptest.Start(t)
	ts.Seed(t, "first-install-note")
	profile := t.TempDir()
	lay := localstore.Layout{ProfileDir: profile}
	if err := lay.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	// 首代 provision（无 client_profile 表，模拟服务端快照来源）
	if err := os.MkdirAll(lay.GenDir("g-000001"), 0700); err != nil {
		t.Fatal(err)
	}

	rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
	// ⭐ oldGenDB 传 nil = 首装路径（本缺陷的触发条件）
	res, err := rn.Run(&localauth.Session{ServerURL: ts.URL, AccessToken: "t", DeviceID: "dev-new"}, nil)
	if err != nil {
		t.Fatalf("首装 Run 必须成功: %v", err)
	}
	newDB, err := localstore.OpenGenDB(lay.GenDB(res.Generation))
	if err != nil {
		t.Fatal(err)
	}
	defer newDB.Close()

	var n int
	if err := newDB.QueryRow(`SELECT COUNT(*) FROM client_profile`).Scan(&n); err != nil {
		t.Fatalf("换代后新代必须有 client_profile 表（v21.165 根因回归）: %v", err)
	}
	_ = n // 表在即可；行数由 carry 语义决定（首装无旧代身份 ⇒ 0 行属正常）
	// 反向对照：synclocal 表族同样必须在位（既有纪律，防同类退化）
	for _, tb := range []string{"sync_state", "sync_outbox", "sync_changes"} {
		if err := newDB.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil {
			t.Fatalf("换代后新代缺 %s 表: %v", tb, err)
		}
	}
}

// TestBootstrapCarriesClientIdentityFromRealOldGen v21.152 生产根因回归：
// 换代（快照激活）后 desktop 专属表 client_profile 必须随代迁移——否则 UI「账号与设备」
// 显示未登录、必须重登（真机第 1/2/3/4 条症状的共同底座）。
// 同时钉住"Run 必须返回"：真机曾观测到激活完成（磁盘有 g-000002）而调用方永不返回。
func TestBootstrapCarriesClientIdentityFromRealOldGen(t *testing.T) {
	ts := bootptest.Start(t)
	ts.Seed(t, "carry-note")
	profile := t.TempDir()
	lay := localstore.Layout{ProfileDir: profile}
	if err := lay.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lay.GenDir("g-000001"), 0700); err != nil {
		t.Fatal(err)
	}
	oldDB, err := localstore.OpenGenDB(lay.GenDB("g-000001"))
	if err != nil {
		t.Fatal(err)
	}
	defer oldDB.Close()
	if err := synclocal.EnsureTables(oldDB); err != nil {
		t.Fatal(err)
	}
	if err := localauth.EnsureTables(oldDB); err != nil {
		t.Fatal(err)
	}
	if _, err := oldDB.Exec(`INSERT INTO client_profile (id, device_id, server_url, username, refresh_sealed, secret_sealed)
		VALUES (1,'dev-old','http://old.example/','alice', x'0102', x'0304')`); err != nil {
		t.Fatal(err)
	}

	rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
	res, err := rn.Run(&localauth.Session{ServerURL: ts.URL, AccessToken: "t", DeviceID: "dev-old"}, oldDB)
	if err != nil {
		t.Fatalf("Run 必须返回成功: %v", err)
	}
	newDB, err := localstore.OpenGenDB(lay.GenDB(res.Generation))
	if err != nil {
		t.Fatal(err)
	}
	defer newDB.Close()
	var devID, srv, user string
	rowQ := newDB.QueryRow(`SELECT device_id, server_url, username FROM client_profile WHERE id=1`)
	if err := rowQ.Scan(&devID, &srv, &user); err != nil {
		t.Fatalf("换代后 client_profile 必须随代迁移（v21.152 根因）: %v", err)
	}
	if devID != "dev-old" || srv != "http://old.example/" || user != "alice" {
		t.Fatalf("身份迁移内容不符: %q %q %q", devID, srv, user)
	}
	var rfLen, scLen int
	if err := newDB.QueryRow(`SELECT length(refresh_sealed), length(secret_sealed) FROM client_profile WHERE id=1`).Scan(&rfLen, &scLen); err != nil {
		t.Fatal(err)
	}
	if rfLen != 2 || scLen != 2 {
		t.Fatalf("封存件必须原字节迁移: rf=%d sc=%d", rfLen, scLen)
	}
}

// TestVerifyIdentityCarried v21.153 R-1/W4 收口自检的三格：
// ① 旧代有身份 + 新代空 → carry 幂等补齐、无错；② 旧代无身份 → 直接放行（离线单机换代属正常）；
// ③ 新代不可写 → 重试失败必须 fail-closed 显式报错，不得静默（192 真机「carry 偶发未落」的防复发钉）。
func TestVerifyIdentityCarried(t *testing.T) {
	newFileDB := func(t *testing.T) *sql.DB {
		p := filepath.Join(t.TempDir(), "notes.db")
		db, err := sql.Open("sqlite", "file:"+p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := localauth.EnsureTables(db); err != nil {
			t.Fatal(err)
		}
		return db
	}
	seedIdentity := func(t *testing.T, db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO client_profile (id, device_id, server_url, username, refresh_sealed, secret_sealed)
			VALUES (1,'dev-v','http://s.example/','bob', x'0102', x'0304')`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("old_has_identity_new_empty_carry_fills", func(t *testing.T) {
		oldDB := newFileDB(t)
		seedIdentity(t, oldDB)
		newDB := newFileDB(t)
		if err := verifyIdentityCarried(oldDB, newDB); err != nil {
			t.Fatalf("应自愈成功: %v", err)
		}
		var n int
		if err := newDB.QueryRow(`SELECT COUNT(*) FROM client_profile WHERE id=1`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("新代必须有身份行 n=%d err=%v", n, err)
		}
	})

	t.Run("old_without_identity_passes", func(t *testing.T) {
		oldDB := newFileDB(t)
		newDB := newFileDB(t)
		if err := verifyIdentityCarried(oldDB, newDB); err != nil {
			t.Fatalf("旧代无身份应放行: %v", err)
		}
	})

	t.Run("new_db_readonly_fails_closed", func(t *testing.T) {
		oldDB := newFileDB(t)
		seedIdentity(t, oldDB)
		p := filepath.Join(t.TempDir(), "ro.db")
		rw, err := sql.Open("sqlite", "file:"+p)
		if err != nil {
			t.Fatal(err)
		}
		if err := localauth.EnsureTables(rw); err != nil {
			t.Fatal(err)
		}
		rw.Close()
		ro, err := sql.Open("sqlite", "file:"+p+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer ro.Close()
		if err := verifyIdentityCarried(oldDB, ro); err == nil {
			t.Fatal("重试失败必须显式报错（fail-closed），不得静默通过")
		}
	})
}
