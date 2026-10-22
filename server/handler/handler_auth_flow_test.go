package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"stickynote/model"
)

// ============================================================================
// auth.go handler 单测（v21.77 P1：补 handler 覆盖率 20.8%→40%）
// 覆盖：setup 状态、注册（含关闭注册/重名/防爆破）、登录（含锁定）、登出、
//       refresh 换发、安全问题（含防枚举假问题）、改密、cookie 策略
// ============================================================================

func resetGuard(t *testing.T) {
	t.Helper()
	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
}

func TestGetSetupStatus(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()

	t.Run("空库 needs_setup=true", func(t *testing.T) {
		c, w := newNoteContext("GET", "/api/auth/setup-status", "")
		GetSetupStatus(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), `"needs_setup":true`) {
			t.Errorf("空库应 needs_setup=true: %s", w.Body.String())
		}
	})
	t.Run("有用户 needs_setup=false", func(t *testing.T) {
		if _, err := model.RegisterUser("su", "Passw0rd!2026", "", ""); err != nil {
			t.Fatal(err)
		}
		c, w := newNoteContext("GET", "/api/auth/setup-status", "")
		GetSetupStatus(c)
		if !strings.Contains(w.Body.String(), `"needs_setup":false`) {
			t.Errorf("有用户应 needs_setup=false: %s", w.Body.String())
		}
	})
}

func TestRegisterHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	t.Run("首用户注册 201 + token", func(t *testing.T) {
		c, w := newNoteContext("POST", "/api/auth/register",
			`{"username":"reg1","password":"Passw0rd!2026","security_question":"q","security_answer":"a"}`)
		Register(c)
		if w.Code != http.StatusCreated {
			t.Fatalf("应 201, got %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "access_token") {
			t.Error("应返回 access_token")
		}
	})

	t.Run("开启注册后第二用户 201", func(t *testing.T) {
		resetGuard(t)
		model.SetConfig("allow_register", "true")
		c, w := newNoteContext("POST", "/api/auth/register",
			`{"username":"reg2","password":"Passw0rd!2026"}`)
		Register(c)
		if w.Code != http.StatusCreated {
			t.Fatalf("开启注册应 201, got %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("关闭注册 403", func(t *testing.T) {
		resetGuard(t)
		model.SetConfig("allow_register", "false")
		c, w := newNoteContext("POST", "/api/auth/register",
			`{"username":"reg3","password":"Passw0rd!2026"}`)
		Register(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("关闭注册应 403, got %d", w.Code)
		}
	})

	t.Run("重名 400", func(t *testing.T) {
		resetGuard(t)
		model.SetConfig("allow_register", "true") // 确保走到重名分支而非注册关闭分支
		c, w := newNoteContext("POST", "/api/auth/register",
			`{"username":"reg1","password":"Passw0rd!2026"}`)
		Register(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("重名应 400, got %d body=%s", w.Code, w.Body.String())
		}
	})
}

// TestRegisterDefaultClosed（v21.133 M-04，审计 Grok-M-04）：
// 新装实例默认不开放自助注册。断言迁移种子 allow_register=false、首管（count==0）恒放行、
// 其后二次注册默认拒绝（403）、管理员显式开启后恢复放行——含"不配置即拒绝"的反向验证。
func TestRegisterDefaultClosed(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	if v, _ := model.GetConfig("allow_register"); v != "false" {
		t.Fatalf("新装迁移种子 allow_register 应为 false, got %q", v)
	}

	// 首个管理员（count==0）不受开关限制，仍可注册
	c, w := newNoteContext("POST", "/api/auth/register",
		`{"username":"admin1","password":"Passw0rd!2026","security_question":"q","security_answer":"a"}`)
	Register(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("首管注册应 201, got %d body=%s", w.Code, w.Body.String())
	}

	// 反向：不改任何配置，第二用户注册应被默认拒绝
	resetGuard(t)
	c, w = newNoteContext("POST", "/api/auth/register",
		`{"username":"lanuser","password":"Passw0rd!2026","security_question":"q","security_answer":"a"}`)
	Register(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("默认关闭注册时第二用户应 403, got %d body=%s", w.Code, w.Body.String())
	}

	// 管理员开启后放行
	if err := model.SetConfig("allow_register", "true"); err != nil {
		t.Fatal(err)
	}
	resetGuard(t)
	c, w = newNoteContext("POST", "/api/auth/register",
		`{"username":"lanuser","password":"Passw0rd!2026","security_question":"q","security_answer":"a"}`)
	Register(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("开启注册后应 201, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestLoginHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)
	if _, err := model.RegisterUser("luser", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}

	t.Run("正确 200 + token", func(t *testing.T) {
		c, w := newNoteContext("POST", "/api/auth/login", `{"username":"luser","password":"Passw0rd!2026"}`)
		Login(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "access_token") {
			t.Error("应含 access_token")
		}
	})

	t.Run("错误密码 401", func(t *testing.T) {
		resetGuard(t)
		c, w := newNoteContext("POST", "/api/auth/login", `{"username":"luser","password":"bad"}`)
		Login(c)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("应 401, got %d", w.Code)
		}
	})

	t.Run("锁定 429", func(t *testing.T) {
		resetGuard(t)
		setLoginEnv2(t, 3, 30, 300, 600)
		c, w := newNoteContext("POST", "/api/auth/login", `{"username":"luser","password":"Passw0rd!2026"}`)
		ip := c.ClientIP()
		for i := 0; i < 3; i++ {
			guard.fail("luser", ip)
		}
		Login(c)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("锁定应 429, got %d", w.Code)
		}
	})
}

func TestLogoutHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("lout", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	c, w := newNoteContext("POST", "/api/auth/logout", "")
	c.Set("userID", int64(1))
	Logout(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200, got %d", w.Code)
	}
	// 登出应吊销 refresh：token_version +1
	u, _ := model.GetUserByID(1)
	if u.TokenVersion != 1 {
		t.Errorf("登出应递增 token_version, got %d", u.TokenVersion)
	}
}

func TestRefreshHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()

	t.Run("有效 refresh token 200（登录建立的会话）", func(t *testing.T) {
		resetGuard(t)
		// v21.94 P3-7：Refresh 由 refresh_sessions 会话驱动——必须走登录建立会话，
		// 直接 GenerateRefreshToken 的裸票因无会话、无 refresh_jti 会被拒绝。
		_, tok := loginAndGetRefresh(t, "ruser", "Passw0rd!2026")
		c, w := newNoteContext("POST", "/api/auth/refresh", `{"refresh_token":"`+tok+`"}`)
		Refresh(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "access_token") {
			t.Error("应返回 access_token")
		}
	})

	t.Run("无 token 401", func(t *testing.T) {
		c, w := newNoteContext("POST", "/api/auth/refresh", `{}`)
		Refresh(c)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("应 401, got %d", w.Code)
		}
	})

	t.Run("无效 token 401", func(t *testing.T) {
		c, w := newNoteContext("POST", "/api/auth/refresh", `{"refresh_token":"garbage"}`)
		Refresh(c)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("应 401, got %d", w.Code)
		}
	})
}

func TestGetSecurityQuestionHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("squser", "Passw0rd!2026", "真实问题?", "答案"); err != nil {
		t.Fatal(err)
	}

	// v21.93 P3（H-05）：不再返回真实问题文本，一律统一话术。
	t.Run("真实用户不泄露真实问题", func(t *testing.T) {
		resetGuard(t)
		c, w := newNoteContext("GET", "/api/auth/security-question?username=squser", "")
		GetSecurityQuestion(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "真实问题") {
			t.Errorf("不得返回真实安全问题（账号信息泄露）: %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), unifiedSecurityQuestionPrompt) {
			t.Errorf("应返回统一话术: %s", w.Body.String())
		}
	})

	// 核心回归点：存在与不存在的用户响应必须**逐字节一致**。
	// 只要两者有任何差异（问题文本不同、状态码不同、字段不同），
	// 就重新变回账号存在性预言机。
	t.Run("存在与不存在用户响应完全一致（消除枚举 oracle）", func(t *testing.T) {
		resetGuard(t)
		c1, w1 := newNoteContext("GET", "/api/auth/security-question?username=squser", "")
		GetSecurityQuestion(c1)

		resetGuard(t)
		c2, w2 := newNoteContext("GET", "/api/auth/security-question?username=ghost", "")
		GetSecurityQuestion(c2)

		if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
			t.Fatalf("两者都应 200, got %d / %d", w1.Code, w2.Code)
		}
		if w1.Body.String() != w2.Body.String() {
			t.Errorf("存在/不存在用户的响应必须完全一致（否则即为枚举预言机）：\n  真实用户=%s\n  不存在=%s",
				w1.Body.String(), w2.Body.String())
		}
	})

	t.Run("无 username 400", func(t *testing.T) {
		c, w := newNoteContext("GET", "/api/auth/security-question", "")
		GetSecurityQuestion(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应 400, got %d", w.Code)
		}
	})
}

func TestChangePasswordHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("cp2", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}

	t.Run("成功 200", func(t *testing.T) {
		c, w := newNoteContext("POST", "/api/auth/change-password",
			`{"current_password":"Passw0rd!2026","new_password":"NewPass1234!"}`)
		c.Set("userID", int64(1))
		c.Set("username", "cp2")
		ChangePassword(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
		}
		// v21.105 P0-2：成功响应体必须携带新签发的 token 对——前端本地 storage
		// 持有旧 token 且请求头优先于 cookie，仅 Set-Cookie 会导致本会话下一请求 401 被踢
		var resp struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
		}
		if resp.AccessToken == "" || resp.RefreshToken == "" {
			t.Errorf("改密成功响应应包含 access_token/refresh_token，got body=%s", w.Body.String())
		}
	})
	t.Run("旧密码错误 400", func(t *testing.T) {
		// v21.105 P0-3：业务密码错误 401→400——401 会被前端拦截器当作会话过期
		// 误触发 refresh→踢登录页，用户看不到「当前密码错误」（操作逻辑审查 P0-3）
		c, w := newNoteContext("POST", "/api/auth/change-password",
			`{"current_password":"wrong","new_password":"NewPass1234!"}`)
		c.Set("userID", int64(1))
		c.Set("username", "cp2")
		ChangePassword(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应 400, got %d", w.Code)
		}
	})
}

func TestUpdateSecurityQuestionHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("usq", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	c, w := newNoteContext("POST", "/api/auth/security-question",
		// v21.90 H-02：敏感操作二次认证——修改安全问题必须携带当前密码
		`{"current_password":"Passw0rd!2026","security_question":"新问题?","security_answer":"新答案八个字了吧"}`)
	c.Set("userID", int64(1))
	c.Set("username", "usq")
	UpdateSecurityQuestion(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
	}

	// 反向用例：不带当前密码 → binding required 拦截 400（拒绝，不进入修改逻辑）
	c2, w2 := newNoteContext("POST", "/api/auth/security-question",
		`{"security_question":"问题2","security_answer":"答案二八个字了啊"}`)
	c2.Set("userID", int64(1))
	c2.Set("username", "usq")
	UpdateSecurityQuestion(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("缺当前密码应 400, got %d", w2.Code)
	}
}

func TestCookieSecureAndIsHTTPS(t *testing.T) {
	t.Run("COOKIE_SECURE=true 强制 Secure", func(t *testing.T) {
		t.Setenv("COOKIE_SECURE", "true")
		c, _ := newNoteContext("GET", "/", "")
		if !cookieSecure(c) {
			t.Error("COOKIE_SECURE=true 应返回 true")
		}
	})
	t.Run("COOKIE_SECURE=false 强制关闭", func(t *testing.T) {
		t.Setenv("COOKIE_SECURE", "false")
		c, _ := newNoteContext("GET", "/", "")
		if cookieSecure(c) {
			t.Error("COOKIE_SECURE=false 应返回 false")
		}
	})
	t.Run("默认跟随协议", func(t *testing.T) {
		t.Setenv("COOKIE_SECURE", "")
		c, _ := newNoteContext("GET", "/", "")
		if cookieSecure(c) {
			t.Error("HTTP 默认应 false")
		}
		c.Request.Header.Set("X-Forwarded-Proto", "https")
		if !cookieSecure(c) {
			t.Error("X-Forwarded-Proto=https 应 true")
		}
		if !isHTTPS(c) {
			t.Error("isHTTPS 应识别 XFP")
		}
	})
}
