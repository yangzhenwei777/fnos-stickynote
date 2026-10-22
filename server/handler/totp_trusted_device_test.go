package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"stickynote/auth"
	"stickynote/model"
)

// ============================================================================
// trusted_device Cookie SameSite 属性回归测试（v21.126 P2 修复）
//
// 背景：VerifyTOTP 的"记住我"分支签发 trusted_device Cookie 时未调用
// c.SetSameSite(http.SameSiteLaxMode)，导致该 Cookie 缺失 SameSite 属性。
// 现代浏览器对该 Cookie 的跨站携带行为不一致（Chrome Lax / 其他不一定），
// 且与主认证路径 auth_cookie.go 里 access/refresh Cookie 都显式声明
// SameSite=Lax 的既有约定不符。修复动作：在 remember=true 分支的 SetCookie
// 之前加 c.SetSameSite(http.SameSiteLaxMode) 一行。
//
// 本用例锁定：无论 remember 分支，issued 的 trusted_device Set-Cookie 头
// 必须显式包含 SameSite=Lax。若修复被回退（删掉 SetSameSite 一行），本
// 测试必红。
// ============================================================================

// readCookieHeader 从 recorder 里找名为 name 的 Set-Cookie 头（原始字符串）。
// httptest.ResponseRecorder 会累积多条 Set-Cookie，需按名字过滤。
func readCookieHeader(t *testing.T, w *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, line := range w.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(strings.TrimSpace(line), name+"=") {
			return line
		}
	}
	t.Fatalf("no Set-Cookie header for cookie %q; got headers: %v", name, w.Result().Header)
	return ""
}

// mustCreateUserForCookieTest 建一个测试用户并返回（带 TokenVersion 与 ID）。
func mustCreateUserForCookieTest(t *testing.T) *model.User {
	t.Helper()
	u, err := model.CreateUser("td-cookie-user", "Password123!", "q", "a", "admin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// TestIssueTOTPLoginResponse_TrustedDevice_HasSameSite_Lax 核心回归：
// remember=true 分支签发的 trusted_device Cookie 必须含 SameSite=Lax。
func TestIssueTOTPLoginResponse_TrustedDevice_HasSameSite_Lax(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	auth.Init("unit-test-jwt-secret-for-trusted-device")

	c, w := newNoteContext("POST", "/api/auth/verify-totp", "")
	u := mustCreateUserForCookieTest(t)

	issueTOTPLoginResponse(c, u, true)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	hdr := readCookieHeader(t, w, "trusted_device")
	if !strings.Contains(strings.ToLower(hdr), "samesite=lax") {
		t.Errorf("trusted_device Cookie 缺失 SameSite=Lax 属性；原始 Set-Cookie 头：%q", hdr)
	}
	if !strings.Contains(strings.ToLower(hdr), "httponly") {
		t.Errorf("trusted_device Cookie 应 HttpOnly；原始 Set-Cookie 头：%q", hdr)
	}
}

// TestIssueTOTPLoginResponse_TrustedDevice_Clear_HasSameSite_Lax 保护性对齐：
// remember=false 分支的清除指令同样必须含 SameSite=Lax（此分支修复前已正确，
// 本用例仅防未来回退）。
func TestIssueTOTPLoginResponse_TrustedDevice_Clear_HasSameSite_Lax(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	auth.Init("unit-test-jwt-secret-for-trusted-device-clear")

	// 建一个带 trusted_device 上下文，走 remember=false 分支
	c, w := newNoteContext("POST", "/api/auth/verify-totp", "")
	u := mustCreateUserForCookieTest(t)

	issueTOTPLoginResponse(c, u, false)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	hdr := readCookieHeader(t, w, "trusted_device")
	if !strings.Contains(strings.ToLower(hdr), "samesite=lax") {
		t.Errorf("trusted_device 清除指令缺失 SameSite=Lax；原始 Set-Cookie 头：%q", hdr)
	}
	// 清除指令 max-age 应为负值（或 0）
	if !strings.Contains(hdr, "Max-Age=0") && !strings.Contains(hdr, "Max-Age=-1") {
		// 允许 Expires 已过去的旧写法，宽松判定
		t.Logf("清除 Cookie 的 Max-Age 非典型值（不阻断），原始头：%q", hdr)
	}
}

// TestIssueTOTPLoginResponse_AccessCookie_StillHasSameSite 防扰动：
// 主认证 access/refresh Cookie 的 SameSite 语义（此前已正确）不因本轮
// 修复被回退——issueTokenPair 内部调用 setAccessCookie/setRefreshCookie，
// 均显式 SetSameSite(Lax)。
func TestIssueTOTPLoginResponse_AccessCookie_StillHasSameSite(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	auth.Init("unit-test-jwt-secret-for-access-cookie")

	c, w := newNoteContext("POST", "/api/auth/verify-totp", "")
	u := mustCreateUserForCookieTest(t)

	issueTOTPLoginResponse(c, u, true)

	for _, name := range []string{"access_token", "refresh_token"} {
		hdr := readCookieHeader(t, w, name)
		if !strings.Contains(strings.ToLower(hdr), "samesite=lax") {
			t.Errorf("%s Cookie 缺失 SameSite=Lax；原始 Set-Cookie 头：%q", name, hdr)
		}
	}
}
