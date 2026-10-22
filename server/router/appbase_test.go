package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"stickynote/appbase"
)

// v2.1.2：子路径挂载支持的单测。
//
// 这是本次「移动端启动卡死」根治的服务端侧闭环——前端把资源相对化之后，
// 服务端必须能从子路径请求里还原出应用内路径，并把挂载基址注入 index.html。

func TestAppBasePrefix(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		header string // X-Forwarded-Prefix
		want   string
	}{
		{"根首页", "/", "", ""},
		{"根前端路由", "/login", "", ""},
		{"根深路由", "/notes/123", "", ""},
		{"根静态资源", "/assets/index-abc.js", "", ""},
		{"根上传图片", "/uploads/x.png", "", ""},

		{"子路径首页", "/app/stickynote/", "", "/app/stickynote"},
		{"子路径前端路由", "/app/stickynote/login", "", "/app/stickynote"},
		{"子路径深路由", "/app/stickynote/notes/123", "", "/app/stickynote"},
		{"子路径静态资源", "/app/stickynote/assets/index-abc.js", "", "/app/stickynote"},
		{"子路径上传图片", "/app/stickynote/uploads/x.png", "", "/app/stickynote"},
		{"子路径manifest", "/app/stickynote/manifest.webmanifest", "", "/app/stickynote"},

		// 显式头优先，且允许非 /app/ 形态（运维自定义反代）
		{"显式头", "/anything/here", "/proxy/notepad", "/proxy/notepad"},
		{"显式头根", "/", "/", ""},

		// 非法头不得透传（防开放代理 / 目录穿越）
		{"非法头穿越", "/x", "/a/../../etc", ""},
		{"非法头字符", "/x", "/a b", ""},
		{"非法头过长", "/x", "/a/b/c/d", ""},

		// /app/ 形态下的相邻应用名：/app/xy 就是它自己的挂载点，
		// 不能被 /app/x 误吞（段边界由 stripAppBase 保证）
		{"相邻应用名", "/app/xy/notes", "", "/app/xy"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("X-Forwarded-Prefix", tc.header)
			}
			if got := appBasePrefix(tc.path, h); got != tc.want {
				t.Fatalf("appBasePrefix(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestStripAppBase(t *testing.T) {
	cases := []struct{ path, prefix, want string }{
		{"/app/stickynote/", "/app/stickynote", "/"},
		{"/app/stickynote/login", "/app/stickynote", "/login"},
		{"/app/stickynote/assets/a.js", "/app/stickynote", "/assets/a.js"},
		{"/app/stickynote", "/app/stickynote", "/"},
		{"/login", "", "/login"},
		{"/app/xy/notes", "/app/x", "/app/xy/notes"}, // 段边界不匹配 → 原样
	}
	for _, tc := range cases {
		if got := stripAppBase(tc.path, tc.prefix); got != tc.want {
			t.Fatalf("stripAppBase(%q, %q) = %q, want %q", tc.path, tc.prefix, got, tc.want)
		}
	}
}

func TestNormalizeAppPrefixRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"/a/../b", "/a/./b", "/a//b", "//a", "/a b", "/a/b/c/d", strings.Repeat("/x", 5)} {
		if got := normalizeAppPrefix(bad); got != "" {
			t.Fatalf("normalizeAppPrefix(%q) = %q, want \"\"", bad, got)
		}
	}
	for _, ok := range []string{"/app/stickynote", "app/stickynote", "/app/stickynote/"} {
		if got := normalizeAppPrefix(ok); got != "/app/stickynote" {
			t.Fatalf("normalizeAppPrefix(%q) = %q, want /app/stickynote", ok, got)
		}
	}
}

// TestAppBaseHandlerRewritesBeforeRouting 锁住最关键的一条语义：
// **路径还原必须发生在 gin 路由匹配之前**。
//
// 若改成在 r.Use() 中间件里改 c.Request.URL.Path，gin 早已按原始路径判定了
// NoRoute，子路径下的 /api/* 会全部 404（这正是冒烟 16.7b.8 抓到的真实缺陷）。
func TestAppBaseHandlerRewritesBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var seenPath string
	var seenBase string
	r.GET("/api/health", func(c *gin.Context) {
		seenPath = c.Request.URL.Path
		seenBase = appbase.FromContext(c.Request.Context())
		c.String(http.StatusOK, "ok")
	})
	h := &appBaseHandler{engine: r}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app/stickynote/api/health", nil)
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("子路径 API 未被路由到 handler：status=%d（说明还原发生在匹配之后）", w.Code)
	}
	if seenPath != "/api/health" {
		t.Fatalf("handler 看到的路径 = %q, want /api/health", seenPath)
	}
	if seenBase != "/app/stickynote" {
		t.Fatalf("context 里的挂载前缀 = %q, want /app/stickynote", seenBase)
	}

	// 根路径请求不得被改写
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK || seenPath != "/api/health" {
		t.Fatalf("根路径请求被误改写：status=%d path=%q", w2.Code, seenPath)
	}
	if seenBase != "" {
		t.Fatalf("根路径前缀应为空，实际 %q", seenBase)
	}
}

func TestRewriteAppAssetPaths(t *testing.T) {
	src := []byte(`<!DOCTYPE html><html><head><meta charset="UTF-8"><script type="module" src="./assets/a.js"></script><link rel="stylesheet" href="./assets/a.css"></head><body><a id="boot-reload" href="./">re</a></body></html>`)

	// 根路径：原样返回。绝不能注入 <base href="/">——网关剥前缀时应用也只看到 /，
	// 一旦钉死根，`./assets/…` 就会全部 404（实测踩过的真坑）。
	if got := string(rewriteAppAssetPaths(src, "")); got != string(src) {
		t.Fatalf("根路径不应改写，实际：%s", got)
	}

	// 可判定前缀：相对引用重写成带前缀的绝对路径（深路由刷新也正确）
	sub := string(rewriteAppAssetPaths(src, "/app/stickynote"))
	for _, want := range []string{
		`src="/app/stickynote/assets/a.js"`,
		`href="/app/stickynote/assets/a.css"`,
		`href="/app/stickynote/"`,
		`<meta name="sn-app-base" content="/app/stickynote">`,
	} {
		if !strings.Contains(sub, want) {
			t.Fatalf("缺少重写结果 %q：%s", want, sub)
		}
	}
	if strings.Contains(sub, "<base ") {
		t.Fatalf("不得注入 <base>（Vite 入口 script 在 head 最前，base 来不及生效）: %s", sub)
	}
	if strings.Contains(sub, "./assets/") {
		t.Fatalf("仍有未重写的相对引用: %s", sub)
	}

	// 无 <head> 时也要完成路径重写（只是没有 meta）
	plain := []byte(`<html><body><img src="./assets/x.png"></body></html>`)
	if got := string(rewriteAppAssetPaths(plain, "/app/x")); !strings.Contains(got, `src="/app/x/assets/x.png"`) {
		t.Fatalf("无 <head> 时应仍重写路径: %s", got)
	}
}

// v2.1.3：旧多段管理路由必须在**服务端**就 302 到新单段路由。
//
// 为什么不能只靠前端 redirect：老书签若命中「内联基址脚本未执行」的极端情形，
// 前端 bundle 根本跑不起来，只有服务端跳转能保证老链接仍然可用。
// 反向验证：去掉 NoRoute 里的 legacyAdminRoute 分支，本用例立即变红。
func TestLegacyAdminRouteRedirects(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte(`<!DOCTYPE html><html><head></head><body><div id="app"></div></body></html>`), 0o600); err != nil {
		t.Fatalf("准备 index.html 失败: %v", err)
	}
	h := Setup("", dir)

	// ① 前缀可判定（不剥前缀）→ 服务端 302，跳转目标带挂载前缀
	cases := []struct {
		req  string
		want string
	}{
		{"/app/stickynote/admin/users", "/app/stickynote/admin-users"},
		{"/app/stickynote/admin/logs?page=2", "/app/stickynote/admin-logs?page=2"},
		{"/app/stickynote/admin/configs", "/app/stickynote/admin-configs"},
		{"/app/stickynote/admin/limits", "/app/stickynote/admin-limits"},
		{"/app/stickynote/admin/backup/", "/app/stickynote/admin-backup"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.req, nil))
		if w.Code != http.StatusFound {
			t.Fatalf("%s：status=%d，want 302", c.req, w.Code)
		}
		if got := w.Header().Get("Location"); got != c.want {
			t.Fatalf("%s：Location=%q，want %q", c.req, got, c.want)
		}
	}

	// ② 前缀**不可判定**（网关剥前缀）→ 绝不能在服务端跳！
	// 此时 Location 只能是根相对的 /admin-users，会把用户送出挂载点到宿主根
	// （飞牛主站 404），比不跳更糟。这类请求必须原样回 SPA 首页，由前端 redirect
	// 在挂载点内完成跳转（探针实测：剥前缀下资源解析正确）。
	//
	// 反向验证：把 NoRoute 里的 `prefix != ""` 条件去掉，本段立刻变成 302 ⇒ 判红。
	for _, p := range []string{"/admin/users", "/admin/users?probe=1", "/admin/backup/"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s：status=%d，want 200（剥前缀时不得服务端跳转）", p, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "" {
			t.Fatalf("%s：不应带 Location（实际 %q）", p, loc)
		}
	}

	// 新单段路由不得被重定向逻辑误伤：应直接吐出 SPA 首页（200）
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin-users", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/admin-users 应为 200，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `id="app"`) {
		t.Fatalf("/admin-users 未回退到 SPA 首页：%s", w.Body.String()[:120])
	}
}
