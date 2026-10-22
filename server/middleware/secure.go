package middleware

import "github.com/gin-gonic/gin"

// BootFallbackHash 是 web/index.html 里 #sn-boot-inline 内联脚本的 CSP sha256
// （该脚本同时承担「挂载基址自举」与「启动兜底」两件事）。
//
// v2.1.2 移动端启动卡死根治：bundle 内的兜底在「bundle 自己没被加载」时等于零防护，
// 所以启动兜底必须内联。内联脚本受 script-src 约束，故用 hash 白名单精确放行
// （不用 'unsafe-inline'，避免把整个 script-src 放开）。
//
// ⚠ 改 web/index.html 的内联脚本内容后必须同步本常量与 index.html 的 meta；
// TestBootFallbackHashMatchesInlineScript 会直接判红，防静默失效。
const BootFallbackHash = "sha256-C2qFUGeWuqS80N1g2xmQsO6p70uoGAUoBv75e9IeVTo="

// SecureHeaders 写入基础安全响应头。
//
// v2.1.2（移动端启动卡死根治配套）：**移除 frame-ancestors 'self'**。
// 原因：web/index.html 里明确写着「刻意不包含 frame-ancestors / X-Frame-Options：
// fnOS 桌面通过 iframe 嵌入应用，设置后桌面图标将无法打开」，v20.8 审计却在响应头
// 单方面加了 frame-ancestors 'self'，两者口径冲突。
// 而 'self' 只在**同源**时放行：`type: url` + `port` 直连是跨端口
// （5666 桌面 vs 13389 应用），不同源 ⇒ 经 iframe 包装的客户端会被直接拦截。
// 点击劫持防护由 CSP 其余指令 + Referrer-Policy: no-referrer + X-Content-Type-Options 承担；
// 如需恢复 frame-ancestors，必须先确认所有加载链路（桌面 Web / 手机 App / 远程访问）都同源。
//
// script-src 追加启动兜底内联脚本的 sha256（见 BootFallbackHash）。
// v21.90 P2（N-09 审计核对）：object-src 'none'——禁 <object>/<embed>/<applet>。
func SecureHeaders() gin.HandlerFunc {
	csp := "default-src 'self'; " +
		"script-src 'self' '" + BootFallbackHash + "'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'"
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		c.Next()
	}
}
