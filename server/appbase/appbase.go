// Package appbase 处理应用被挂载在**子路径**（而非站点根）时的路径换算。
//
// 背景（v2.1.2 移动端启动卡死根治）：
// fnOS 有两条加载链路——Web 桌面用 `type: url` + `port` 直连站点根，手机 App /
// 远程访问则经网关以 `/app/<appname>/…` 子路径加载同一个应用。2.1.1 及之前
// 全链路假设「永远在根」，导致子路径下静态资源、API、cookie 作用域全部错位。
//
// 本包只放**纯函数 + context 载体**，不依赖 gin / handler / router，避免循环引用。
package appbase

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// WithPrefix 把本次请求推算出的挂载前缀写进 context（不带尾斜杠，站点根为空串）。
func WithPrefix(ctx context.Context, prefix string) context.Context {
	if prefix == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, prefix)
}

// FromContext 取出挂载前缀；站点根返回空串。
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}

// Prefix 推算请求路径对应的挂载前缀（不带尾斜杠）。站点根返回 ""。
//
// 判定顺序（由强到弱）：
//  1. X-Forwarded-Prefix 显式声明（运维可控，最可靠）
//  2. 从构建产物里的固定资源锚点反推（/assets/、/manifest.webmanifest 等）
//  3. fnOS 网关固定形态 /app/<appname>[/…]
//
// 都判不出来就返回 ""——**宁可当站点根，也不猜一个可能错的前缀**。
func Prefix(path string, h http.Header) string {
	if h != nil {
		if v := strings.TrimSpace(h.Get("X-Forwarded-Prefix")); v != "" {
			return Normalize(v)
		}
	}
	for _, anchor := range []string{
		"/assets/", "/uploads/", "/manifest.webmanifest",
		"/registerSW.js", "/favicon.svg", "/favicon.png",
	} {
		if i := strings.Index(path, anchor); i > 0 {
			if n := Normalize(path[:i]); n != "" {
				return n
			}
		}
	}
	if rest, ok := strings.CutPrefix(path, "/app/"); ok {
		if j := strings.IndexByte(rest, '/'); j > 0 {
			return Normalize("/app/" + rest[:j])
		}
	}
	return ""
}

// Normalize 校验并规范化前缀；非法返回 ""。
//
// 白名单：最多 3 段，每段 [A-Za-z0-9._-]{1,64}，拒绝空段 / "." / ".."。
// 这样前缀可以安全地拼进 URL、cookie Path 与 HTML，无注入面、无目录穿越。
func Normalize(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "/" {
		return ""
	}
	if !strings.HasPrefix(v, "/") {
		v = "/" + v
	}
	v = strings.TrimRight(v, "/")
	segs := strings.Split(strings.TrimPrefix(v, "/"), "/")
	if len(segs) == 0 || len(segs) > 3 {
		return ""
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || len(s) > 64 {
			return ""
		}
		for _, r := range s {
			ok := r == '-' || r == '_' || r == '.' ||
				(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				return ""
			}
		}
	}
	return "/" + strings.Join(segs, "/")
}

// Strip 把请求路径还原成应用内路径（段边界严格匹配）。
func Strip(path, prefix string) string {
	if prefix == "" || !strings.HasPrefix(path, prefix) {
		return path
	}
	rest := path[len(prefix):]
	if rest == "" {
		return "/"
	}
	if !strings.HasPrefix(rest, "/") {
		// 段边界不匹配（prefix=/app/x 而路径是 /app/xy）→ 不是本应用的挂载点
		return path
	}
	return rest
}

// CookiePath 把 cookie 的 Path 换算到挂载点下。
//
// 关键：refresh_token 的 scope 是 `/api/auth`，子路径下必须变成
// `/app/<appname>/api/auth`——否则浏览器按 Path 前缀匹配会**完全不发送**该 cookie，
// 表现为「能登录但一段时间后静默掉线，且刷新永远 401」。
func CookiePath(prefix, base string) string {
	if prefix == "" {
		if base == "" {
			return "/"
		}
		return base
	}
	if base == "" || base == "/" {
		return prefix + "/"
	}
	return prefix + base
}
