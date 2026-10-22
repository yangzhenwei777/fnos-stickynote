package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"stickynote/backup"
	"stickynote/database"
)

// setupBackupHandler 初始化真实 schema DB + Manager，并把备份路由挂到一个带"伪鉴权"
// （注入 userID/username）的 gin engine 上，返回 engine。RequireAuth/RequireAdmin 中间件
// 本身在别处已测，这里只验备份 handler 的业务契约与脱敏。
func setupBackupHandler(t *testing.T) (*gin.Engine, *backup.Manager, string) {
	t.Helper()
	dataDir := tempInstanceDir(t) // 先注册 remove，后注册 close（LIFO→close 先行）：同根加固
	uploadDir := filepath.Join(dataDir, "upload")
	if err := database.Init(filepath.Join(dataDir, "notepad.db"), "1.0.167"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })

	m := backup.NewManager(dataDir, uploadDir, "1.0.167", 1)
	SetBackupManager(m)

	r := gin.New()
	auth := r.Group("/api", func(c *gin.Context) {
		c.Set("userID", int64(1))
		c.Set("username", "root")
		c.Set("role", "admin")
		c.Next()
	})
	auth.GET("/backup/status", BackupStatus)
	auth.GET("/backup/targets", ListBackupTargets)
	auth.POST("/backup/targets", CreateBackupTarget)
	auth.PUT("/backup/targets/:id", UpdateBackupTarget)
	auth.DELETE("/backup/targets/:id", DeleteBackupTarget)
	auth.POST("/backup/targets/:id/test", TestBackupTarget)
	auth.POST("/backup/targets/:id/run", RunBackupNow)
	auth.GET("/backup/jobs/:id", GetBackupJob)
	auth.POST("/backup/key/export", KeyExport)
	return r, m, dataDir
}

func doJSON(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		payload = *strings.NewReader(string(b))
	} else {
		payload = *strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, &payload)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestBackupTargetCRUDDesensitized(t *testing.T) {
	r, _, _ := setupBackupHandler(t)

	create := doJSON(t, r, "POST", "/api/backup/targets", gin.H{
		"name": "njy", "base_url": "https://dav.jianguoyun.com/dav/", "username": "me",
		"password": "super-secret-pw", "remote_path": "/bk/", "enabled": true,
		"retention_count": 7, "schedule_interval": "24h",
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", create.Code, create.Body.String())
	}
	var cid struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(create.Body.Bytes(), &cid)
	if cid.ID == 0 {
		t.Fatal("create returned id 0")
	}

	list := doJSON(t, r, "GET", "/api/backup/targets", nil)
	if list.Code != 200 {
		t.Fatalf("list code=%d", list.Code)
	}
	bodyStr := list.Body.String()
	// 关键安全断言：任何密钥字段/明文密码都不得出现在响应里
	for _, leak := range []string{"super-secret-pw", "secret_encrypted", "custom_ca_pem"} {
		if strings.Contains(bodyStr, leak) {
			t.Fatalf("列表响应泄露字段 %q: %s", leak, bodyStr)
		}
	}
	var resp struct {
		Targets []map[string]any `json:"targets"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &resp)
	if len(resp.Targets) != 1 || resp.Targets[0]["has_secret"] != true {
		t.Fatalf("targets 异常: %s", bodyStr)
	}
}

func TestBackupRunNow202ThenAsyncFail(t *testing.T) {
	r, m, _ := setupBackupHandler(t)
	// loopback 被禁的 127.0.0.1 目标：备份本地阶段可过，webdav 阶段被 SSRF 护栏拒 → job 最终 FAILED。
	create := doJSON(t, r, "POST", "/api/backup/targets", gin.H{
		"name": "local-blocked", "base_url": "http://127.0.0.1:5005/dav/", "username": "u",
		"password": "p", "remote_path": "/bk/", "enabled": true, "retention_count": 7,
		"schedule_interval": "24h", "allow_loopback": false,
	})
	var cid struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(create.Body.Bytes(), &cid)

	run := doJSON(t, r, "POST", "/api/backup/targets/"+i64toa(cid.ID)+"/run", nil)
	if run.Code != http.StatusAccepted {
		t.Fatalf("run code=%d body=%s", run.Code, run.Body.String())
	}
	var job struct {
		JobID    int64  `json:"job_id"`
		BackupID string `json:"backup_id"`
	}
	_ = json.Unmarshal(run.Body.Bytes(), &job)
	if job.JobID == 0 || job.BackupID == "" {
		t.Fatalf("202 应含 job_id + backup_id: %s", run.Body.String())
	}

	// 轮询到终态（异步 goroutine 结束、互斥释放），最长 10s
	deadline := time.Now().Add(10 * time.Second)
	var final map[string]any
	for time.Now().Before(deadline) {
		g := doJSON(t, r, "GET", "/api/backup/jobs/"+i64toa(job.JobID), nil)
		_ = json.Unmarshal(g.Body.Bytes(), &final)
		if s, _ := final["status"].(string); s == "SUCCESS" || s == "FAILED" || s == "INTERRUPTED" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final["status"] != "FAILED" {
		t.Fatalf("SSRF 目标应最终 FAILED，得 %v", final["status"])
	}
	if final["error_code"] != "WEBDAV_SSRF_BLOCKED" {
		t.Fatalf("error_code 应 WEBDAV_SSRF_BLOCKED，得 %v", final["error_code"])
	}
	// 等异步 goroutine 跑完收尾（SetTargetRunResult 在 FinishJob 之后），避免与 Cleanup 关库竞争
	time.Sleep(250 * time.Millisecond)
	_ = m // manager 已由 SetBackupManager 注入
}

func TestBackupRunNowConflictWhenRunning(t *testing.T) {
	r, m, _ := setupBackupHandler(t)
	create := doJSON(t, r, "POST", "/api/backup/targets", gin.H{
		"name": "x", "base_url": "http://127.0.0.1:5005/dav/", "username": "u",
		"password": "p", "enabled": true, "retention_count": 7, "schedule_interval": "24h",
	})
	var cid struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(create.Body.Bytes(), &cid)

	// 先手工占住互斥（模拟已在跑），再点 run → 409
	jobID, bid, err := m.BeginJob(cid.ID, "manual")
	if err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	run := doJSON(t, r, "POST", "/api/backup/targets/"+i64toa(cid.ID)+"/run", nil)
	if run.Code != http.StatusConflict {
		t.Fatalf("running 时 run 应 409，得 %d", run.Code)
	}
	// 释放：同步跑掉这个已 Begin 的 job（SSRF 目标 → 快速失败并解锁）
	_ = m.RunBackupJob(cid.ID, "manual", jobID, bid)
}

func TestBackupKeyExportRejectsBadPassword(t *testing.T) {
	r, _, _ := setupBackupHandler(t)
	// 没有匹配的 admin 用户 → AuthenticateUser 失败 → 403，绝不回显密钥
	w := doJSON(t, r, "POST", "/api/backup/key/export", gin.H{"password": "wrong"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("口令错误应 403，得 %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "key:") {
		t.Fatal("错误口令却疑似泄露密钥内容")
	}
}

func TestBackupStatusShape(t *testing.T) {
	r, _, _ := setupBackupHandler(t)
	w := doJSON(t, r, "GET", "/api/backup/status", nil)
	if w.Code != 200 {
		t.Fatalf("status code=%d", w.Code)
	}
	var s map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if _, ok := s["running"].(bool); !ok {
		t.Fatalf("status 应含 bool running: %s", w.Body.String())
	}
}

func i64toa(n int64) string { return strconv.FormatInt(n, 10) }
