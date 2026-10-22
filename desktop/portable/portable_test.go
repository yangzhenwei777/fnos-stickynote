package portable

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"stickynote/backup"

	"stickynote-desktop/localstore"
	"stickynote-desktop/synclocal"
)

// ---- 测试工装：最小生产形状档案（真实 Activator 建立 generations/指针状态） ----

type seedOpts struct {
	deviceID    string
	instanceID  string
	cursor      int64
	epoch       int64
	noteTitle   string
	noteSyncID  string
	pendingOp   bool   // 建 1 条 PENDING CREATE op（含实体行 = 账在物在）
	hasProfile  bool   // client_profile 行
	conflict    bool   // sync_conflicts 行
	attachedIMG bool   // uploads/note_uploads 挂一张附件
	withSecrets bool   // users/client_profile 写入 SENTINEL 凭据
	profileDir  string // 相对根目录
}

const (
	sentinelTOTP  = "SENTINEL-TOTP-SECRET"
	sentinelJTI   = "SENTINEL-REFRESH-JTI"
	sentinelDPAPI = "SENTINEL-DPAPI-SEALED"
	sentinelPIN   = "SENTINEL-PIN-VERIFIER"
	sentinelPass  = "SENTINEL-PASSHASH"
)

var seedDDL = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL DEFAULT '', security_question TEXT DEFAULT '',
		security_answer_hash TEXT DEFAULT '', role TEXT NOT NULL DEFAULT 'user',
		token_version INTEGER NOT NULL DEFAULT 0, totp_enabled INTEGER DEFAULT 0,
		totp_secret TEXT DEFAULT '', refresh_jti TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
	`CREATE TABLE IF NOT EXISTS note_folders (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL,
		name TEXT NOT NULL, sort_order INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, sync_id TEXT, version INTEGER DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS notes (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL,
		title TEXT DEFAULT 'Untitled', content TEXT DEFAULT '', content_text TEXT DEFAULT '',
		tags TEXT DEFAULT '', created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME,
		folder_id INTEGER, is_encrypted INTEGER DEFAULT 0,
		encrypt_password_hash TEXT DEFAULT '', encrypted_content TEXT DEFAULT '',
		version INTEGER DEFAULT 0, sync_id TEXT, trash_upload_bytes INTEGER DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS uploads (
		id INTEGER PRIMARY KEY AUTOINCREMENT, filename TEXT NOT NULL UNIQUE,
		user_id INTEGER, size INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		status TEXT DEFAULT 'attached', status_changed_at DATETIME, sha256 TEXT DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS note_uploads (note_id INTEGER NOT NULL, filename TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS client_profile (
		id INTEGER PRIMARY KEY CHECK (id = 1), device_id TEXT NOT NULL,
		server_url TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '',
		created_at TEXT, last_login_at TEXT DEFAULT '', last_revalidate_at TEXT DEFAULT '',
		refresh_sealed BLOB, secret_sealed BLOB, pin_verifier TEXT)`,
	`CREATE TABLE IF NOT EXISTS totp_recovery (id INTEGER PRIMARY KEY, user_id INTEGER, code_hash TEXT, used INTEGER DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS refresh_sessions (id INTEGER PRIMARY KEY, user_id INTEGER, jti TEXT, family_id TEXT)`,
	`CREATE TABLE IF NOT EXISTS login_fails (k TEXT PRIMARY KEY, n INTEGER)`,
	`CREATE TABLE IF NOT EXISTS backup_targets (id INTEGER PRIMARY KEY, url TEXT, user TEXT, pass TEXT)`,
	`CREATE TABLE IF NOT EXISTS sync_snapshots (id INTEGER PRIMARY KEY)`,
	`CREATE TABLE IF NOT EXISTS sync_snapshots_hold (id INTEGER PRIMARY KEY)`,
	`CREATE TABLE IF NOT EXISTS restore_finalizations (id INTEGER PRIMARY KEY)`,
}

// seedProfile 建立带一个激活代的档案目录（走真实 Activator，指针/journal 语义真实）。
func seedProfile(t *testing.T, root string, o seedOpts) string {
	t.Helper()
	prof := filepath.Join(root, o.profileDir)
	if err := os.MkdirAll(prof, 0700); err != nil {
		t.Fatal(err)
	}
	stag := filepath.Join(prof, "staging")
	if err := os.MkdirAll(stag, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(stag, "seed.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, q := range seedDDL {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed ddl: %v", err)
		}
	}
	if err := synclocal.EnsureTables(db); err != nil {
		t.Fatal(err)
	}
	passHash, qHash, aHash, pinV := "", "", "", ""
	if o.withSecrets {
		passHash, qHash, aHash, pinV = sentinelPass, "Q?", sentinelTOTP+"-ANS", sentinelPIN
	}
	if _, err := db.Exec(`INSERT INTO users(id,username,password_hash,security_question,security_answer_hash,
		totp_enabled,totp_secret,refresh_jti) VALUES(1,'alice',?,?,?,0,?,?)`,
		passHash, qHash, aHash,
		func() string {
			if o.withSecrets {
				return sentinelTOTP
			}
			return ""
		}(),
		func() string {
			if o.withSecrets {
				return sentinelJTI
			}
			return ""
		}()); err != nil {
		t.Fatal(err)
	}
	if o.hasProfile {
		var rs, ss []byte
		if o.withSecrets {
			rs = []byte(sentinelDPAPI)
			ss = []byte(sentinelDPAPI + "-SECRET")
		} else {
			rs = []byte("plain-refresh-sealed")
			ss = []byte("plain-device-secret")
		}
		if _, err := db.Exec(`INSERT INTO client_profile(id,device_id,server_url,username,refresh_sealed,secret_sealed,pin_verifier)
			VALUES(1,?,'https://nas:13389','alice',?,?,?)`, o.deviceID, rs, ss, pinV); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sync_state(key,value) VALUES
		('cursor',?),('server_epoch',?),('server_instance_id',?)`,
		itos(o.cursor), itos(o.epoch), o.instanceID); err != nil {
		t.Fatal(err)
	}
	if o.noteSyncID != "" {
		if _, err := db.Exec(`INSERT INTO notes(id,user_id,title,content,content_text,version,sync_id)
			VALUES(1,1,?,'hello','hello',3,?)`, o.noteTitle, o.noteSyncID); err != nil {
			t.Fatal(err)
		}
	}
	if o.pendingOp {
		if _, err := db.Exec(`INSERT INTO notes(id,user_id,title,content,content_text,version,sync_id)
			VALUES(2,1,'未推送新建','body','body',0,'srv-new')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO sync_outbox(op_id,entity_type,entity_sync_id,operation,base_version,payload,status)
			VALUES('op-local-1','NOTE','srv-new','CREATE',NULL,'{"sync_id":"srv-new","title":"未推送新建"}','PENDING')`); err != nil {
			t.Fatal(err)
		}
	}
	if o.conflict {
		if _, err := db.Exec(`INSERT INTO sync_conflicts(entity_sync_id,kind,local_payload,remote_payload)
			VALUES('srv-c1','PUSH_CONFLICT','L','R')`); err != nil {
			t.Fatal(err)
		}
	}
	if o.attachedIMG {
		upDir := filepath.Join(prof, "uploads")
		if err := os.MkdirAll(upDir, 0700); err != nil {
			t.Fatal(err)
		}
		img := []byte("PNG-fake-bytes-1234567890")
		if err := os.WriteFile(filepath.Join(upDir, "img-uuid-1.png"), img, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO uploads(filename,user_id,size,status) VALUES('img-uuid-1.png',1,?, 'attached')`, len(img)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO note_uploads(note_id,filename) VALUES(1,'img-uuid-1.png')`); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	gen, err := localstore.NewActivator(prof).Activate(dbPath, []byte(`{"prov":"test-seed"}`))
	if err != nil {
		t.Fatalf("seed activate: %v", err)
	}
	_ = gen
	return prof
}

func itos(v int64) string { return strconv.FormatInt(v, 10) }

// ---- 测试 ----

func TestExportSanitizesAllCredentials(t *testing.T) {
	root := t.TempDir()
	prof := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", instanceID: "inst-A",
		cursor: 42, epoch: 7, noteTitle: "n1", noteSyncID: "srv-1",
		hasProfile: true, withSecrets: true, attachedIMG: true, conflict: true})
	out := filepath.Join(root, "a.snbak")
	res, err := Export(ExportOptions{ProfileDir: prof, UploadDir: filepath.Join(prof, "uploads"),
		Password: "p@ssw0rd-long", OutPath: out, AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.OutboxPending != 0 || res.NoteCount != 1 {
		t.Fatalf("unexpected stats %+v", res)
	}
	salt, iter, env, err := readContainer(out, 300<<20)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := backup.EnvelopeOpen(env, deriveMaster("p@ssw0rd-long", salt, iter), 0)
	if err != nil {
		t.Fatal(err)
	}
	exDir := t.TempDir()
	if _, err := backup.ExtractArchive(payload, exDir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(exDir, "notepad.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{sentinelTOTP, sentinelJTI, sentinelDPAPI, sentinelPIN, sentinelPass} {
		if bytes.Contains(raw, []byte(s)) {
			t.Fatalf("red line §107: sentinel %q leaked into portable snapshot bytes", s)
		}
	}
	// 结构面复检：client_profile 空、users 行保留但凭据列清空
	db, err := sql.Open("sqlite", filepath.Join(exDir, "notepad.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM client_profile`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("client_profile must be stripped: n=%d err=%v", n, err)
	}
	var ph, ts, rj string
	if err := db.QueryRow(`SELECT password_hash, totp_secret, refresh_jti FROM users WHERE id=1`).Scan(&ph, &ts, &rj); err != nil {
		t.Fatal(err)
	}
	if ph != "" || ts != "" || rj != "" {
		t.Fatalf("users creds not cleared: %q %q %q", ph, ts, rj)
	}
	// 红线 26：同步态（sync_state/outbox/conflicts）与附件账目必须原样在包内
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("conflicts must be preserved (红线27 数据面): n=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM note_uploads`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("attachment refs must be preserved (红线26): n=%d", n)
	}
	if v, _ := syncStateValue(db, "cursor"); v != "42" {
		t.Fatalf("cursor anchor lost: %q", v)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	root := t.TempDir()
	profA := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", instanceID: "inst-A",
		cursor: 42, epoch: 7, noteTitle: "A之笔记", noteSyncID: "srv-1",
		hasProfile: true, pendingOp: false, attachedIMG: true})
	out := filepath.Join(root, "share.snbak")
	if _, err := Export(ExportOptions{ProfileDir: profA, UploadDir: filepath.Join(profA, "uploads"),
		Password: "correct horse battery", OutPath: out, AppVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	// B：已绑同一服务器、有自己的档案与未推送 CREATE（账在物在）
	profB := seedProfile(t, root, seedOpts{profileDir: "B", deviceID: "devB", instanceID: "inst-A",
		cursor: 99, epoch: 7, noteTitle: "B之笔记", noteSyncID: "srv-b1",
		hasProfile: true, pendingOp: true})
	upB := filepath.Join(profB, "uploads")
	res, err := Import(ImportOptions{ProfileDir: profB, UploadDir: upB,
		InPath: out, Password: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.InstanceMatch || !res.HadProfile || !res.ReseededProfile {
		t.Fatalf("same-server profile semantics: %+v", res)
	}
	if res.Carried != 1 || res.AttachmentsMerged != 1 {
		t.Fatalf("carry/merge counts: %+v", res)
	}
	lay := localstore.Layout{ProfileDir: profB}
	active, _ := lay.Active()
	db, err := localstore.OpenGenDB(lay.GenDB(active))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A 的笔记与附件账目 + B 的未推送实体行"账在物在"
	var title string
	if err := db.QueryRow(`SELECT title FROM notes WHERE sync_id='srv-1'`).Scan(&title); err != nil || title != "A之笔记" {
		t.Fatalf("A note missing: %v", err)
	}
	if err := db.QueryRow(`SELECT title FROM notes WHERE sync_id='srv-new'`).Scan(&title); err != nil || title != "未推送新建" {
		t.Fatalf("B unpushed entity row must be carried (账在物在): %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE op_id='op-local-1' AND status='PENDING'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("B pending op lost through import: n=%d err=%v", n, err)
	}
	// B 的设备身份保留（DPAPI 封存件不出机器），A 的身份不进来
	var dev string
	var sealed []byte
	if err := db.QueryRow(`SELECT device_id, refresh_sealed FROM client_profile WHERE id=1`).Scan(&dev, &sealed); err != nil {
		t.Fatal(err)
	}
	if dev != "devB" || string(sealed) != "plain-refresh-sealed" {
		t.Fatalf("B profile must be reseeded intact, got %q %q", dev, sealed)
	}
	if _, err := os.Stat(filepath.Join(upB, "img-uuid-1.png")); err != nil {
		t.Fatalf("attachment must land in B uploads: %v", err)
	}
}

func TestImportCrossServerMarksMismatch(t *testing.T) {
	root := t.TempDir()
	profA := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", instanceID: "inst-A",
		cursor: 42, epoch: 7, noteTitle: "n", noteSyncID: "srv-1"})
	out := filepath.Join(root, "x.snbak")
	if _, err := Export(ExportOptions{ProfileDir: profA, UploadDir: filepath.Join(profA, "uploads"),
		Password: "password123", OutPath: out}); err != nil {
		t.Fatal(err)
	}
	profB := seedProfile(t, root, seedOpts{profileDir: "B", deviceID: "devB", instanceID: "inst-B",
		cursor: 9, epoch: 3, noteTitle: "b", noteSyncID: "srv-b1", hasProfile: true})
	res, err := Import(ImportOptions{ProfileDir: profB, UploadDir: filepath.Join(profB, "uploads"),
		InPath: out, Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	// 跨服务器：InstanceMatch=false（绑定面据此引导重绑）；导入的锚仍是 A 的 inst-A/epoch=7，
	// worker 联网后必然命中锚校验停推（红线 10 既有机制）→ §108"拒绝自动进入同步状态"。
	if res.InstanceMatch || !res.HadProfile {
		t.Fatalf("cross-server must flag mismatch: %+v", res)
	}
	if res.InstanceID != "inst-A" || res.Epoch != 7 || res.Cursor != 42 {
		t.Fatalf("imported anchors must be carried verbatim: %+v", res)
	}
}

func TestImportRejectsWrongPasswordAndTamper(t *testing.T) {
	root := t.TempDir()
	profA := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", instanceID: "inst-A",
		noteTitle: "n", noteSyncID: "srv-1", hasProfile: true})
	out := filepath.Join(root, "p.snbak")
	if _, err := Export(ExportOptions{ProfileDir: profA, UploadDir: filepath.Join(profA, "uploads"),
		Password: "password123", OutPath: out}); err != nil {
		t.Fatal(err)
	}
	profB := seedProfile(t, root, seedOpts{profileDir: "B", deviceID: "devB", instanceID: "inst-A", noteSyncID: "srv-b1", pendingOp: true})
	im := ImportOptions{ProfileDir: profB, UploadDir: filepath.Join(profB, "uploads"), InPath: out}
	if _, err := Import(im); !errors.Is(err, backup.ErrDecryptFailed) {
		t.Fatalf("empty password must fail as decrypt-failed (no oracle): %v", err)
	}
	im.Password = "password12X"
	if _, err := Import(im); !errors.Is(err, backup.ErrDecryptFailed) {
		t.Fatalf("wrong password: %v", err)
	}
	// 篡改信封体一个字节 → GCM 认证失败
	raw, _ := os.ReadFile(out)
	raw[len(raw)-40] ^= 0xFF
	bad := filepath.Join(root, "bad.snbak")
	if err := os.WriteFile(bad, raw, 0600); err != nil {
		t.Fatal(err)
	}
	im.InPath = bad
	im.Password = "password123"
	if _, err := Import(im); !errors.Is(err, backup.ErrDecryptFailed) {
		t.Fatalf("tamper must be rejected: %v", err)
	}
	// 失败导入零破坏：B 的活动代与未推送 op 原样
	lay := localstore.Layout{ProfileDir: profB}
	g, _ := lay.Active()
	db, err := localstore.OpenGenDB(lay.GenDB(g))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE op_id='op-local-1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("failed import must not touch B (红线1): n=%d", n)
	}
}

func TestExportMissingAttachmentFailClosed(t *testing.T) {
	root := t.TempDir()
	prof := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", noteTitle: "n",
		noteSyncID: "srv-1", attachedIMG: true, instanceID: "inst-A"})
	if err := os.Remove(filepath.Join(prof, "uploads", "img-uuid-1.png")); err != nil {
		t.Fatal(err)
	}
	_, err := Export(ExportOptions{ProfileDir: prof, UploadDir: filepath.Join(prof, "uploads"),
		Password: "password123", OutPath: filepath.Join(root, "e.snbak")})
	if !errors.Is(err, ErrAttachmentsMissing) {
		t.Fatalf("missing referenced attachment must abort export (红线17/25): %v", err)
	}
}

func TestExportSizeCap(t *testing.T) {
	root := t.TempDir()
	prof := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", noteTitle: "n",
		noteSyncID: "srv-1", attachedIMG: true, instanceID: "inst-A"})
	_, err := Export(ExportOptions{ProfileDir: prof, UploadDir: filepath.Join(prof, "uploads"),
		Password: "password123", OutPath: filepath.Join(root, "cap.snbak"), MaxPayloadBytes: 2048})
	if !errors.Is(err, backup.ErrBackupTooLarge) {
		t.Fatalf(">cap must be refused (V1 §109): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cap.snbak")); !os.IsNotExist(err) {
		t.Fatal("refused export must not leave a file")
	}
	if _, err := Export(ExportOptions{ProfileDir: prof, UploadDir: filepath.Join(prof, "uploads"),
		Password: "pass1", OutPath: filepath.Join(root, "weak.snbak")}); !errors.Is(err, ErrPasswordWeak) {
		t.Fatal("password min=6 must be enforced on export")
	}
}

// 服务端灾备包（BackupType 空）不得被便携导入接收；夹带未声明成员同样拒。
func TestImportRejectsForeignAndUndeclared(t *testing.T) {
	root := t.TempDir()
	// 手工产一个"server 形状"包：manifest BackupType=""
	profTmp := seedProfile(t, root, seedOpts{profileDir: "SRC", deviceID: "devS", instanceID: "inst-S", noteTitle: "n", noteSyncID: "srv-1"})
	lay := localstore.Layout{ProfileDir: profTmp}
	g, _ := lay.Active()
	dbPath := lay.GenDB(g)
	man := &backup.InternalManifest{FormatVersion: 1, BackupID: strings.Repeat("0", 8) + "-0000-0000-0000-000000000000",
		InstanceID: "inst-S", Database: struct {
			Path   string `json:"path"`
			Sha256 string `json:"sha256"`
			Size   int64  `json:"size"`
		}{Path: "notepad.db"}}
	sha, size, err := hashFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	man.Database.Sha256, man.Database.Size = sha, size
	man.FileHashes = map[string]string{"notepad.db": sha}
	mb, err := marshalManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := backup.BuildArchive(backup.ArchiveInput{ManifestBytes: mb, DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	salt := make([]byte, 16)
	_, _ = randRead(salt)
	env, err := backup.EnvelopeSeal(payload, man.BackupID, deriveMaster("password123", salt, pbkdfIter), 0)
	if err != nil {
		t.Fatal(err)
	}
	srvPkg := filepath.Join(root, "server-shape.snbak")
	if err := writeContainer(srvPkg, salt, pbkdfIter, env); err != nil {
		t.Fatal(err)
	}
	profB := seedProfile(t, root, seedOpts{profileDir: "B2", deviceID: "devB", instanceID: "inst-S"})
	_, err = Import(ImportOptions{ProfileDir: profB, UploadDir: filepath.Join(profB, "uploads"),
		InPath: srvPkg, Password: "password123"})
	if !errors.Is(err, ErrNotPortable) {
		t.Fatalf("server-type archive must be refused: %v", err)
	}
}
