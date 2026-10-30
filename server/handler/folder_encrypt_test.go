package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"stickynote/app"
	"stickynote/auth"
	"stickynote/model"
)

func newFolderCtx(t *testing.T, uid int64, folderID int64, body any, header map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	raw, _ := json.Marshal(body)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		c.Request.Header.Set(k, v)
	}
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(folderID, 10)}}
	c.Set("userID", uid)
	c.Set("username", "tester")
	c.Set("tokenVersion", int64(0))
	return c, w
}

func TestFolderEncryptHandlersAndGrants(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	auth.Init("handler-folder-secret")

	u, err := model.RegisterUser("folder_h", "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatal(err)
	}
	fs := app.NewServerFolderService()
	f, err := fs.Create(u.ID, "夹H")
	if err != nil {
		t.Fatal(err)
	}
	fid := *f.ID

	// 密码不一致 → 400
	c, w := newFolderCtx(t, u.ID, fid, map[string]string{"password": "abc123", "confirm_password": "other"}, nil)
	EncryptFolder(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("不一致应 400, got %d %s", w.Code, w.Body.String())
	}
	// 正常加密 → 200 且 version=2
	c, w = newFolderCtx(t, u.ID, fid, map[string]string{"password": "abc123", "confirm_password": "abc123"}, nil)
	EncryptFolder(c)
	if w.Code != http.StatusOK {
		t.Fatalf("加密应 200, got %d %s", w.Code, w.Body.String())
	}
	var encResp struct {
		Version int64 `json:"version"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &encResp)
	if encResp.Version != 2 {
		t.Fatalf("加密后 version 应 2, got %d", encResp.Version)
	}

	// 无令牌：需要解锁
	c, _ = newFolderCtx(t, u.ID, fid, nil, nil)
	if !folderRequiresUnlock(c, fid) {
		t.Fatal("未带令牌应要求解锁")
	}
	if len(folderGrants(c)) != 0 {
		t.Fatal("无令牌不应有授权")
	}

	// 解锁拿令牌
	c, w = newFolderCtx(t, u.ID, fid, map[string]string{"password": "abc123"}, nil)
	UnlockFolder(c)
	if w.Code != http.StatusOK {
		t.Fatalf("解锁应 200, got %d %s", w.Code, w.Body.String())
	}
	var unlockResp struct {
		Token string `json:"unlock_token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &unlockResp)
	if unlockResp.Token == "" {
		t.Fatal("应返回 unlock_token")
	}

	// 带令牌：已授权
	c, _ = newFolderCtx(t, u.ID, fid, nil, map[string]string{folderGrantHeader: unlockResp.Token})
	if folderRequiresUnlock(c, fid) {
		t.Fatal("带有效令牌不应要求解锁")
	}
	if _, ok := folderGrants(c)[fid]; !ok {
		t.Fatal("有效令牌应解析出文件夹授权")
	}

	// 篡改令牌：fail-closed
	c, _ = newFolderCtx(t, u.ID, fid, nil, map[string]string{folderGrantHeader: unlockResp.Token + "x"})
	if len(folderGrants(c)) != 0 {
		t.Fatal("篡改令牌不应授权")
	}

	// 错误密码 → 403；连续失败触发 429
	c, w = newFolderCtx(t, u.ID, fid, map[string]string{"password": "nope"}, nil)
	UnlockFolder(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("错误密码应 403, got %d", w.Code)
	}
	for i := 0; i < 5; i++ {
		c, w = newFolderCtx(t, u.ID, fid, map[string]string{"password": "nope"}, nil)
		UnlockFolder(c)
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("连续失败应 429, got %d %s", w.Code, w.Body.String())
	}

	// 删除锁定文件夹 → 423
	c, w = newFolderCtx(t, u.ID, fid, nil, nil)
	DeleteFolder(c)
	if w.Code != http.StatusLocked {
		t.Fatalf("删除锁定文件夹应 423, got %d %s", w.Code, w.Body.String())
	}
}
