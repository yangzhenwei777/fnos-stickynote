package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"stickynote/auth"
	"stickynote/database"
	"stickynote/model"
)

// setupTestDB 初始化独立临时库（每测试用例互不干扰）
func setupTestDB(t *testing.T) {
	t.Helper()
	if err := database.Init(tempDBPath(t), "1.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() {
		database.DB.Close()
		database.DB = nil
	})
}

// doAuthReq 构造一次穿过 RequireAuth 的请求，返回状态码与 userID 上下文
func doAuthReq(t *testing.T, token string) (int, interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/probe", RequireAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"uid": c.GetInt64("userID")})
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/probe", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestRequireAuthNoToken(t *testing.T) {
	setupTestDB(t)
	code, _ := doAuthReq(t, "")
	if code != http.StatusUnauthorized {
		t.Errorf("无 token 应 401, got %d", code)
	}
}

func TestRequireAuthInvalidToken(t *testing.T) {
	setupTestDB(t)
	code, _ := doAuthReq(t, "not-a-jwt")
	if code != http.StatusUnauthorized {
		t.Errorf("伪造 token 应 401, got %d", code)
	}
}

func TestRequireAuthRefreshTokenRejected(t *testing.T) {
	setupTestDB(t)
	u, err := model.RegisterUser("rtuser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	refresh, _, err := auth.GenerateRefreshToken(u.ID, u.TokenVersion, 3600, false)
	if err != nil {
		t.Fatalf("gen refresh: %v", err)
	}
	code, _ := doAuthReq(t, refresh)
	if code != http.StatusUnauthorized {
		t.Errorf("refresh token 访问业务 API 应 401, got %d", code)
	}
}

func TestRequireAuthValidTokenPasses(t *testing.T) {
	setupTestDB(t)
	u, err := model.RegisterUser("vuser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	tok, err := auth.GenerateAccessToken(u.ID, u.Username, u.Role, u.TokenVersion)
	if err != nil {
		t.Fatalf("gen access: %v", err)
	}
	code, _ := doAuthReq(t, tok)
	if code != http.StatusOK {
		t.Errorf("有效 access token 应 200, got %d", code)
	}
}

// 登出/改密后 token_version 递增，旧 access token 必须立即失效（v18.6 语义）
func TestRequireAuthStaleTokenVersionRejected(t *testing.T) {
	setupTestDB(t)
	u, err := model.RegisterUser("staleuser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	tok, _ := auth.GenerateAccessToken(u.ID, u.Username, u.Role, u.TokenVersion)
	if err := model.BumpTokenVersion(u.ID); err != nil {
		t.Fatalf("bump: %v", err)
	}
	code, _ := doAuthReq(t, tok)
	if code != http.StatusUnauthorized {
		t.Errorf("旧版本 access token 应 401, got %d", code)
	}
}

// 被禁用用户（status=disabled）即使 token 有效也被拦截（v20.5）
func TestRequireAuthDisabledUserRejected(t *testing.T) {
	setupTestDB(t)
	u, err := model.RegisterUser("disuser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := model.UpdateUserStatus(u.ID, "disabled"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	tok, _ := auth.GenerateAccessToken(u.ID, u.Username, u.Role, u.TokenVersion)
	code, _ := doAuthReq(t, tok)
	if code != http.StatusUnauthorized {
		t.Errorf("禁用用户应 401, got %d", code)
	}
}

func TestRequireAdmin(t *testing.T) {
	setupTestDB(t)
	gin.SetMode(gin.TestMode)
	// 第一个注册用户自动成为 admin
	admin, err := model.RegisterUser("adm1", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	// 第二个为普通用户
	user, err := model.RegisterUser("reg2", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	r := gin.New()
	r.GET("/admin/probe", RequireAuth(), RequireAdmin(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 普通用户 403
	utok, _ := auth.GenerateAccessToken(user.ID, user.Username, user.Role, user.TokenVersion)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/probe", nil)
	req.Header.Set("Authorization", "Bearer "+utok)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("普通用户访问 admin 应 403, got %d", w.Code)
	}

	// 管理员 200
	atok, _ := auth.GenerateAccessToken(admin.ID, admin.Username, admin.Role, admin.TokenVersion)
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/admin/probe", nil)
	req2.Header.Set("Authorization", "Bearer "+atok)
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("管理员访问 admin 应 200, got %d", w2.Code)
	}
}
