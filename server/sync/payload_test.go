package sync

// payload_test.go —— §32~36 P1-b 集成面（2026-10-15）：
// 大笔记 push → blob 分流落盘 → pull 返回 blob_ref 形状 → 端点下载/授权 → GC 差集。
// 复用真 Push/Pull/PayloadBlob 处理器 + httptest（与 sync_test 同工装风格）。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"stickynote/changelog"
	"stickynote/database"
)

func setupPayloadEnv(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	if err := database.Init(filepath.Join(t.TempDir(), "srv.db"), "99.0.0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	if _, err := database.DB.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','user'),(2,'u2','x','user')`); err != nil {
		t.Fatal(err)
	}
	srvDir := t.TempDir()
	Configure(srvDir)
	pdir := filepath.Join(srvDir, "sync-payloads")
	changelog.ConfigurePayloads(pdir)
	t.Cleanup(func() { changelog.ConfigurePayloads("") }) // 包级状态复位

	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := r.Group("/api", func(c *gin.Context) {
		uid := int64(1)
		if raw := c.GetHeader("x-test-user"); raw == "2" {
			uid = 2
		}
		c.Set("userID", uid)
		c.Next()
	})
	auth.POST("/sync/push", Push)
	auth.GET("/sync/pull", Pull)
	auth.GET("/sync/payload/:sha256", PayloadBlob)
	return r, pdir
}

func pushBigNote(t *testing.T, r *gin.Engine, opID, syncID string, contentLen int) {
	t.Helper()
	base := int64(0)
	op := PushOp{OpID: opID, EntityType: "NOTE", EntitySyncID: syncID, Operation: "CREATE", BaseVersion: &base,
		Payload: mustJSON(noteP{"sync_id": syncID, "title": "big", "content": strings.Repeat("x", contentLen)})}
	body, _ := json.Marshal(PushRequest{DeviceID: "dev-t", Ops: []PushOp{op}})
	req := httptest.NewRequest("POST", "/api/sync/push", bytes.NewReader(body))
	req.Header.Set("x-test-user", "1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("push 大笔记失败: http %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Results []OpResult `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Code != CodeAcked {
		t.Fatalf("push 应 ACK: %+v", resp.Results)
	}
}

func TestPushBigNotePullBlobRefAndDownload(t *testing.T) {
	r, _ := setupPayloadEnv(t)

	// >4MB 内容 → blob 分流。
	pushBigNote(t, r, "op-big-1", "sync-big-1", changelog.InlinePayloadLimit+1024)

	// pull 形状：blob_ref 行带 ref/size、payload 为 null。
	req := httptest.NewRequest("GET", "/api/sync/pull?cursor=0&watermark=0", nil)
	req.Header.Set("x-test-user", "1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("pull 失败: %d", w.Code)
	}
	var pullResp struct {
		Changes []map[string]any `json:"changes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pullResp); err != nil {
		t.Fatal(err)
	}
	if len(pullResp.Changes) != 1 {
		t.Fatalf("应恰 1 条 change: %d", len(pullResp.Changes))
	}
	ch := pullResp.Changes[0]
	if ch["payload_kind"] != "blob_ref" {
		t.Fatalf("应为 blob_ref: %v", ch["payload_kind"])
	}
	ref, _ := ch["payload_ref"].(string)
	if !changelog.ValidPayloadRef(ref) {
		t.Fatalf("payload_ref 形状非法: %v", ch["payload_ref"])
	}
	if size, _ := ch["payload_size"].(float64); size <= 0 {
		t.Fatalf("payload_size 应为正: %v", ch["payload_size"])
	}
	if payload, ok := ch["payload"].(map[string]any); ok && payload != nil {
		t.Fatalf("blob_ref 行 payload 应为 null: %v", ch["payload"])
	}

	// §34 下载：属主 200 + 内容 sha256 == ref。
	req2 := httptest.NewRequest("GET", "/api/sync/payload/"+ref, nil)
	req2.Header.Set("x-test-user", "1")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("属主下载应 200: %d", w2.Code)
	}
	h := sha256.New()
	if _, err := io.Copy(h, w2.Body); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != ref {
		t.Fatalf("下载内容 sha256 与 ref 不符: %s vs %s", got, ref)
	}

	// §35 授权：跨用户 404 / 未引用 hash 404 / 非 hex 404。
	req3 := httptest.NewRequest("GET", "/api/sync/payload/"+ref, nil)
	req3.Header.Set("x-test-user", "2")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != 404 {
		t.Fatalf("§35 跨用户必须 404: %d", w3.Code)
	}
	req4 := httptest.NewRequest("GET", "/api/sync/payload/"+strings.Repeat("cd", 32), nil)
	req4.Header.Set("x-test-user", "1")
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	if w4.Code != 404 {
		t.Fatalf("未引用 hash 必须 404: %d", w4.Code)
	}
	req5 := httptest.NewRequest("GET", "/api/sync/payload/not-a-hash", nil)
	req5.Header.Set("x-test-user", "1")
	w5 := httptest.NewRecorder()
	r.ServeHTTP(w5, req5)
	if w5.Code != 404 {
		t.Fatalf("非 hex 形状必须 404: %d", w5.Code)
	}
	if w5.Header().Get("X-Payload-Sha256") != "" && w4.Code == 404 {
		t.Fatal("404 响应不应携带载荷头")
	}
}

func TestSmallNoteStaysInline(t *testing.T) {
	r, _ := setupPayloadEnv(t)
	pushBigNote(t, r, "op-small-1", "sync-small-1", 1024)
	req := httptest.NewRequest("GET", "/api/sync/pull?cursor=0&watermark=0", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var pullResp struct {
		Changes []map[string]any `json:"changes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &pullResp)
	if len(pullResp.Changes) != 1 || pullResp.Changes[0]["payload_kind"] != "json" {
		t.Fatalf("小载荷应保持内联 json: %v", pullResp.Changes)
	}
	if _, has := pullResp.Changes[0]["payload_ref"]; has {
		if v, _ := pullResp.Changes[0]["payload_ref"].(string); v != "" {
			t.Fatalf("内联行不应携带非空 payload_ref: %v", pullResp.Changes[0]["payload_ref"])
		}
	}
}

func TestGCOrphanPayloads(t *testing.T) {
	r, pdir := setupPayloadEnv(t)
	pushBigNote(t, r, "op-gc-1", "sync-gc-1", changelog.InlinePayloadLimit+1024)

	// 取真 ref（有引用，必须保留）。
	var ref string
	if err := database.DB.QueryRow(`SELECT payload_ref FROM sync_changes WHERE payload_kind='blob_ref' LIMIT 1`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pdir, ref)); err != nil {
		t.Fatalf("blob 文件应在位: %v", err)
	}

	// 造孤儿（hex64 形状但无 DB 引用）+ 临时件 + 非 blob 形状件。
	orphan := strings.Repeat("99", 32)
	if err := os.WriteFile(filepath.Join(pdir, orphan), []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, ".payload-tmp"), []byte("tmp"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "README.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	removed, err := GCOrphanPayloads()
	if err != nil {
		t.Fatalf("GC 失败: %v", err)
	}
	if removed != 1 {
		t.Fatalf("应恰好删除 1 个孤儿，实得 %d", removed)
	}
	if _, err := os.Stat(filepath.Join(pdir, ref)); err != nil {
		t.Fatal("有引用 blob 不得被 GC 删除（fail-closed 破坏）")
	}
	if _, err := os.Stat(filepath.Join(pdir, ".payload-tmp")); err != nil {
		t.Fatal("临时件不归 GC 管")
	}
	if _, err := os.Stat(filepath.Join(pdir, "README.txt")); err != nil {
		t.Fatal("非 blob 形状件不归 GC 管")
	}
	if _, err := os.Stat(filepath.Join(pdir, orphan)); !os.IsNotExist(err) {
		t.Fatal("无引用孤儿应被删除")
	}
}
