package backup

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/webdav"

	"stickynote/database"
	"stickynote/model"
)

// ---- 测试脚手架 ----

func newFakeWebDAV(t *testing.T) *httptest.Server {
	t.Helper()
	h := &webdav.Handler{
		Prefix:     "/dav",
		FileSystem: webdav.NewMemFS(),
		LockSystem: webdav.NewMemLS(),
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// newFakeWebDAVRejectDelete：PUT/MOVE/GET 正常，但任何 DELETE 返 403——
// 用于验证远端删除失败时 prune 保留 placement + 返回 warnings（不"失忆删除"）。
func newFakeWebDAVRejectDelete(t *testing.T) *httptest.Server {
	t.Helper()
	h := &webdav.Handler{
		Prefix:     "/dav",
		FileSystem: webdav.NewMemFS(),
		LockSystem: webdav.NewMemLS(),
	}
	failer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(failer)
	t.Cleanup(srv.Close)
	return srv
}

// initRealDB 用真实应用 schema 初始化全局 database.DB（含 backup_* 表），
// 并灌入 1 用户 / 1 笔记 / 1 attached 附件（pic.png）。返回 dataDir、uploadDir。
func initRealDB(t *testing.T) (dataDir, uploadDir string) {
	t.Helper()
	dataDir = t.TempDir()
	uploadDir = t.TempDir()
	if err := database.Init(filepath.Join(dataDir, "notepad.db"), "1.0.167"); err != nil {
		t.Fatalf("database.Init: %v", err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })

	db := database.DB
	if _, err := db.Exec(`INSERT INTO users(username,password_hash,role) VALUES('admin','x','admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notes(user_id,title,content) VALUES(1,'t','hello world')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO uploads(filename,user_id,status) VALUES('pic.png',1,'attached')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO note_uploads(note_id,filename) VALUES(1,'pic.png')`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploadDir, "pic.png"), []byte("PNGDATA-123"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".jwt_secret"), []byte("topsecret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dataDir, uploadDir
}

func makeTarget(t *testing.T, m *Manager, baseURL string, loopback bool) int64 {
	t.Helper()
	enc, err := m.EncryptSecret("app-password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := model.CreateBackupTarget(model.BackupTarget{
		Name: "test", Type: "webdav", BaseURL: baseURL, Username: "user",
		SecretEncrypted: enc, RemotePath: "/backups/", Enabled: true,
		RetentionCount: 7, ScheduleInterval: "24h", AllowLoopback: loopback,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func placementStatus(t *testing.T, backupID string, targetID int64) string {
	t.Helper()
	var s string
	err := database.DB.QueryRow(`SELECT status FROM backup_placements WHERE backup_id=? AND target_id=?`, backupID, targetID).Scan(&s)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func jobStatus(t *testing.T, jobID int64) (string, string) {
	t.Helper()
	var status, code string
	if err := database.DB.QueryRow(`SELECT status, error_code FROM backup_jobs WHERE id=?`, jobID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	return status, code
}

// ---- 用例 ----

func TestManagerRunBackupHappyPath(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.167", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)

	jobID, bid, err := m.BeginJob(tid, "manual")
	if err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	if err := m.runJob(tid, "manual", jobID, bid); err != nil {
		t.Fatalf("runJob: %v", err)
	}

	if st, _ := jobStatus(t, jobID); st != "SUCCESS" {
		t.Fatalf("job status=%s want SUCCESS", st)
	}
	if got := placementStatus(t, bid, tid); got != "VERIFIED" {
		t.Fatalf("placement=%s want VERIFIED", got)
	}
	// 远端确有 .snbak + .index.json 两个对象
	c, err := m.clientFor(mustGetTarget(t, tid))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.Head("/backups/" + bid + ".snbak"); err != nil || !ok {
		t.Fatalf("远端缺归档 ok=%v err=%v", ok, err)
	}
	if _, ok, err := c.Head("/backups/" + bid + ".index.json"); err != nil || !ok {
		t.Fatalf("远端缺 index ok=%v err=%v", ok, err)
	}
	// 本地暂存 .snbak 应被清理
	if _, err := os.Stat(filepath.Join(m.Env().tmpDir(), bid+".snbak")); !os.IsNotExist(err) {
		t.Fatal("本地暂存未清理")
	}
	// 回归：快照列表必须能读出来（曾因 ListBackupSnapshots 把 RFC3339 文本扫进 int → Scan 报错 → 快照页恒空/500）
	snaps, err := model.ListBackupSnapshots(50)
	if err != nil {
		t.Fatalf("ListBackupSnapshots 报错（快照页会空白）: %v", err)
	}
	var found *model.BackupSnapshot
	for i := range snaps {
		if snaps[i].BackupID == bid {
			found = &snaps[i]
		}
	}
	if found == nil {
		t.Fatalf("快照列表未含本次备份 %s（共 %d 条）", bid[:8], len(snaps))
	}
	if found.Status != "LOCAL_VERIFIED" {
		t.Fatalf("快照状态=%s want LOCAL_VERIFIED", found.Status)
	}
	if found.CreatedAt.IsZero() {
		t.Fatal("快照 created_at 未被正确解析（前端会显示 Invalid Date）")
	}
}

func TestManagerRunBackupSSRFBlocked(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t) // 监听 127.0.0.1
	m := NewManager(dataDir, uploadDir, "1.0.167", 1)
	// AllowLoopback=false → 连本机 httptest 应被 SSRF 护栏拒
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", false)

	jobID, bid, err := m.BeginJob(tid, "manual")
	if err != nil {
		t.Fatal(err)
	}
	err = m.runJob(tid, "manual", jobID, bid)
	if err == nil {
		t.Fatal("期望 SSRF 拦截失败")
	}
	if !strings.Contains(err.Error(), "SSRF_BLOCKED") && errCodeString(err) != "WEBDAV_SSRF_BLOCKED" {
		t.Fatalf("err=%v want WEBDAV_SSRF_BLOCKED", err)
	}
	st, code := jobStatus(t, jobID)
	if st != "FAILED" || code != "WEBDAV_SSRF_BLOCKED" {
		t.Fatalf("job=%s/%s want FAILED/WEBDAV_SSRF_BLOCKED", st, code)
	}
	_ = bid
	// 失败应累加目标连续失败计数
	tt := mustGetTarget(t, tid)
	if tt.ConsecutiveFailures != 1 {
		t.Fatalf("consecutive_failures=%d want 1", tt.ConsecutiveFailures)
	}
}

func TestManagerAlreadyRunningMutex(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.167", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)

	if _, _, err := m.BeginJob(tid, "manual"); err != nil {
		t.Fatal(err)
	}
	// 未 runJob（未 end）→ 第二个 BeginJob 应忙
	if _, _, err := m.BeginJob(tid, "manual"); err != ErrAlreadyRunning {
		t.Fatalf("第二个 BeginJob err=%v want ErrAlreadyRunning", err)
	}
	// 主密钥操作也共用同一互斥 → ExportKey 忙
	if _, err := m.ExportKey(); err != ErrAlreadyRunning {
		t.Fatalf("ExportKey err=%v want ErrAlreadyRunning", err)
	}
	m.end()
	// 释放后可再导出
	if _, err := m.ExportKey(); err != nil {
		t.Fatalf("释放后 ExportKey 失败: %v", err)
	}
}

func TestManagerPruneNeverToZero(t *testing.T) {
	// retention=1：跑两次备份，第二次成功后旧的被删，但绝不为零（最新一个保留）。
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.167", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)
	if err := model.SetTargetRetention(tid, 1); err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 2)
	for i := 0; i < 2; i++ {
		jobID, bid, err := m.BeginJob(tid, "manual")
		if err != nil {
			t.Fatalf("begin %d: %v", i, err)
		}
		if err := m.runJob(tid, "manual", jobID, bid); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		ids[i] = bid
	}
	oldest, newest := ids[0], ids[1]
	c, _ := m.clientFor(mustGetTarget(t, tid))

	if _, ok, _ := c.Head("/backups/" + newest + ".snbak"); !ok {
		t.Fatal("最新归档不应被轮转删除（never-to-zero）")
	}
	if placementStatus(t, newest, tid) != "VERIFIED" {
		t.Fatal("最新放置应 VERIFIED")
	}
	if _, ok, _ := c.Head("/backups/" + oldest + ".snbak"); ok {
		t.Fatal("超保留数的旧归档应被删除")
	}
	if placementStatus(t, oldest, tid) != "" {
		t.Fatal("旧放置记录应随远端删除清理")
	}
}

func mustGetTarget(t *testing.T, id int64) model.BackupTarget {
	t.Helper()
	tt, err := model.GetBackupTarget(id)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

// TestSnapshotListRemoteInvariant 编码"快照列表 ↔ 远端副本"不变量（v21.131 测试轮）。
// 正是 v21.130 孤儿快照行 bug（面板显示但远端无副本、点下载必失败）的回归防线：
// 跑 3 次备份 retention=2 → 列表恰 2 条且全 has_remote=true、零孤儿行、远端恰 2 份 .snbak；
// 再手动删 1 条 → 列表 1 条、远端 1 份。
func TestSnapshotListRemoteInvariant(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAV(t)
	m := NewManager(dataDir, uploadDir, "1.0.171", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)
	if err := model.SetTargetRetention(tid, 2); err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		jobID, bid, err := m.BeginJob(tid, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if err := m.runJob(tid, "manual", jobID, bid); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		ids[i] = bid
	}

	find := func(snaps []model.BackupSnapshot, id string) *model.BackupSnapshot {
		for i := range snaps {
			if snaps[i].BackupID == id {
				return &snaps[i]
			}
		}
		return nil
	}

	snaps, err := model.ListBackupSnapshots(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("列表应恰 2 条，得 %d", len(snaps))
	}
	for _, s := range snaps {
		if !s.HasRemote {
			t.Fatalf("列表出现 has_remote=false 孤儿行 %s", s.BackupID)
		}
	}
	if find(snaps, ids[2]) == nil || find(snaps, ids[1]) == nil {
		t.Fatal("最新两条快照应保留")
	}
	if find(snaps, ids[0]) != nil {
		t.Fatal("最旧快照应被轮转删除（含孤儿行清理）")
	}

	c, err := m.clientFor(mustGetTarget(t, tid))
	if err != nil {
		t.Fatal(err)
	}
	countSnbak := func() int {
		names, e := c.ListNames("/backups/")
		if e != nil {
			t.Fatal(e)
		}
		n := 0
		for _, s := range names {
			if strings.HasSuffix(s, ".snbak") {
				n++
			}
		}
		return n
	}
	if got := countSnbak(); got != 2 {
		t.Fatalf("远端应恰 2 份 .snbak，得 %d", got)
	}

	// 手动删最新一条 → 列表 1、远端 1
	if _, _, err := m.DeleteSnapshot(ids[2]); err != nil {
		t.Fatal(err)
	}
	snaps2, _ := model.ListBackupSnapshots(50)
	if len(snaps2) != 1 || snaps2[0].BackupID != ids[1] {
		t.Fatalf("删后列表应剩 1 条(%s)，得 %+v", ids[1][:8], snaps2)
	}
	if got := countSnbak(); got != 1 {
		t.Fatalf("删后远端应 1 份 .snbak，得 %d", got)
	}
}

// TestPruneKeepsPlacementOnRemoteDeleteFailure 编码 v21.129 修复的核心不变量：远端删除
// 失败时 prune **保留 placement**（下次重试）并返回 warnings，绝不"本地以为删了、远端还在"
// 的失忆删除。用 DELETE 恒返 403 的假 WebDAV 复现 fnOS 删除失败场景。
func TestPruneKeepsPlacementOnRemoteDeleteFailure(t *testing.T) {
	dataDir, uploadDir := initRealDB(t)
	srv := newFakeWebDAVRejectDelete(t)
	m := NewManager(dataDir, uploadDir, "1.0.171", 1)
	tid := makeTarget(t, m, strings.TrimRight(srv.URL, "/")+"/dav/", true)

	ids := make([]string, 2)
	for i := 0; i < 2; i++ {
		jobID, bid, err := m.BeginJob(tid, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if err := m.runJob(tid, "manual", jobID, bid); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		ids[i] = bid
	}
	// 调小保留数 → 应轮转最旧那条，但远端 DELETE 被拦成 403
	if err := model.SetTargetRetention(tid, 1); err != nil {
		t.Fatal(err)
	}
	deleted, warns, err := m.PruneTarget(tid)
	if err != nil {
		t.Fatalf("PruneTarget: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("DELETE 被拦，deleted 应 0，得 %d", deleted)
	}
	if len(warns) == 0 {
		t.Fatal("远端删除失败应返回 warnings（供 handler 如实透传，不再静默）")
	}
	// 关键：旧 placement 必须保留 VERIFIED（下次重试），不能被"失忆删除"
	if st := placementStatus(t, ids[0], tid); st != "VERIFIED" {
		t.Fatalf("远端删除失败时旧 placement 应保留 VERIFIED，得 %q", st)
	}
}
