package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/webdav"

	"stickynote/backup"
	"stickynote/database"
	"stickynote/model"
)

// v21.129 备份手动删除 + 保留份数即时轮转的 handler 层端到端测试。
// 用一个可写的 loopback fake WebDAV（allow_loopback=true）跑真实备份，再验 HTTP 契约。

// setupDeleteHandler 初始化 DB + Manager + 可写 fake WebDAV，挂载删除/更新路由。
// 返回 engine、manager、webdav base url。
func setupDeleteHandler(t *testing.T) (*gin.Engine, *backup.Manager, string) {
	t.Helper()
	dataDir := t.TempDir()
	uploadDir := filepath.Join(dataDir, "upload")
	if err := database.Init(filepath.Join(dataDir, "notepad.db"), "1.0.169"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })

	// 最小业务数据（备份快照统计用）
	if _, err := database.DB.Exec(`INSERT INTO users(username,password_hash,role) VALUES('admin','x','admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO notes(user_id,title,content) VALUES(1,'t','hello')`); err != nil {
		t.Fatal(err)
	}

	h := &webdav.Handler{Prefix: "/dav", FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base := strings.TrimRight(srv.URL, "/") + "/dav/"

	m := backup.NewManager(dataDir, uploadDir, "1.0.169", 1)
	SetBackupManager(m)

	r := gin.New()
	auth := r.Group("/api", func(c *gin.Context) {
		c.Set("userID", int64(1))
		c.Set("username", "root")
		c.Set("role", "admin")
		c.Next()
	})
	auth.POST("/backup/targets", CreateBackupTarget)
	auth.PUT("/backup/targets/:id", UpdateBackupTarget)
	auth.DELETE("/backup/snapshots/:backup_id", DeleteBackupSnapshot)
	auth.GET("/backup/snapshots", ListBackupSnapshots)
	return r, m, base
}

// createLoopbackTarget 建一个指向 fake WebDAV 的目标（allow_loopback=true），返回 id。
func createLoopbackTarget(t *testing.T, r *gin.Engine, base string, retention int) int64 {
	t.Helper()
	w := doJSON(t, r, "POST", "/api/backup/targets", gin.H{
		"name": "local", "base_url": base, "username": "u", "password": "p",
		"remote_path": "/backups/", "enabled": true, "retention_count": retention,
		"schedule_interval": "24h", "allow_loopback": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create target code=%d body=%s", w.Code, w.Body.String())
	}
	var cid struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &cid)
	return cid.ID
}

// runBackupSync 同步跑一次真实备份，返回 backup_id。
func runBackupSync(t *testing.T, m *backup.Manager, tid int64) string {
	t.Helper()
	jobID, bid, err := m.BeginJob(tid, "manual")
	if err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	if err := m.RunBackupJob(tid, "manual", jobID, bid); err != nil {
		t.Fatalf("RunBackupJob: %v", err)
	}
	return bid
}

func TestDeleteBackupSnapshotHappyPath(t *testing.T) {
	r, m, base := setupDeleteHandler(t)
	tid := createLoopbackTarget(t, r, base, 7)

	bid := runBackupSync(t, m, tid)

	// 前置：快照列表含该备份
	w := doJSON(t, r, "GET", "/api/backup/snapshots", nil)
	if !strings.Contains(w.Body.String(), bid) {
		t.Fatalf("前置：快照列表应含 %s，得 %s", bid[:8], w.Body.String())
	}

	// DELETE
	del := doJSON(t, r, "DELETE", "/api/backup/snapshots/"+bid, nil)
	if del.Code != http.StatusOK {
		t.Fatalf("delete code=%d body=%s", del.Code, del.Body.String())
	}
	var resp struct {
		OK            bool     `json:"ok"`
		DeletedRemote int      `json:"deleted_remote"`
		Warnings      []string `json:"warnings"`
	}
	_ = json.Unmarshal(del.Body.Bytes(), &resp)
	if !resp.OK || resp.DeletedRemote != 1 || len(resp.Warnings) != 0 {
		t.Fatalf("delete 响应异常: %+v", resp)
	}

	// 快照列表不再含该备份
	w2 := doJSON(t, r, "GET", "/api/backup/snapshots", nil)
	if strings.Contains(w2.Body.String(), bid) {
		t.Fatalf("删除后快照列表不应含 %s，得 %s", bid[:8], w2.Body.String())
	}
	// 本地快照行确实没了
	if ex, _ := model.SnapshotExists(bid); ex {
		t.Fatal("删除后本地快照行应不存在")
	}
}

func TestDeleteBackupSnapshotConflictWhenRunning(t *testing.T) {
	r, m, base := setupDeleteHandler(t)
	tid := createLoopbackTarget(t, r, base, 7)
	bid := runBackupSync(t, m, tid)

	// 占住运行锁 → DELETE 应 409
	if _, _, err := m.BeginJob(tid, "manual"); err != nil {
		t.Fatal(err)
	}
	del := doJSON(t, r, "DELETE", "/api/backup/snapshots/"+bid, nil)
	if del.Code != http.StatusConflict {
		t.Fatalf("运行中删除应 409，得 %d body=%s", del.Code, del.Body.String())
	}
	if !strings.Contains(del.Body.String(), "BACKUP_ALREADY_RUNNING") {
		t.Fatalf("409 应含 code BACKUP_ALREADY_RUNNING，得 %s", del.Body.String())
	}
	m.Unlock()
}

func TestDeleteBackupSnapshotMissingID(t *testing.T) {
	r, _, base := setupDeleteHandler(t)
	_ = createLoopbackTarget(t, r, base, 7)
	// 空 backup_id（路由 :backup_id 不会匹配空段，这里验一个不存在的 id → 仍 200 幂等，
	// 因为 DeleteSnapshot 对无放置的 id 删 0 份、本地删 0 行不报错）
	del := doJSON(t, r, "DELETE", "/api/backup/snapshots/nonexistent-backup-id", nil)
	if del.Code != http.StatusOK {
		t.Fatalf("删除不存在备份应幂等 200，得 %d body=%s", del.Code, del.Body.String())
	}
	var resp struct {
		OK            bool `json:"ok"`
		DeletedRemote int  `json:"deleted_remote"`
	}
	_ = json.Unmarshal(del.Body.Bytes(), &resp)
	if !resp.OK || resp.DeletedRemote != 0 {
		t.Fatalf("不存在备份应 ok=true deleted_remote=0，得 %+v", resp)
	}
}

func TestUpdateTargetRetentionLoweredTriggersPrune(t *testing.T) {
	r, m, base := setupDeleteHandler(t)
	tid := createLoopbackTarget(t, r, base, 7)

	// 跑 3 份备份
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		ids[i] = runBackupSync(t, m, tid)
	}

	// PUT 把保留份数调到 1 → 应立即轮转 2 份最旧
	up := doJSON(t, r, "PUT", "/api/backup/targets/"+i64toa(tid), gin.H{
		"name": "local", "base_url": base, "username": "u", "password": "",
		"remote_path": "/backups/", "enabled": true, "retention_count": 1,
		"schedule_interval": "24h", "allow_loopback": true,
	})
	if up.Code != http.StatusOK {
		t.Fatalf("update code=%d body=%s", up.Code, up.Body.String())
	}
	var ur struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(up.Body.Bytes(), &ur)
	if !strings.Contains(ur.Message, "清理") {
		t.Fatalf("保留份数调小应报告立即清理，得 message=%q", ur.Message)
	}

	// 最新那份仍在，两份最旧的本地快照行应被孤儿清理
	newest := ids[2]
	if ex, _ := model.SnapshotExists(newest); !ex {
		t.Fatal("最新快照应保留")
	}
	for _, old := range []string{ids[0], ids[1]} {
		if ex, _ := model.SnapshotExists(old); ex {
			t.Fatalf("旧档 %s 应被轮转清理（孤儿快照行）", old[:8])
		}
	}
}
