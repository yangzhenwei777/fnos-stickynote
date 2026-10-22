package main

// spa_fallback_test.go —— 桌面资源服务器 SPA 兜底的反向断言（v21.141 修正版）。
//
// ⚠️ 本测试锁定的是一次**真实回归**：v21.140 的实现在深链接未命中时直接返回 index.html，
// 而 Wails **只在路径以 "/" 或 "/index.html" 结尾时**才注入 runtime（`window.go` 桥的唯一来源）
// → 页面渲染正常但没有桥 → 所有绑定调用报「桌面桥不可用」→ 退出登录后**无法再登录**。
//
// 因此这里必须同时钉住三件事，缺一不可：
//   ① 无扩展名深链接 → **302 到 `/?_spa=<原路径>`**（根路径必然被 Wails 注入，桥才在）
//   ② 带扩展名的缺失资源 → 仍 404（回 HTML 会让浏览器把 HTML 当 JS 执行，v21.24 同源教训）
//   ③ 非 GET/HEAD → 405（不把打错方法的请求伪装成成功）

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPAFallbackRedirectsDeepLinksToInjectedRoot(t *testing.T) {
	h := desktopSPAFallback{}

	cases := []struct{ path, wantLoc string }{
		{"/profile", "/?_spa=%2Fprofile"},
		{"/login", "/?_spa=%2Flogin"},
		{"/notes", "/?_spa=%2Fnotes"},
		{"/admin/backup", "/?_spa=%2Fadmin%2Fbackup"},
		{"/", "/"}, // 根路径本身即注入目标，不需要 query
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusFound {
			t.Errorf("GET %s: code=%d want 302（必须 302 到被注入的根路径，不能直接吐 HTML）", c.path, w.Code)
			continue
		}
		if got := w.Header().Get("Location"); got != c.wantLoc {
			t.Errorf("GET %s: Location=%q want %q", c.path, got, c.wantLoc)
		}
		// 回归断言：绝不能返回 HTML body（那意味着绕过了 Wails 的 runtime 注入）
		if strings.Contains(w.Body.String(), "<html") || strings.Contains(w.Body.String(), "<!DOCTYPE") {
			t.Errorf("GET %s: 响应体含 HTML —— 这正是「桌面桥不可用」的成因（绕过了 runtime 注入）", c.path)
		}
	}
}

func TestSPAFallbackRejectsMissingAssetsWithExtension(t *testing.T) {
	h := desktopSPAFallback{}

	// 带扩展名 = 静态资源引用缺失：必须 404，绝不能回 HTML/跳转
	for _, p := range []string{"/assets/missing.js", "/assets/app.css", "/uploads/gone.png", "/favicon.ico"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s: code=%d want 404（带扩展名的缺失资源不得回 HTML/跳转）", p, w.Code)
		}
	}
}

func TestSPAFallbackRejectsNonGet(t *testing.T) {
	h := desktopSPAFallback{}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/profile", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /profile: code=%d want 405", m, w.Code)
		}
	}
}

func TestSPAFallbackHeadHasNoBody(t *testing.T) {
	h := desktopSPAFallback{}
	req := httptest.NewRequest(http.MethodHead, "/profile", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("HEAD /profile: code=%d want 302", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD 不应带 body，实际 %d 字节", w.Body.Len())
	}
}

// waitFirstRound 在调度器未启动时必须**快速返回 false + 可上屏说明**，不得 panic、不得空转。
func TestWaitFirstRoundWithoutScheduler(t *testing.T) {
	saved := syncScheduler
	syncScheduler = nil
	defer func() { syncScheduler = saved }()

	done, note := waitFirstRound(0, 0)
	if done {
		t.Fatal("调度器未启动时不应报告已完成")
	}
	if strings.TrimSpace(note) == "" {
		t.Fatal("未完成时必须给出可上屏的说明（不能静默）")
	}
}

func TestSchedulerRoundsWithoutScheduler(t *testing.T) {
	saved := syncScheduler
	syncScheduler = nil
	defer func() { syncScheduler = saved }()
	if got := schedulerRounds(); got != -1 {
		t.Fatalf("schedulerRounds()=%d want -1（未启动哨兵值）", got)
	}
}
