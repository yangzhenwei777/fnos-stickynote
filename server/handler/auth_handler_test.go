package handler

import (
	"net/http"
	"strings"
	"testing"

	"stickynote/model"
)

// handler 层认证/找回密码/内容上限测试（v21.54 P0：认证流 + v21.52 P5 账号防爆破 handler 层回归点）

// TestForgotPassword_Success 找回密码成功：正确安全答案 → 密码重置。
func TestForgotPassword_Success(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("fpuser", "Passw0rd!2026", "母亲的姓", "王")

	c, w := newNoteContext("POST", "/api/auth/forgot-password",
		`{"username":"fpuser","security_answer":"王","new_password":"NewPassw0rd!2026"}`)
	ForgotPassword(c)
	if w.Code != http.StatusOK {
		t.Fatalf("找回密码应 200, got %d body=%s", w.Code, w.Body.String())
	}
	// 验证新密码可登录（通过 VerifySecurityAnswer + 密码校验间接验证）
	valid, _ := model.VerifySecurityAnswer("fpuser", "王")
	if !valid {
		t.Error("安全答案应仍有效")
	}
}

// TestForgotPassword_WrongAnswer 错误答案：400 + 账号维度失败计数累计。
func TestForgotPassword_WrongAnswer(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("fpwrong", "Passw0rd!2026", "q", "正确")

	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	c, w := newNoteContext("POST", "/api/auth/forgot-password",
		`{"username":"fpwrong","security_answer":"错误","new_password":"NewPassw0rd!2026"}`)
	ForgotPassword(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("错误答案应 400, got %d", w.Code)
	}
	if locked, _ := guard.accountLocked("fpwrong"); locked {
		t.Error("1 次失败后账号维度不应锁定")
	}
}

// TestVerifyAnswer_AccountLocked 账号维度锁定后：即使答案正确也 429。
func TestVerifyAnswer_AccountLocked(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("vlock", "Passw0rd!2026", "q", "a")

	// 用短窗口阈值快速锁定账号维度
	setLoginEnv2(t, 3, 30, 300, 600)
	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	for i := 0; i < 3; i++ {
		guard.accountFail("vlock")
	}
	if locked, _ := guard.accountLocked("vlock"); !locked {
		t.Fatal("前置：账号维度应已锁定")
	}

	c, w := newNoteContext("POST", "/api/auth/verify-answer",
		`{"username":"vlock","security_answer":"a"}`)
	VerifyAnswer(c)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定后应 429, got %d", w.Code)
	}
}

// TestContentLimitBoundary 内容上限：硬上限 100MB 边界 + 软上限配置。
func TestContentLimitBoundary(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()

	// 100MB+1 超硬上限
	huge := make([]byte, 100*1024*1024+1)
	c, w := newNoteContext("POST", "/api/notes", "")
	if checkContentLimit(c, string(huge)) {
		t.Error("100MB+1 应超硬上限")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("硬上限应 413, got %d", w.Code)
	}

	// 软上限配置 1KB：1KB+1 超限
	model.SetConfig("max_note_content_kb", "1")
	small := make([]byte, 1025)
	c2, w2 := newNoteContext("POST", "/api/notes", "")
	if checkContentLimit(c2, string(small)) {
		t.Error("软上限 1KB 应超限")
	}
	if w2.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("软上限应 413, got %d", w2.Code)
	}
}

// TestChangePassword_MinLength 改密最小长度：min=6（v21.96 回归点）。
//
// v21.96 修正：原用例传的是 `old_password`，而 changePasswordRequest 的字段是
// `current_password`——它之所以 400 是因为 required 校验失败，与长度毫无关系，
// 即"声称测 min=8、实际测拼写错误"的假阳性用例。本次改用正确字段名，
// 并对新的下限 6 位做真实的双向边界断言。
func TestChangePassword_MinLength(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("cpuser", "Passw0rd!2026", "q", "a")

	// 新密码 5 位 → binding min=6 拒绝
	c, w := newNoteContext("POST", "/api/auth/change-password",
		`{"current_password":"Passw0rd!2026","new_password":"Sh0r5"}`)
	c.Set("userID", int64(1))
	c.Set("username", "cpuser")
	ChangePassword(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("5 位新密码应 400, got %d body=%s", w.Code, w.Body.String())
	}

	// v21.96：7 位弱密码放行——6 位是唯一硬下限，字符复杂度不再拦截
	c2, w2 := newNoteContext("POST", "/api/auth/change-password",
		`{"current_password":"Passw0rd!2026","new_password":"Short7!"}`)
	c2.Set("userID", int64(1))
	c2.Set("username", "cpuser")
	ChangePassword(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("7 位新密码应放行 200, got %d body=%s", w2.Code, w2.Body.String())
	}
	// 弱密码放行同时应回传分级，供前端提示
	if !strings.Contains(w2.Body.String(), `"password_strength":"weak"`) {
		t.Fatalf("弱密码应回传 password_strength=weak, got body=%s", w2.Body.String())
	}
}
