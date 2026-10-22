package synclocal

// payload_test.go —— §32 P1-b 桌面端集成（2026-10-15）：
// 真 push 大笔记 → 服务端 blob 分流 → 桌面 pull（applyChange blob_ref 分支）下载应用；
// 载荷缺失 → 退避重试 → payloadMissingEscapeLimit 轮后逃逸跳过（防 pull 永久卡死）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"stickynote/changelog"
	"stickynote/database"
	syncsrv "stickynote/sync"
	"stickynote/syncspec"
)

// newPayloadServer 最小真服务端工装：push/pull/payload 三端点 + blob 目录注入，
// 返回 server 与 blob 目录（缺失逃逸用例需要删文件）。
func newPayloadServer(t *testing.T) (*httptest.Server, string) {
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
	pdir := filepath.Join(srvDir, "sync-payloads")
	changelog.ConfigurePayloads(pdir)
	t.Cleanup(func() { changelog.ConfigurePayloads("") })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := r.Group("/api", func(c *gin.Context) { c.Set("userID", int64(1)); c.Next() })
	auth.POST("/sync/push", syncsrv.Push)
	auth.GET("/sync/pull", syncsrv.Pull)
	auth.GET("/sync/state", syncsrv.State)
	auth.GET("/sync/payload/:sha256", syncsrv.PayloadBlob)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, pdir
}

// mustJSON 测试载荷序列化（失败即 Fatal 语义由调用方断言兜底）。
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// serverPushBigNote 经真 push 处理器造一条大笔记变更（服务端权威重建 payload → blob 分流）。
func serverPushBigNote(t *testing.T, ts *httptest.Server, syncID, content string) {
	t.Helper()
	base := int64(0)
	op := syncspec.PushOp{OpID: "op-" + syncID, EntityType: "NOTE", EntitySyncID: syncID,
		Operation: "CREATE", BaseVersion: &base,
		Payload: mustJSON(map[string]any{"sync_id": syncID, "title": "t-" + syncID, "content": content})}
	body, _ := json.Marshal(syncspec.PushRequest{DeviceID: "seed", Ops: []syncspec.PushOp{op}})
	req := httptest.NewRequest("POST", "/api/sync/push", bytes.NewReader(body))
	req.Header.Set("x-test-user", "1")
	rec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ACKED") {
		t.Fatalf("服务端造数失败: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWorkerPullBlobRefAppliesLocally(t *testing.T) {
	ts, _ := newPayloadServer(t)
	db := newStore(t)
	serverPushBigNote(t, ts, "sync-big-1", strings.Repeat("x", changelog.InlinePayloadLimit+1024))

	// 桌面 pull → blob 下载（sha 校验）→ 本地落地。
	w := NewWorker(db, NewClient(ts.URL, "tok", "dev-a"))
	if _, err := w.RunOnce(nilCtx()); err != nil {
		t.Fatalf("首拉应成功（含 blob 下载）: %v", err)
	}
	var title, content string
	if err := db.QueryRow(`SELECT title, content FROM notes WHERE sync_id = 'sync-big-1'`).Scan(&title, &content); err != nil {
		t.Fatalf("blob_ref 变更应落地本地: %v", err)
	}
	if title != "t-sync-big-1" || len(content) != changelog.InlinePayloadLimit+1024 {
		t.Fatalf("本地内容与载荷不符: title=%q len=%d", title, len(content))
	}
	// cursor 推进到服务端水位（防重复拉取）；二轮幂等零新事件。
	var cur, maxSeq int64
	_ = db.QueryRow(`SELECT value FROM sync_state WHERE key='cursor'`).Scan(&cur)
	_ = database.DB.QueryRow(`SELECT MAX(change_seq) FROM sync_changes`).Scan(&maxSeq)
	if cur != maxSeq {
		t.Fatalf("cursor 应推到服务端水位: cursor=%d max=%d", cur, maxSeq)
	}
	if _, err := w.RunOnce(nilCtx()); err != nil {
		t.Fatalf("二轮应成功: %v", err)
	}
}

func TestWorkerPullBlobRefEscapesAfterLimit(t *testing.T) {
	ts, pdir := newPayloadServer(t)
	serverPushBigNote(t, ts, "sync-gone-1", strings.Repeat("y", changelog.InlinePayloadLimit+1024))
	var ref string
	if err := database.DB.QueryRow(`SELECT payload_ref FROM sync_changes WHERE payload_kind='blob_ref' LIMIT 1`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	// 行在、载荷缺（模拟极端故障：服务端文件丢失）。
	if err := os.Remove(filepath.Join(pdir, ref)); err != nil {
		t.Fatalf("删 blob 文件失败: %v", err)
	}

	db := newStore(t)
	w := NewWorker(db, NewClient(ts.URL, "tok", "dev-a"))
	// 前 N-1 轮：失败传播（本轮 pull 停在缺失处，cursor 不推）。
	for i := 1; i < payloadMissingEscapeLimit; i++ {
		if _, err := w.RunOnce(nilCtx()); !errors.Is(err, ErrPayloadGone) {
			t.Fatalf("第 %d 轮应因载荷缺失失败，实得: %v", i, err)
		}
	}
	// 第 N 轮：逃逸——跳过该 change 推进 cursor，pull 收敛。
	if _, err := w.RunOnce(nilCtx()); err != nil {
		t.Fatalf("第 %d 轮应逃逸成功: %v", payloadMissingEscapeLimit, err)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id='sync-gone-1'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatal("逃逸跳过后本地不应有该笔记（留待全量重建自愈）")
	}
	var cur, maxSeq int64
	_ = db.QueryRow(`SELECT value FROM sync_state WHERE key='cursor'`).Scan(&cur)
	_ = database.DB.QueryRow(`SELECT MAX(change_seq) FROM sync_changes`).Scan(&maxSeq)
	if cur != maxSeq {
		t.Fatalf("逃逸后 cursor 应推进: cursor=%d max=%d", cur, maxSeq)
	}
}
