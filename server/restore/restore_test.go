package restore

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stickynote/backup"
	"stickynote/database"
	"stickynote/model"
)

// ---- 测试脚手架：真库 + 真归档 ----

func buildInstance(t *testing.T) (dataDir, uploadDir, dbPath string) {
	t.Helper()
	dataDir = t.TempDir()
	uploadDir = filepath.Join(dataDir, "upload")
	dbPath = filepath.Join(dataDir, "notepad.db")
	if err := database.Init(dbPath, "1.0.168"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() {
		if database.DB != nil {
			database.DB.Close()
			database.DB = nil
		}
	})

	if _, err := database.DB.Exec(`INSERT INTO users(username,password_hash,role) VALUES('admin','x','admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO notes(user_id,title,content) VALUES(1,'baseline','first')`); err != nil {
		t.Fatal(err)
	}
	mk, err := backup.LoadOrCreateMasterKey(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = mk
	if _, err := backup.LoadOrCreateInstanceID(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dataDir, ".jwt_secret"), "secret-A\n"); err != nil {
		t.Fatal(err)
	}
	return dataDir, uploadDir, dbPath
}

func writeFile(p, s string) error { return write0600(p, []byte(s)) }

// makeArchive 对当前实例做一次备份并返回归档字节 + backupID。
func makeArchive(t *testing.T, dataDir, uploadDir string) ([]byte, string) {
	t.Helper()
	m := backup.NewManager(dataDir, uploadDir, "1.0.168", 1)
	bid, err := backup.NewBackupID()
	if err != nil {
		t.Fatal(err)
	}
	res, err := backup.CreateBackup(m.Env(), bid, false)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	b, err := readAll(res.SnbakPath)
	if err != nil {
		t.Fatal(err)
	}
	return b, bid
}

func addNote(t *testing.T, title string) {
	t.Helper()
	if _, err := database.DB.Exec(`INSERT INTO notes(user_id,title,content) VALUES(1,?, 'after')`, title); err != nil {
		t.Fatal(err)
	}
}
func noteCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM notes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func tokenVersion(t *testing.T) int {
	t.Helper()
	var v int
	if err := database.DB.QueryRow("SELECT token_version FROM users WHERE id=1").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func defaultOpts(dataDir, uploadDir, dbPath string) Options {
	return Options{DataDir: dataDir, UploadDir: uploadDir, DBPath: dbPath, AppVersion: "1.0.168"}
}

// ---- 用例 ----

func TestRestoreHappyPathSameInstance(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	archive, bid := makeArchive(t, dataDir, uploadDir)
	addNote(t, "post-backup-note") // 备份之后新增，恢复后应消失
	if noteCount(t) != 2 {
		t.Fatalf("pre-state want 2 notes, got %d", noteCount(t))
	}
	before := tokenVersion(t)

	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	rid := "restore-happy-1"
	if err := c.Run(Request{RestoreID: rid, BackupID: bid, ArchiveBytes: archive}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if database.IsRestoring() {
		t.Fatal("安全窗口未关闭")
	}
	if got := noteCount(t); got != 1 {
		t.Fatalf("恢复后应回到备份时 1 条笔记，得 %d", got)
	}
	// journal 已清理
	if j, _ := loadJournal(dataDir); j != nil {
		t.Fatal("成功恢复后日志应被清除")
	}
	// restore_generation 落子
	if v, _ := model.GetConfig("restore_generation"); v != rid {
		t.Fatalf("restore_generation=%q want %q", v, rid)
	}
	// 会话吊销（幂等首次应 +1）
	if tokenVersion(t) <= before {
		t.Fatalf("token_version 未 bump: %d -> %d", before, tokenVersion(t))
	}
	// .pre-restore 原件应被清理
	if exists(filepath.Join(dataDir, "notepad.db.pre-restore-"+rid)) {
		t.Fatal("成功恢复后 .pre-restore 原件应被删除")
	}
}

func TestRestoreRollbackOnMidFailure(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	archive, bid := makeArchive(t, dataDir, uploadDir)
	addNote(t, "must-survive-rollback") // 恢复失败 → 这条仍在

	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	c.failAt = func(next State) error {
		if next == StateMigrated { // 已换文件、未重开迁移 → 强制失败回滚
			return errors.New("injected failure at migrate")
		}
		return nil
	}
	err := c.Run(Request{RestoreID: "rb-1", BackupID: bid, ArchiveBytes: archive})
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("应返回注入错误，得 %v", err)
	}
	if database.IsRestoring() {
		t.Fatal("回滚后安全窗口应关闭")
	}
	// 原库完好：备份后新增的笔记仍在
	if got := noteCount(t); got != 2 {
		t.Fatalf("回滚后应保留原 2 条笔记，得 %d", got)
	}
	if j, _ := loadJournal(dataDir); j != nil {
		t.Fatal("回滚后日志应清除")
	}
	if exists(filepath.Join(dataDir, "notepad.db.pre-restore-rb-1")) {
		t.Fatal("回滚后 .pre-restore 应被移回原件")
	}
}

func TestRestoreIdentityMismatchRefused(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	archive, bid := makeArchive(t, dataDir, uploadDir)
	// 篡改当前 instance_id 制造身份不符
	if err := writeFile(filepath.Join(dataDir, ".instance_id"), "DIFFERENT-INSTANCE\n"); err != nil {
		t.Fatal(err)
	}
	addNote(t, "untouched")
	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	err := c.Run(Request{RestoreID: "idm-1", BackupID: bid, ArchiveBytes: archive})
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("应 ErrIdentityMismatch，得 %v", err)
	}
	// 无副作用：日志不存在、笔记未动、未关窗
	if j, _ := loadJournal(dataDir); j != nil {
		t.Fatal("身份校验失败不应写日志")
	}
	if noteCount(t) != 2 {
		t.Fatal("身份校验失败不应触碰数据")
	}
}

func TestRestoreRefuseWhenJWTEnv(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	archive, bid := makeArchive(t, dataDir, uploadDir)
	opt := defaultOpts(dataDir, uploadDir, dbPath)
	opt.JWTSecretIsEnv = true
	c := New(opt)
	err := c.Run(Request{RestoreID: "env-1", BackupID: bid, ArchiveBytes: archive})
	if !errors.Is(err, ErrRefuseEnv) {
		t.Fatalf("应 ErrRefuseEnv，得 %v", err)
	}
}

func TestRestoreNoUsableKey(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	archive, bid := makeArchive(t, dataDir, uploadDir)
	// 换掉磁盘主密钥 → 磁盘/pending/内联三源都解不开
	if _, err := backup.NewBackupID(); err != nil {
		t.Fatal(err)
	}
	other, _ := randomKey()
	if err := backup.SetMasterKey(dataDir, other); err != nil {
		t.Fatal(err)
	}
	addNote(t, "keep")
	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	err := c.Run(Request{RestoreID: "nk-1", BackupID: bid, ArchiveBytes: archive})
	if !errors.Is(err, ErrNoUsableKey) {
		t.Fatalf("应 ErrNoUsableKey，得 %v", err)
	}
	if noteCount(t) != 2 {
		t.Fatal("解不开密钥不应触碰数据")
	}
}

func TestRestoreInlineKeyAdoptsIdentity(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	archive, bid := makeArchive(t, dataDir, uploadDir)
	// 取当前密钥文本，模拟"灾难恢复到新实例"：清空磁盘密钥+instance，用内联文本恢复
	mk, _ := backup.ReadMasterKey(dataDir)
	inst := currentInstanceID(dataDir)
	bundle := backup.ExportBundle(mk, inst)

	other, _ := randomKey()
	_ = backup.SetMasterKey(dataDir, other)
	_ = backup.SetInstanceID(dataDir, "fresh-instance-xyz")
	addNote(t, "will-be-overwritten")

	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	if err := c.Run(Request{RestoreID: "ik-1", BackupID: bid, ArchiveBytes: archive, KeyFileText: bundle, AllowIdentityChange: true}); err != nil {
		t.Fatalf("inline-key restore: %v", err)
	}
	if noteCount(t) != 1 {
		t.Fatalf("应恢复到备份时 1 条，得 %d", noteCount(t))
	}
	if currentInstanceID(dataDir) != inst {
		t.Fatalf("应采纳归档实例 %q，得 %q", inst, currentInstanceID(dataDir))
	}
	// 采纳后运行密钥应能再次解开同档（验证 SetMasterKey 生效）
	if _, _, err := backup.EnvelopeOpen(archive, mustReadKey(t, dataDir), backup.DefaultArchiveCapBytes); err != nil {
		t.Fatalf("采纳后磁盘密钥应可解档: %v", err)
	}
}

func TestStartupRecoverRollbacksSwapped(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	dbFile := filepath.Join(dataDir, "notepad.db")
	pre := dbFile + ".pre-restore-crash"
	// 与真实 swapOut 一致：先 checkpoint 折叠 WAL，再关库，Windows 文件锁才会释放；
	// 关闭后句柄释放有微小时差，改名用有界重试。
	if database.DB != nil {
		_, _ = database.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		database.DB.Close()
		database.DB = nil
	}
	if err := renameWithRetry(dbFile, pre); err != nil {
		t.Fatal(err)
	}
	j := &Journal{
		RestoreID: "crash-1", BackupID: "b", State: StateSwapped,
		StageDir: filepath.Join(dataDir, ".restore-stage-crash-1"),
		Ops:      []RenameOp{{Kind: "file", Original: dbFile, PreRestore: pre}},
	}
	j.JournalPath = journalPath(dataDir)
	if err := j.write(); err != nil {
		t.Fatal(err)
	}

	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	if err := c.StartupRecover(); err != nil {
		t.Fatalf("StartupRecover: %v", err)
	}
	if !exists(dbFile) || exists(pre) {
		t.Fatalf("崩溃恢复应把原库移回：db=%v pre=%v", exists(dbFile), exists(pre))
	}
	if j, _ := loadJournal(dataDir); j != nil {
		t.Fatal("恢复后日志应清除")
	}
	// 重开确认库完好
	if err := database.Init(dbPath, "1.0.168"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	if noteCount(t) < 1 {
		t.Fatal("原库内容应完好")
	}
	_ = uploadDir
}

// TestStartupRecoverCorruptJournalFails（v21.133 GPT-04，审计严重）：
// 崩溃恢复日志损坏时 StartupRecover 必须返回 error，供 server.go fail-closed 拒绝开库服务，
// 而非把半恢复态当正常库对外提供 API。反向：无 journal 属合法空态，应返回 nil（no-op）。
func TestStartupRecoverCorruptJournalFails(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)

	// 合法空态：无 journal → no-op，不报错。
	if err := New(defaultOpts(dataDir, uploadDir, dbPath)).StartupRecover(); err != nil {
		t.Fatalf("无 journal 时 StartupRecover 应返回 nil, got %v", err)
	}

	// 损坏 journal → 必须报错（server.go 据此 fail-closed 停止启动）。
	if err := os.WriteFile(journalPath(dataDir), []byte("{ this is not valid json "), 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(defaultOpts(dataDir, uploadDir, dbPath)).StartupRecover(); err == nil {
		t.Fatal("损坏 journal 时 StartupRecover 必须返回 error（GPT-04 fail-closed 前提）")
	}
}

func TestFinalizeIdempotent(t *testing.T) {
	_, _, _ = buildInstance(t) // 提供真库
	first, err := model.MarkRestoreFinalized("dup-1")
	if err != nil || !first {
		t.Fatalf("first finalize want true, got %v err=%v", first, err)
	}
	if err := model.RevokeAllSessions(); err != nil {
		t.Fatal(err)
	}
	second, err := model.MarkRestoreFinalized("dup-1")
	if err != nil || second {
		t.Fatalf("second finalize want false(幂等跳过), got %v err=%v", second, err)
	}
}

// ---- test helpers ----

func write0600(p string, b []byte) error { return os.WriteFile(p, b, 0600) }
func readAll(p string) ([]byte, error)   { return os.ReadFile(p) }
func renameFile(a, b string) error       { return os.Rename(a, b) }
func renameWithRetry(a, b string) error {
	var err error
	for i := 0; i < 40; i++ {
		if err = os.Rename(a, b); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}
func exists(p string) bool { _, err := os.Stat(p); return err == nil }
func mustReadKey(t *testing.T, dir string) []byte {
	t.Helper()
	k, err := backup.ReadMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func randomKey() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}
