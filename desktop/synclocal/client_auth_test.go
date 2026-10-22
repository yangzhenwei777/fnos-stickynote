// client_auth_test.go —— 登录/换票错误分类（批次 8-Alt 刀 6）。
//
// 钉的是"UI 能不能说对话"的地基：服务端账密错回 401、会话作废也回 401，
// 若两者归一成同一个哨兵，桌面登录页就只能对输错密码的用户说"你的会话已被吊销"。
// 同时反向钉 refresh 通道**不得**被改成账密错——它的 401 语义就是"票废了，要重登"，
// 既有降级链（ensureAccess → demoteToRelogin）全押在这个判断上。
package synclocal

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginMaps401ToBadCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"用户名或密码错误"}`))
		default:
			t.Errorf("登录通道不该请求 %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	_, _, err := Login(srv.URL, "admin", "wrong-password")
	if err == nil {
		t.Fatal("401 必须报错")
	}
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("账密错必须是 ErrBadCredentials，得 %v", err)
	}
	if errors.Is(err, ErrAuthRevoked) {
		t.Fatal("不得同时命中 ErrAuthRevoked——否则上层降级链会把【输错密码】当会话作废处理")
	}
	if !strings.Contains(err.Error(), "用户名或密码错误") {
		t.Fatalf("服务端原文要留在错误里供 UI 兜底显示，得 %v", err)
	}
}

func TestRefresh401StaysAuthRevoked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid refresh token"}`))
	}))
	defer srv.Close()

	_, _, err := Refresh(srv.URL, "stale-token")
	if !errors.Is(err, ErrAuthRevoked) {
		t.Fatalf("refresh 的 401 语义是【票废了】，必须仍是 ErrAuthRevoked，得 %v", err)
	}
	if errors.Is(err, ErrBadCredentials) {
		t.Fatal("refresh 通道不得被判成账密错（ensureAccess 的降级决策依赖它）")
	}
}

func TestLoginForbiddenStillAuthRevoked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"account disabled"}`))
	}))
	defer srv.Close()
	if _, _, err := Login(srv.URL, "u", "p"); !errors.Is(err, ErrAuthRevoked) {
		t.Fatalf("403（账号被禁/吊销类）应保持 ErrAuthRevoked，得 %v", err)
	}
}

func TestLoginLockoutIsNotTerminalSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"尝试次数过多，请 10 分钟后再试"}`))
	}))
	defer srv.Close()
	_, _, err := Login(srv.URL, "u", "p")
	if err == nil {
		t.Fatal("429 必须报错")
	}
	if errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrAuthRevoked) {
		t.Fatalf("防爆破锁定既不是账密错也不是吊销，得 %v", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("状态码要留在错误里，得 %v", err)
	}
}

func TestLoginSuccessReturnsBothTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"A","refresh_token":"R","user":{"id":1}}`))
	}))
	defer srv.Close()
	a, rr, err := Login(srv.URL, "admin", "good")
	if err != nil {
		t.Fatal(err)
	}
	if a != "A" || rr != "R" {
		t.Fatalf("票证回传错: %q %q", a, rr)
	}
}

// 分类器侧的连带钉：账密错不该被调度器当成"离线探测"或"终态停手"。
// （RemoteLogin 是用户主动动作，不走调度轮；但 ensureAccess 万一拿到它，分类必须合理。）
func TestBadCredentialsClassificationIsRetryNotTransport(t *testing.T) {
	if got := classifyRoundErr(ErrBadCredentials); got != outcomeRetry {
		t.Fatalf("ErrBadCredentials 应归普通重试档，得 %d", got)
	}
	if isTransportErr(ErrBadCredentials) {
		t.Fatal("账密错不是网络问题，不该进离线探测态")
	}
}

// ---- v21.151 TOTP 二因子登录（生产 Bug2：2FA 账号桌面端登录误报「会话已失效」）----

// TestLoginTOTPRequiredIsExplicit v21.151：服务端对 2FA 账号登录回 200 {totp_required}（无 token）。
// 旧缺陷：Login 静默返回空票 + nil error → 下游任一调用 401 → UI 误报「登录会话已失效（被吊销）」。
// 修后：必须显式 ErrTOTPRequired，且临时票随错误携带供第二步换正式会话。
func TestLoginTOTPRequiredIsExplicit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			t.Errorf("不期望的路径 %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"totp_required":true,"totp_token":"tok-123","user":{"id":1,"username":"alice"}}`))
	}))
	defer srv.Close()

	access, refresh, err := Login(srv.URL, "alice", "pw-right")
	if access != "" || refresh != "" {
		t.Fatalf("totp_required 不得返回票证: %q %q", access, refresh)
	}
	if !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("必须显式 ErrTOTPRequired（旧实现在此静默空票），得 %v", err)
	}
	if errors.Is(err, ErrBadCredentials) || errors.Is(err, ErrAuthRevoked) {
		t.Fatal("TOTP 要求不得混入账密错/吊销两个终态哨兵（UI 会照此误报）")
	}
	var te *TOTPRequiredError
	if !errors.As(err, &te) || te.Token != "tok-123" {
		t.Fatalf("临时票必须随错误携带供第二步使用，得 %+v", te)
	}
}

// TestLoginEmptyTokensRejected v21.151：200 但缺 access_token（异常响应）必须显式报错。
func TestLoginEmptyTokensRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"id":1}}`))
	}))
	defer srv.Close()
	_, _, err := Login(srv.URL, "u", "p")
	if err == nil {
		t.Fatal("200 无票必须报错（旧缺陷：静默空票 → 下游 401 误报吊销）")
	}
	if !strings.Contains(err.Error(), "LOGIN_INCOMPLETE") {
		t.Fatalf("错误须带可识别前缀供前端映射，得 %v", err)
	}
}

// TestVerifyTOTPStep2 v21.151：第二步临时票+动态码换正式票；服务端中文文案原样透传。
func TestVerifyTOTPStep2(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/verify-totp" {
			t.Errorf("路径错: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"access_token":"A2","refresh_token":"R2","user":{"id":1}}`))
	}))
	defer ok.Close()
	a, rr, err := VerifyTOTP(ok.URL, "tok-123", "123456")
	if err != nil || a != "A2" || rr != "R2" {
		t.Fatalf("第二步换票失败: %q %q %v", a, rr, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"验证码错误"}`))
	}))
	defer bad.Close()
	_, _, err = VerifyTOTP(bad.URL, "tok-123", "000000")
	if err == nil || err.Error() != "验证码错误" {
		t.Fatalf("验证码错误文案必须原样透出（不得含 sync http / JSON 噪声），得 %v", err)
	}
}
