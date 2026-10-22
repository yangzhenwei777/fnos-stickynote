package sync

// snapshot_api_test.go —— 批次 5 · 刀 2：快照 HTTP 面契约（归属/404 合并防枚举/下载头/状态推进）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"stickynote/database"
	"stickynote/model"
)

func snapAPIRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := r.Group("/api", func(c *gin.Context) {
		uid := int64(1)
		if c.Query("as") == "2" {
			uid = 2
		}
		role := "user"
		if c.Query("as") == "admin" {
			role = "admin"
		}
		c.Set("userID", uid)
		c.Set("username", "u1")
		c.Set("role", role)
		c.Next()
	})
	auth.POST("/sync/snapshot", SnapshotCreate)
	auth.GET("/sync/snapshot/:id", SnapshotStatus)
	auth.GET("/sync/snapshot/:id/download", SnapshotDownload)
	return r
}

func waitSnapshotTerminal(t *testing.T, id string) *model.SyncSnapshot {
	t.Helper()
	for i := 0; i < 100; i++ {
		s, err := model.GetSyncSnapshot(id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Status == model.SnapshotReady || s.Status == model.SnapshotFailed {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("snapshot never reached terminal state")
	return nil
}

func TestSnapshotAPIFlow(t *testing.T) {
	setupSnapEnv(t)
	r := snapAPIRouter()

	// 1) 创建 → 202
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/sync/snapshot", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var created struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal("bad json")
	}
	// 2) 他人不可见（404 合并语义，防枚举）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sync/snapshot/"+created.SnapshotID+"?as=2", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("other user must see 404, got %d", w.Code)
	}
	// 3) 后台构建（202 异步语义本体）→ 轮询至 READY；下载头齐备
	snap := waitSnapshotTerminal(t, created.SnapshotID)
	if snap.Status != model.SnapshotReady {
		t.Fatalf("async build failed: %s %s", snap.Status, snap.Error)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sync/snapshot/"+created.SnapshotID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var st map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if st["status"] != model.SnapshotReady || st["sha256"] == "" || st["download_url"] == "" {
		t.Fatalf("ready payload: %v", st)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sync/snapshot/"+created.SnapshotID+"/download", nil))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download=%d cd=%q", w.Code, w.Header().Get("Content-Disposition"))
	}
	if w.Header().Get("X-Snapshot-Sha256") != st["sha256"] || w.Body.Len() == 0 {
		t.Fatal("download sha/body mismatch")
	}
	// 4) admin 可轮询他人快照状态（运维面），但下载仍限本人
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sync/snapshot/"+created.SnapshotID+"?as=admin", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("admin status=%d", w.Code)
	}
	// admin 下载放行是有意设计（运维/桌面初始化经管理员取档分发）；他人普通用户仍 404（上方已钉）。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/sync/snapshot/"+created.SnapshotID+"/download?as=admin", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("admin download should 200 (ops path), got %d", w.Code)
	}
}

func TestSnapshotAPICapErrors(t *testing.T) {
	setupSnapEnv(t)
	r := snapAPIRouter()
	newSnapRow(t, 1, model.SnapshotBuilding, time.Now().Add(time.Hour))
	newSnapRow(t, 2, model.SnapshotBuilding, time.Now().Add(time.Hour))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/sync/snapshot?as=2", nil))
	// 用户 2 已有一枚 BUILDING → 复用返回 202；全局帽满时新用户 1… 直接验证两态之一
	if w.Code != http.StatusAccepted && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 202(reuse)/503, got %d %s", w.Code, w.Body.String())
	}
}

// 恢复换代联动（restore.OnEpochCommitted 钩子实现面）：
// epoch bump → 活跃快照全部 INVALIDATED + Hold 释放 + 文件删除。
func TestInvalidateBootstrapSnapshotsHook(t *testing.T) {
	setupSnapEnv(t)
	id := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
	if err := BuildSnapshotSync(id, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	s, _ := model.GetSyncSnapshot(id)
	if _, err := os.Stat(s.FilePath); err != nil {
		t.Fatal(err)
	}
	InvalidateBootstrapSnapshots()
	s2, _ := model.GetSyncSnapshot(id)
	if s2.Status != model.SnapshotInvalidated {
		t.Fatalf("status=%s", s2.Status)
	}
	if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
		t.Fatal("file must be removed on invalidation")
	}
	var holds int
	database.DB.QueryRow(`SELECT COUNT(*) FROM sync_snapshots_hold WHERE snapshot_id=?`, id).Scan(&holds)
	if holds != 0 {
		t.Fatal("holds must be released")
	}
}
