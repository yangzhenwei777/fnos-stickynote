package sync

// redline_gaps_test.go —— 批次 9 · C1 红线缺口补测（第一批：服务端面）。
//
// 覆盖《docs/红线挂测清点.md》§八 中标记为「部分挂测」的两条：
//   红线 4  op_id 跨请求复用 → **缺：跨用户复用同 op_id 不串档（user_id 双键隔离面）**
//   红线 22 Sync Upload 绕过校验 → **缺：恶意文件名/坏扩展名、超大、错 MIME 三类反向用例**
//
// 判据口径同台账 §一：必须给出用例名 + 文件:行号，且**反向用例必须真跑真断言**，
// 不接受「防守代码看起来在」这种目视论证（v21.91 门禁空转教训）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"stickynote/database"
)

// ---------------------------------------------------------------- 红线 4

// TestOpIDCrossUserIsolation —— 红线 4 缺口面：userA 与 userB 使用**同一个 op_id**
// 各推自己的 CREATE，两者都必须成功且互不串档：
//  1. u2 复用 u1 的 op_id 不应被判 OP_ID_REUSE_ERROR（幂等键须含 user_id）；
//  2. 各自 pull 只看得到自己的笔记（数据面零串档）；
//  3. u1 重放同 op_id 仍走幂等 ACK（不产生第二条事件）。
//
// 为什么必须有这条：幂等表若只按 op_id 建唯一键，跨用户复用会互相顶掉；
// 单用户用例（TestOpReuseSameIDDifferentPayload）永远测不到这个面。
func TestOpIDCrossUserIsolation(t *testing.T) {
	r := setupSyncMulti(t)

	res := pushAs(t, r, 1, createOp("op-1", "sync-1", "u1-note"))
	if len(res) != 1 || res[0].Error != "" {
		t.Fatalf("u1 push 应成功，得到 %+v", res)
	}

	res = pushAs(t, r, 2, createOp("op-1", "sync-2", "u2-note"))
	if len(res) != 1 {
		t.Fatalf("u2 复用同 op_id 应得 1 条结果，得到 %d: %+v", len(res), res)
	}
	if res[0].Error != "" {
		t.Fatalf("红线4：u2 复用 u1 的 op_id 不应报错（幂等键未按 user 隔离），得到 %q", res[0].Error)
	}

	for _, tc := range []struct {
		uid       int64
		wantTitle string
		notWant   string
	}{{1, "u1-note", "u2-note"}, {2, "u2-note", "u1-note"}} {
		got := pullTitlesAs(t, r, tc.uid)
		if !strings.Contains(got, tc.wantTitle) {
			t.Fatalf("uid=%d 应看到自己的 %q，实际 %q", tc.uid, tc.wantTitle, got)
		}
		if strings.Contains(got, tc.notWant) {
			t.Fatalf("红线4：uid=%d 看到了别人的 %q（串档）——幂等/事件面未按 user_id 隔离", tc.uid, tc.notWant)
		}
	}

	res = pushAs(t, r, 1, createOp("op-1", "sync-1", "u1-note"))
	if len(res) != 1 || res[0].Error != "" {
		t.Fatalf("u1 重放同 op_id 应幂等成功，得到 %+v", res)
	}
	if got := pullTitlesAs(t, r, 1); strings.Count(got, "u1-note") != 1 {
		t.Fatalf("u1 重放产生了重复实体：%q", got)
	}
}

// ---------------------------------------------------------------- 红线 22

// TestSyncUploadRejectsMaliciousFilename —— 红线 22 反向用例①：恶意文件名
// （路径穿越 / 目录分隔符 / 非 uuid.ext / 空名）必须 400，且**不得落盘**。
func TestSyncUploadRejectsMaliciousFilename(t *testing.T) {
	r, dir := uploadEngine(t)
	for _, name := range []string{
		"../../etc/passwd.png",
		`..\..\windows\system32\evil.png`,
		"sub/dir/evil.png",
		"notauuid.png",
		"",
	} {
		w := doSyncUpload(t, r, name, syncPNG("x", 1), "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("红线22：恶意文件名 %q 应 400，得到 %d: %s", name, w.Code, w.Body.String())
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("红线22：恶意文件名被拒后目录仍残留 %d 个文件", len(entries))
	}
}

// TestSyncUploadRejectsBadExtension —— 红线 22 反向用例②：白名单外扩展名必须 400。
func TestSyncUploadRejectsBadExtension(t *testing.T) {
	r, dir := uploadEngine(t)
	for _, name := range []string{
		"11111111-1111-1111-1111-111111111111.exe",
		"11111111-1111-1111-1111-111111111111.svg",
		"11111111-1111-1111-1111-111111111111.html",
		"11111111-1111-1111-1111-111111111111.pdf",
	} {
		w := doSyncUpload(t, r, name, syncPNG("x", 2), "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("红线22：坏扩展名 %q 应 400，得到 %d: %s", name, w.Code, w.Body.String())
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("红线22：坏扩展名被拒后目录仍残留 %d 个文件", len(entries))
	}
}

// TestSyncUploadRejectsOversize —— 红线 22 反向用例③：超大文件。
// 走 UPLOAD_MAX_MB 配置面（生产可调），压到 1MB 后送 2MB → 400 且不落盘。
func TestSyncUploadRejectsOversize(t *testing.T) {
	r, dir := uploadEngine(t)
	t.Setenv("UPLOAD_MAX_MB", "1")
	big := bytes.Repeat([]byte{0x41}, 2*1024*1024)
	w := doSyncUpload(t, r, "22222222-2222-2222-2222-222222222222.png", big, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("红线22：2MB > UPLOAD_MAX_MB=1 应 400，得到 %d: %s", w.Code, w.Body.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("红线22：超大文件被拒后目录仍残留 %d 个文件", len(entries))
	}
}

// TestSyncUploadRejectsMIMEMismatch —— 红线 22 反向用例④：错 MIME（magic 嗅探面）。
// 文件名 .png 但内容是纯文本 → 400；并附**真 PNG 成功**对照，防「假绿」。
func TestSyncUploadRejectsMIMEMismatch(t *testing.T) {
	r, dir := uploadEngine(t)
	fakePNG := []byte("<?php echo 'not-an-image'; ?> plain text payload")
	w := doSyncUpload(t, r, "33333333-3333-3333-3333-333333333333.png", fakePNG, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("红线22：错 MIME 应 400，得到 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "内容与图片格式不符") {
		t.Fatalf("红线22：错 MIME 应由内容嗅探拦下，实际 %s", w.Body.String())
	}
	w = doSyncUpload(t, r, "44444444-4444-4444-4444-444444444444.png", syncPNG("x", 9), "")
	if w.Code != http.StatusOK {
		t.Fatalf("对照：真 PNG 应 200，得到 %d: %s", w.Code, w.Body.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("对照：仅真 PNG 落盘，目录文件数 = %d", len(entries))
	}
}

// ---------------------------------------------------------------- helpers

// setupSyncMulti —— 自建多用户工装：身份经 **URL query** 传递。
// 为什么不用既有setupSyncDB 的 `x-test-user` 头：gin.Context.Get() 读的是 c.Keys 上下文键，
// **不是 HTTP 头**，那条路径实际永远取不到值 → 头号身份（恒为 u1）。不改既有工装以免影响存量用例。
func setupSyncMulti(t *testing.T) *gin.Engine {
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
	auth := r.Group("/api", func(c *gin.Context) {
		uid := int64(1)
		if v := c.Query("as"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			uid = int64(n)
		}
		c.Set("userID", uid)
		c.Next()
	})
	auth.POST("/sync/push", Push)
	auth.GET("/sync/pull", Pull)
	auth.GET("/sync/state", State)
	return r
}

// pushAs 以指定 uid 推 ops。
func pushAs(t *testing.T, r *gin.Engine, uid int64, ops ...PushOp) []OpResult {
	t.Helper()
	body, _ := json.Marshal(PushRequest{DeviceID: "dev-" + strconv.FormatInt(uid, 10), Ops: ops})
	req := httptest.NewRequest("POST", "/api/sync/push?as="+strconv.FormatInt(uid, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("uid=%d push status=%d body=%s", uid, w.Code, w.Body.String())
	}
	var resp struct {
		Results []OpResult `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Results
}

// pullTitlesAs 拉取并拼出该 uid 可见的笔记标题（逗号分隔）。
func pullTitlesAs(t *testing.T, r *gin.Engine, uid int64) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/sync/pull?as="+strconv.FormatInt(uid, 10)+"&cursor=0&watermark=0", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("uid=%d pull status=%d", uid, w.Code)
	}
	var out struct {
		Changes []struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解 pull 失败: %v", err)
	}
	titles := make([]string, 0, len(out.Changes))
	for _, ch := range out.Changes {
		var p noteP
		if err := json.Unmarshal(ch.Payload, &p); err != nil {
			continue
		}
		if s, ok := p["title"].(string); ok {
			titles = append(titles, s)
		}
	}
	return strings.Join(titles, ",")
}
