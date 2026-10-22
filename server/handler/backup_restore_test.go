package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/webdav"

	"stickynote/backup"
	"stickynote/database"
	"stickynote/model"
	"stickynote/restore"
)

// setupRestoreHandler 建真库 + 真 manager + 内存 WebDAV，注入一个可登录 admin，
// 把 restore/jobs 路由挂到伪鉴权组，并预先产出一次备份到 VERIFIED，返回 engine 与 backup_id。
func setupRestoreHandler(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	dataDir := t.TempDir()
	uploadDir := filepath.Join(dataDir, "upload")
	if err := database.Init(filepath.Join(dataDir, "notepad.db"), "1.0.168"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() {
		if database.DB != nil {
			database.DB.Close()
			database.DB = nil
		}
	})

	hash, _ := bcrypt.GenerateFromPassword([]byte("Adm1n-Passw0rd"), bcrypt.DefaultCost)
	if _, err := database.DB.Exec(`INSERT INTO users(username,password_hash,role) VALUES('rootadmin',?,'admin')`, string(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO notes(user_id,title,content) VALUES(1,'baseline','v1')`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".jwt_secret"), []byte("s3cr3t\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.LoadOrCreateMasterKey(dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.LoadOrCreateInstanceID(dataDir); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(&webdav.Handler{Prefix: "/dav", FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()})
	t.Cleanup(srv.Close)

	m := backup.NewManager(dataDir, uploadDir, "1.0.168", 1)
	SetBackupManager(m)
	SetRestoreController(restore.New(restore.Options{
		DataDir: dataDir, UploadDir: uploadDir, DBPath: filepath.Join(dataDir, "notepad.db"), AppVersion: "1.0.168",
	}))

	r := gin.New()
	auth := r.Group("/api", func(c *gin.Context) {
		c.Set("userID", int64(1))
		c.Set("username", "rootadmin")
		c.Next()
	})
	auth.GET("/backup/jobs/:id", GetBackupJob)
	auth.GET("/backup/restore/progress", RestoreProgress)
	auth.POST("/backup/targets", CreateBackupTarget)
	auth.POST("/backup/restore", RestoreFromBackup)

	enc, _ := m.EncryptSecret("pw")
	tid, err := model.CreateBackupTarget(model.BackupTarget{
		Name: "t", Type: "webdav", BaseURL: strings.TrimRight(srv.URL, "/") + "/dav/",
		Username: "u", SecretEncrypted: enc, RemotePath: "/bk/", Enabled: true,
		RetentionCount: 7, ScheduleInterval: "24h", AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID, bid, err := m.BeginJob(tid, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RunBackupJob(tid, "manual", jobID, bid); err != nil {
		t.Fatalf("seed backup failed: %v", err)
	}
	return r, bid
}

func pollJobStatus(t *testing.T, r *gin.Engine, jobID int64, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		w := doJSON(t, r, "GET", "/api/backup/jobs/"+i64toa(jobID), nil)
		_ = json.Unmarshal(w.Body.Bytes(), &last)
		if s, _ := last["status"].(string); s == want || s == "FAILED" || s == "INTERRUPTED" {
			return last
		}
		time.Sleep(120 * time.Millisecond)
	}
	return last
}

func noteTotal(t *testing.T) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM notes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRestoreEndpointFullOnlineRestore(t *testing.T) {
	r, bid := setupRestoreHandler(t)

	if _, err := database.DB.Exec(`INSERT INTO notes(user_id,title,content) VALUES(1,'added-after','v2')`); err != nil {
		t.Fatal(err)
	}
	if noteTotal(t) != 2 {
		t.Fatalf("pre-state want 2 notes, got %d", noteTotal(t))
	}

	w := doJSON(t, r, "POST", "/api/backup/restore", gin.H{
		"backup_id": bid, "password": "Adm1n-Passw0rd", "confirm": true,
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("restore 应 202，得 %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		RestoreID string `json:"restore_id"`
		JobID     int64  `json:"job_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.JobID == 0 {
		t.Fatalf("202 缺 job_id: %s", w.Body.String())
	}

	// 成功全量恢复会连恢复 job 行一起被覆盖（旧库被替换），故完成信号取内存进度，而非 DB job。
	// 判据用 controller **phase**（单调、可跨越快慢两种时序）：窗口的开与关都是瞬态，
	// 快 runner 上可能在两次 50ms 轮询之间开合完毕（v1.0.175 CI run 35311502214 实证），
	// 慢 runner 上则可能迟迟不开（前一轮 run 35310833939 实证）。phase 终态才可同时满足两种时序。
	var prog map[string]any
	deadline := time.Now().Add(20 * time.Second)
	for {
		gw := doJSON(t, r, "GET", "/api/backup/restore/progress", nil)
		prog = map[string]any{}
		_ = json.Unmarshal(gw.Body.Bytes(), &prog)
		switch prog["phase"] {
		case "FINALIZING", "ROLLING_BACK":
			goto settled
		}
		if time.Now().After(deadline) {
			t.Fatalf("恢复 20s 未达终态，progress=%+v", prog)
		}
		time.Sleep(60 * time.Millisecond)
	}
settled:
	// ROLLING_BACK 视为业务失败：连同 error 字段与 job 状态一起打出，随后仍走断言失败。
	if prog["phase"] != "FINALIZING" {
		jw := doJSON(t, r, "GET", "/api/backup/jobs/"+i64toa(resp.JobID), nil)
		t.Fatalf("恢复终态应 FINALIZING，得 %+v；job=%s", prog, jw.Body.String())
	}
	if restoring, _ := prog["restoring"].(bool); restoring {
		t.Fatalf("FINALIZING 时安全窗口必须已关闭（顺序性断言，§87 红线 15）：%+v", prog)
	}
	// FINALIZING 后 manager 尚有一个 <return 窗口>：等 running=false，确保异步 goroutine
	// 在测试 cleanup 关库前彻底结束（防跨用例污染）。
	settle := time.Now().Add(5 * time.Second)
	for {
		if running, _ := prog["running"].(bool); !running {
			break
		}
		if time.Now().After(settle) {
			t.Fatalf("恢复收尾超时，progress=%+v", prog)
		}
		time.Sleep(60 * time.Millisecond)
		gw := doJSON(t, r, "GET", "/api/backup/restore/progress", nil)
		_ = json.Unmarshal(gw.Body.Bytes(), &prog)
	}
	if noteTotal(t) != 1 {
		t.Fatalf("恢复后应回到 1 条笔记，得 %d", noteTotal(t))
	}
}

func TestRestoreEndpointGuards(t *testing.T) {
	r, bid := setupRestoreHandler(t)

	w := doJSON(t, r, "POST", "/api/backup/restore", gin.H{"backup_id": bid, "password": "Adm1n-Passw0rd"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无 confirm 应 400，得 %d", w.Code)
	}
	w = doJSON(t, r, "POST", "/api/backup/restore", gin.H{"backup_id": bid, "password": "wrong", "confirm": true})
	if w.Code != http.StatusForbidden {
		t.Fatalf("错口令应 403，得 %d", w.Code)
	}
	w = doJSON(t, r, "POST", "/api/backup/restore", gin.H{"backup_id": "no-such-id", "password": "Adm1n-Passw0rd", "confirm": true})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无副本应 400，得 %d body=%s", w.Code, w.Body.String())
	}
}
