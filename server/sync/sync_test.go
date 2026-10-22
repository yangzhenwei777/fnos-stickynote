package sync

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"stickynote/database"
)

// sync_test.go —— P0-D 服务端契约测试：§8/§24/§32~§44 + ADR-002 + ADR-004 R2 逐条钉死。

func setupSyncDB(t *testing.T) *gin.Engine {
	t.Helper()
	if err := database.Init(filepath.Join(t.TempDir(), "srv.db"), "99.0.0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	if _, err := database.DB.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','user'),(2,'u2','x','user')`); err != nil {
		t.Fatal(err)
	}
	Configure(t.TempDir())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 身份切换：**头 `x-test-user` 或 query `?as=` 任一**（批次 9 红线 4 补测时坐实：
	// 此前只写 `c.Get("x-test-user")`，而 gin.Context.Get 读的是 c.Keys 上下文键、
	// **不是 HTTP 头** → 该头永远取不到值，多用户用例会静默以 u1 跑出假绿）。
	auth := r.Group("/api", func(c *gin.Context) {
		uid := int64(1)
		raw := c.GetHeader("x-test-user")
		if raw == "" {
			raw = c.Query("as")
		}
		if raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				uid = int64(n)
			}
		}
		c.Set("userID", uid)
		c.Next()
	})
	auth.POST("/sync/push", Push)
	auth.GET("/sync/pull", Pull)
	auth.GET("/sync/state", State)
	return r
}

type noteP map[string]any

func createOp(opID, syncID, title string) PushOp {
	base := int64(0)
	return PushOp{OpID: opID, EntityType: "NOTE", EntitySyncID: syncID, Operation: "CREATE", BaseVersion: &base,
		Payload: mustJSON(noteP{"sync_id": syncID, "title": title, "content": "<p>c</p>", "tags": "t"})}
}

func updateOp(opID, syncID, title string, base int64) PushOp {
	b := base
	return PushOp{OpID: opID, EntityType: "NOTE", EntitySyncID: syncID, Operation: "UPDATE", BaseVersion: &b,
		Payload: mustJSON(noteP{"sync_id": syncID, "title": title, "content": "<p>u</p>", "tags": ""})}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func doPush(t *testing.T, r *gin.Engine, device string, ops ...PushOp) []OpResult {
	t.Helper()
	body, _ := json.Marshal(PushRequest{DeviceID: device, Ops: ops})
	req := httptest.NewRequest("POST", "/api/sync/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("push status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Results []OpResult `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Results
}

func doPushRaw(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/sync/push", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func doPull(t *testing.T, r *gin.Engine, cursor, watermark int64) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/sync/pull?cursor=0&watermark=0", nil)
	req.URL.RawQuery = "cursor=" + itoa(cursor) + "&watermark=" + itoa(watermark)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("pull status=%d", w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestPushCreatePullRoundTrip(t *testing.T) {
	r := setupSyncDB(t)
	res := doPush(t, r, "devA", createOp("op-1", "sync-a1", "首条"))
	if len(res) != 1 || res[0].Code != CodeAcked || res[0].ServerVersion != 1 || res[0].ChangeSeq != 1 {
		t.Fatalf("create 结果不符: %+v", res)
	}
	pull := doPull(t, r, 0, 0)
	changes := pull["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("pull 应得 1 条: %+v", pull)
	}
	ch := changes[0].(map[string]any)
	if ch["operation"] != "CREATE" || ch["entity_sync_id"] != "sync-a1" {
		t.Fatalf("事件形状不符: %v", ch)
	}
	pl := ch["payload"].(map[string]any)
	if pl["title"] != "首条" || pl["version"].(float64) != 1 {
		t.Fatalf("canonical payload 权威字段不符: %v", pl)
	}
	// 派生数据不进 payload（§30）。
	for _, banned := range []string{"content_text", "highlight", "trash_upload_bytes"} {
		if _, ok := pl[banned]; ok {
			t.Fatalf("派生字段 %s 不应入 payload", banned)
		}
	}
}

func TestOpIdempotentReplayAndReuse(t *testing.T) {
	r := setupSyncDB(t)
	op := createOp("op-re", "sync-re", "幂等")
	first := doPush(t, r, "d", op)
	if first[0].Code != CodeAcked {
		t.Fatalf("first=%+v", first)
	}
	replay := doPush(t, r, "d", op) // 同 op_id 同 payload
	if replay[0].Code != CodeIdempotentReplay {
		t.Fatalf("重放应 IDEMPOTENT_REPLAY，得 %+v", replay)
	}
	var n int
	database.DB.QueryRow(`SELECT COUNT(*) FROM sync_changes`).Scan(&n)
	if n != 1 {
		t.Fatalf("重放不得产生第二条事件（红线 3），得 %d", n)
	}
	tampered := op
	tampered.Payload = mustJSON(noteP{"sync_id": "sync-re", "title": "篡改", "content": ""})
	bad := doPush(t, r, "d", tampered)
	if bad[0].Code != CodeOpIDReuseError {
		t.Fatalf("同 op_id 异 payload 应 OP_ID_REUSE_ERROR，得 %+v", bad)
	}
}

func TestBatchDuplicateEntityRejected(t *testing.T) {
	r := setupSyncDB(t)
	w := doPushRaw(t, r, `{"device_id":"d","ops":[
		{"op_id":"a","entity_type":"NOTE","entity_sync_id":"s1","operation":"CREATE","base_version":0,"payload":{"sync_id":"s1","title":"x"}},
		{"op_id":"b","entity_type":"NOTE","entity_sync_id":"s1","operation":"DELETE","base_version":1}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ADR-002 一批一实体：应 400，得 %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != CodeBatchProtocol {
		t.Fatalf("错误码应为 BATCH_PROTOCOL_ERROR: %v", body)
	}
	var n int
	database.DB.QueryRow(`SELECT COUNT(*) FROM sync_changes`).Scan(&n)
	if n != 0 {
		t.Fatalf("整批拒绝必须零执行，事件数=%d", n)
	}
}

func TestCasConflictAndEntityGone(t *testing.T) {
	r := setupSyncDB(t)
	doPush(t, r, "d", createOp("c1", "s-cas", "初始"))
	ok := doPush(t, r, "d", updateOp("u1", "s-cas", "改一", 1))
	if ok[0].Code != CodeAcked || ok[0].ServerVersion != 2 {
		t.Fatalf("合法 CAS 应 ACK v2: %+v", ok)
	}
	stale := doPush(t, r, "d", updateOp("u2", "s-cas", "旧基", 1))
	if stale[0].Code != CodeConflict || stale[0].RemoteVersion != 2 {
		t.Fatalf("过期 base 应 CONFLICT+remote_version=2，得 %+v", stale)
	}
	// ADR-004 R2：物理删行后 push → ENTITY_GONE（区别于普通 409）。
	if _, err := database.DB.Exec(`DELETE FROM notes WHERE sync_id='s-cas'`); err != nil {
		t.Fatal(err)
	}
	gone := doPush(t, r, "d", updateOp("u3", "s-cas", "幽灵", 2))
	if gone[0].Code != CodeEntityGone {
		t.Fatalf("物理缺行应 ENTITY_GONE，得 %+v", gone)
	}
}

func TestCreateSecondLayerIdempotency(t *testing.T) {
	r := setupSyncDB(t)
	doPush(t, r, "d", createOp("op-x", "s-dup", "原"))
	// 新 op_id 但 sync_id 已存在且属主一致 → 视为已创建（§44），不得双建。
	res := doPush(t, r, "d", createOp("op-y", "s-dup", "重命名尝试"))
	if res[0].Code != CodeIdempotentReplay {
		t.Fatalf("§44 第二层幂等失败: %+v", res)
	}
	var n int
	database.DB.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id='s-dup'`).Scan(&n)
	if n != 1 {
		t.Fatalf("禁止第二次创建，得 %d 行", n)
	}
}

func TestWatermarkOrderingAndPaging(t *testing.T) {
	r := setupSyncDB(t)
	for i := 0; i < 5; i++ {
		doPush(t, r, "d", createOp("w-"+itoa(int64(i)), "s-w"+itoa(int64(i)), "t"))
	}
	// 固定 watermark=3：4、5 号变更本轮不可见（§32/§39）。
	pull := doPull(t, r, 0, 3)
	changes := pull["changes"].([]any)
	if len(changes) != 3 {
		t.Fatalf("watermark 钳制失败: %d 条", len(changes))
	}
	last := int64(0)
	for _, c := range changes {
		seq := int64(c.(map[string]any)["change_seq"].(float64))
		if seq <= last {
			t.Fatal("必须 change_seq ASC 定序（§40）")
		}
		last = seq
	}
	if pull["has_more"] != false || pull["next_cursor"].(float64) != 3 {
		t.Fatalf("游标语义不符: %v", pull)
	}
	// 分页 limit=2：两页拼合无重叠无遗漏。
	p1 := doPull(t, r, 0, 5)
	_ = p1
	req := httptest.NewRequest("GET", "/api/sync/pull?cursor=0&watermark=5&limit=2", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var page map[string]any
	json.Unmarshal(w.Body.Bytes(), &page)
	if len(page["changes"].([]any)) != 2 || page["has_more"] != true {
		t.Fatalf("分页 limit/has_more 不符: %v", page)
	}
}

func TestStatePerUserMinSeqAndEpoch(t *testing.T) {
	r := setupSyncDB(t)
	req := httptest.NewRequest("GET", "/api/sync/state", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var st map[string]any
	json.Unmarshal(w.Body.Bytes(), &st)
	if st["server_epoch"].(float64) != 1 || st["protocol_version"].(float64) != 1 ||
		st["current_change_seq"].(float64) != 0 || st["min_available_change_seq"].(float64) != 1 {
		t.Fatalf("空库 state 不符: %v", st)
	}
	doPush(t, r, "d", createOp("m1", "s-m1", "x"))
	// 用户 2 视角：自己的 min_available = current+1（§7 按用户，非全局 MIN）。
	r2 := gin.New()
	g := r2.Group("/api", func(c *gin.Context) { c.Set("userID", int64(2)); c.Next() })
	g.GET("/sync/state", State)
	req2 := httptest.NewRequest("GET", "/api/sync/state", nil)
	w2 := httptest.NewRecorder()
	r2.ServeHTTP(w2, req2)
	var st2 map[string]any
	json.Unmarshal(w2.Body.Bytes(), &st2)
	if st2["min_available_change_seq"].(float64) != 2 {
		t.Fatalf("§7 per-user min_available 不符: %v", st2)
	}
}
