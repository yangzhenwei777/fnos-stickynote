package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// v2.1.5 CORS 判定范式修正的门禁。
//
// 背景（两轮安卓实测）：
//   - 2.1.2 按 hostname 校正放行 → 只覆盖「同 hostname 剥端口」一种形态；
//   - 2.1.4 信任 Sec-Fetch-Site → 安卓 WebView 请求头形态不可控（老内核/X5），
//     实测仍 403；
//   - 根因是 gin-contrib/cors 对**不允许的 Origin 一律 AbortWithStatus(403)**——
//     超出 CORS 标准的过度防御，且把"服务端判定不了的同源请求"（Host 被网关/
//     中继改写）一并误杀。
//
// 新口径：凭据边界才是真正防线（SameSite=Lax cookie 跨站不发送 + Bearer 跨站
// 拿不到），服务端不再替浏览器做跨域裁决：
//   ① 白名单命中（且未配 *）→ 反射 ACAO（跨端调试场景，行为不变）；
//   ② 其余一律放行处理、**不下发 ACAO**——同源请求浏览器本不做 CORS 校验（畅通），
//      跨站请求浏览器拦读取（数据不泄露）。
// P10 口径不变：* 配置只告警不生效。
// ---------------------------------------------------------------------------

func newCORSRouter(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS())
	r.Any("/*any", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r
}

// withOrigins 临时替换白名单来源（测试注入）。
func withOrigins(t *testing.T, m map[string]bool) {
	t.Helper()
	orig := originsLoader
	originsLoader = func() (map[string]bool, bool) { return m, false }
	t.Cleanup(func() { originsLoader = orig })
}

func acaoOf(w *httptest.ResponseRecorder) string {
	return w.Header().Get("Access-Control-Allow-Origin")
}

// 静态资源完全不走 CORS：即便带了 Origin（Vite 的 crossorigin）也必须是 200。
func TestCORSSkipsStaticAssets(t *testing.T) {
	r := newCORSRouter(nil)

	for _, p := range []string{"/assets/index-abc.js", "/favicon.png", "/manifest.webmanifest", "/"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://nas.local"+p, nil)
		req.Header.Set("Origin", "http://evil.example")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("静态路径 %s 被 CORS 拦截：status=%d（Vite crossorigin 会带 Origin，403 会让页面永久卡启动页）", p, w.Code)
		}
	}
}

// ① 白名单命中：反射 ACAO（跨端调试场景，行为不变）。
func TestCORSReflectsAllowlistedOrigin(t *testing.T) {
	withOrigins(t, map[string]bool{"https://dev.example": true})
	r := newCORSRouter(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://nas.local/api/auth/setup-status", nil)
	req.Header.Set("Origin", "https://dev.example")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("白名单 Origin 被拒：status=%d", w.Code)
	}
	if acaoOf(w) != "https://dev.example" {
		t.Fatalf("白名单 Origin 未反射 ACAO：got %q", acaoOf(w))
	}
}

// P10 口径不变：* 配置只告警不生效（不反射 ACAO，防止"任意来源+凭据"）。
func TestCORSStarConfigStaysInert(t *testing.T) {
	orig := originsLoader
	originsLoader = func() (map[string]bool, bool) { return nil, true } // all=true
	t.Cleanup(func() { originsLoader = orig })

	r := newCORSRouter(nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://nas.local/api/auth/setup-status", nil)
	req.Header.Set("Origin", "https://anything.example")
	r.ServeHTTP(w, req)

	if acaoOf(w) != "" {
		t.Fatalf("* 配置不应反射 ACAO：got %q", acaoOf(w))
	}
}

// ② 核心修复：安卓中继形态（Origin hostname ≠ Host hostname，无 Sec-Fetch-Site）
// 必须放行（200），且不下发 ACAO——同源请求浏览器不做 CORS 校验，畅通即可。
// 反向验证：把「非白名单直接 Next」改回 cc(c)，本用例立刻变 403。
func TestCORSAllowsAndroidRelayForm(t *testing.T) {
	withOrigins(t, nil)
	r := newCORSRouter(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:13389/api/auth/login", nil)
	req.Header.Set("Origin", "https://testhome.fnos.net")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("安卓中继形态仍被拦：status=%d（403 空 body 会变成裸『登录失败』）", w.Code)
	}
	if acaoOf(w) != "" {
		t.Fatalf("非白名单 Origin 不应下发 ACAO：got %q", acaoOf(w))
	}
}

// 预检也不再 403：跨站预检得到 200（无 ACAO，浏览器仍拦读取）；
// 同源请求根本不发预检，此行为只影响真跨站方。
func TestCORSPreflightNeverForbidden(t *testing.T) {
	withOrigins(t, nil)
	r := newCORSRouter(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "http://nas.local/api/auth/login", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	r.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatal("预检被 403——过度防御回归（会把服务端判定不了的同源请求一并误杀）")
	}
	if acaoOf(w) != "" {
		t.Fatalf("非白名单预检不应下发 ACAO：got %q", acaoOf(w))
	}
}

// 跨站请求不下发 ACAO：浏览器据此拦截读取，数据不泄露（这是 CORS 的本职）。
func TestCORSCrossSiteReadBlockedByMissingACAO(t *testing.T) {
	withOrigins(t, nil)
	r := newCORSRouter(nil)

	for _, h := range []string{"https://evil.example", "https://other.fnos.net"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://nas.local/api/auth/setup-status", nil)
		req.Header.Set("Origin", h)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		r.ServeHTTP(w, req)

		if acaoOf(w) != "" {
			t.Fatalf("跨站 Origin %s 不应反射 ACAO（否则跨站可读取响应）", h)
		}
	}
}

// 同 hostname 剥端口形态（2.1.2 场景）继续可用：放行 + 无 ACAO。
func TestCORSSameHostPortStrippedStillWorks(t *testing.T) {
	withOrigins(t, nil)
	r := newCORSRouter(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://nas.local/api/auth/login", nil)
	req.Header.Set("Origin", "http://nas.local:5666")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK || acaoOf(w) != "" {
		t.Fatalf("剥端口形态异常：status=%d acao=%q", w.Code, acaoOf(w))
	}
}

func TestHostnameOnly(t *testing.T) {
	cases := map[string]string{
		"nas.local":      "nas.local",
		"nas.local:5666": "nas.local",
		"10.0.0.5":       "10.0.0.5",
		"10.0.0.5:80":    "10.0.0.5",
		"[::1]:5666":     "[::1]",
		"":               "",
	}
	for in, want := range cases {
		if got := hostnameOnly(in); got != want {
			t.Fatalf("hostnameOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateUA(t *testing.T) {
	if got := truncateUA(strings.Repeat("x", 120)); len(got) != 80 {
		t.Fatalf("truncateUA 长度 = %d, want 80", len(got))
	}
	if got := truncateUA("short"); got != "short" {
		t.Fatalf("truncateUA 短串被改：got %q", got)
	}
}
