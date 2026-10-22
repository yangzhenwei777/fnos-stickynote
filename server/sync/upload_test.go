package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"stickynote/database"
	"stickynote/model"
	"stickynote/syncspec"
)

// upload_test.go —— P1 批次3 sync 上传通道契约（ADR-003 R2/R5 + ADR-004 R3）。

func syncPNG(name string, seed byte) []byte {
	body := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{seed}, 64)...)
	return body
}

func doSyncUpload(t *testing.T, r *gin.Engine, filename string, data []byte, declaredSHA string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormField("filename")
	io.WriteString(fw, filename)
	sw, _ := mw.CreateFormField("sha256")
	io.WriteString(sw, declaredSHA)
	part, _ := mw.CreateFormFile("file", filename)
	part.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/sync/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func uploadEngine(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	if err := database.Init(filepath.Join(t.TempDir(), "up.db"), "99.0.0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	if _, err := database.DB.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','user'),(2,'u2','x','user')`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	Configure(t.TempDir())
	SetAttachmentsDir(dir)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := r.Group("/api", func(c *gin.Context) { c.Set("userID", int64(1)); c.Next() })
	auth.POST("/sync/upload", SyncUpload)
	auth.POST("/sync/push", Push)
	auth.GET("/sync/pull", Pull)
	return r, dir
}

func shaHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestSyncUploadAcceptMismatchIdempotent(t *testing.T) {
	r, dir := uploadEngine(t)
	data := syncPNG("a.png", 7)
	name := "11111111-2222-3333-4444-555555555555.png"

	// 正确申报 → 200，入库 sha=服务端实算，文件落盘。
	w := doSyncUpload(t, r, name, data, shaHex(data))
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["sha256"] != shaHex(data) {
		t.Fatalf("响应权威哈希不符: %v", resp)
	}
	if model.GetUploadSHA256(name) != shaHex(data) {
		t.Fatal("权威哈希必须入库")
	}
	if _, err := statAttachment(dir, name); err != nil {
		t.Fatalf("文件应落盘: %v", err)
	}

	// 同件重传（幂等）→ 200 status=ok。
	w = doSyncUpload(t, r, name, data, shaHex(data))
	if w.Code != 200 {
		t.Fatalf("幂等重传: %d %s", w.Code, w.Body.String())
	}

	// 申报造假（MISMATCH）→ 拒收且**不落新行**（新文件名）。
	name2 := "aaaaaaaa-2222-3333-4444-555555555555.png"
	w = doSyncUpload(t, r, name2, data, shaHex(syncPNG("x", 9)))
	var bad map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &bad)
	if w.Code != 400 || bad["code"] != syncspec.CodeUploadHashMismatch {
		t.Fatalf("造假申报应 MISMATCH: %d %s", w.Code, w.Body.String())
	}
	if _, _, _, exists, _ := model.GetUploadMeta(name2); exists {
		t.Fatal("MISMATCH 拒收不得留行")
	}
	if _, err := statAttachment(dir, name2); err == nil {
		t.Fatal("MISMATCH 拒收必须删临时文件（T-08）")
	}

	// 同名不同内容（同尺寸变异）→ IDENTITY_CONFLICT 且既有件不动（T-08）。
	mutated := make([]byte, len(data))
	copy(mutated, data)
	mutated[len(mutated)-1] ^= 0xFF
	w = doSyncUpload(t, r, name, mutated, shaHex(mutated))
	_ = json.Unmarshal(w.Body.Bytes(), &bad)
	if w.Code != 409 || bad["code"] != syncspec.CodeUploadIdentityConflict {
		t.Fatalf("同名异内容应 IDENTITY_CONFLICT: %d %s", w.Code, w.Body.String())
	}
	if model.GetUploadSHA256(name) != shaHex(data) {
		t.Fatal("冲突拒绝不得改动既有权威件")
	}
}

func TestPushRefsAttachmentNotFoundThenRecover(t *testing.T) {
	r, dir := uploadEngine(t)
	name := "deadbeef-1111-2222-3333-444444444444.png"
	data := syncPNG("p.png", 3)

	// 缺件的 CREATE op → ATTACHMENT_NOT_FOUND，且不登记 op（sync_operations 零行）。
	op := PushOp{OpID: "attach-1", EntityType: "NOTE", EntitySyncID: "s-att", Operation: "CREATE",
		Payload: mustJSON(noteP{"sync_id": "s-att", "title": "图笔", "content": `<img src="/uploads/` + name + `">`,
			"attachment_refs": []map[string]any{{"filename": name, "sha256": shaHex(data), "size": len(data)}}})}
	res := doPush(t, r, "devX", op)
	if res[0].Code != syncspec.CodeAttachmentNotFound {
		t.Fatalf("缺 ref 应 ATTACHMENT_NOT_FOUND: %+v", res)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(res[0].Error), &payload)
	if miss, _ := payload["missing"].([]any); len(miss) != 1 || miss[0] != name {
		t.Fatalf("missing 清单不符: %s", res[0].Error)
	}
	var nOps int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_operations`).Scan(&nOps); err != nil || nOps != 0 {
		t.Fatalf("ATTACHMENT_NOT_FOUND 不得登记 op（R3 重试前提）: %d %v", nOps, err)
	}
	if n := countChangesTotal(t); n != 0 {
		t.Fatalf("拒绝路径零副作用（无事件）: %d", n)
	}

	// 客户端重传缺失文件 → 同 op_id 同 payload 原样重试 → ACKED。
	if w := doSyncUpload(t, r, name, data, shaHex(data)); w.Code != 200 {
		t.Fatalf("重传失败: %d %s", w.Code, w.Body.String())
	}
	res = doPush(t, r, "devX", op)
	if res[0].Code != CodeAcked {
		t.Fatalf("重传后同 op 重试应 ACKED（R3 闭环）: %+v", res)
	}
	// 事件 attachment_refs 携带非空权威哈希（R4 保证位经通道哈希入库后自然成立）。
	pull := doPull(t, r, 0, 0)
	ch := pull["changes"].([]any)[0].(map[string]any)
	pl, _ := json.Marshal(ch["payload"])
	if !bytes.Contains(pl, []byte(shaHex(data))) {
		t.Fatalf("事件 refs 必须含权威哈希: %s", pl)
	}
	_ = dir
}

func statAttachment(dir, name string) (os.FileInfo, error) {
	return os.Stat(filepath.Join(dir, name))
}

func countChangesTotal(t *testing.T) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_changes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
