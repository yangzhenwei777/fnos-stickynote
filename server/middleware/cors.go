package middleware

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"stickynote/logger"
	"stickynote/model"
)

// CORS（v18 收紧）：前后端本就同源部署（SPA 由同一进程服务），
// 默认不放行任何跨域来源——gin-contrib/cors 对"Origin 与 Host 同源"
// 的请求自动视为非 CORS 请求直接放行；其余跨域来源预检 403、
// 简单请求不下发 ACAO（浏览器自然拦截读取）。
// 特殊场景（跨端调试、反代改写 Host 等）在管理后台配置 cors_origins：
// 逗号分隔的来源白名单；填 * 仅记录告警，不再全放行。
//
// v21.52 P10 审计修复：AllowCredentials:true 与通配来源 * 是危险组合——
// 虽 gin-contrib/cors 反射具体 Origin 而不回 "*"，但语义等价于"任意来源+凭据"，
// 跨域访问边界过大。* 配置只告警不生效，必须显式列出来源白名单。
var corsCache struct {
	mu      sync.RWMutex
	expires time.Time
	origins map[string]bool
	all     bool
}

func allowedCorsOrigins() (map[string]bool, bool) {
	corsCache.mu.RLock()
	if time.Now().Before(corsCache.expires) {
		o, a := corsCache.origins, corsCache.all
		corsCache.mu.RUnlock()
		return o, a
	}
	corsCache.mu.RUnlock()

	corsCache.mu.Lock()
	defer corsCache.mu.Unlock()
	if time.Now().Before(corsCache.expires) { // double-check
		return corsCache.origins, corsCache.all
	}
	origins := map[string]bool{}
	all := false
	if v, err := model.GetConfig("cors_origins"); err == nil {
		for _, o := range strings.Split(v, ",") {
			o = strings.TrimSpace(o)
			if o == "*" {
				// P10：通配来源 + 凭据非法，* 只记录告警不生效
				all = true
			} else if o != "" {
				origins[strings.ToLower(o)] = true
			}
		}
	}
	corsCache.origins, corsCache.all = origins, all
	corsCache.expires = time.Now().Add(5 * time.Second)
	return origins, all
}

// originsLoader 白名单来源（可注入：测试替换，默认读 DB 配置）。
var originsLoader = allowedCorsOrigins

func CORS() gin.HandlerFunc {
	cc := cors.New(cors.Config{
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
		// 不设 AllowOrigins（禁用 *）；白名单完全由后台配置驱动。
		// 命中时 gin-contrib/cors 反射具体 Origin（绝不回 *）。
		AllowOriginFunc: func(origin string) bool {
			origins, all := originsLoader()
			if all {
				// P10：* 配置不再放行任意来源（防"任意来源+凭据"），仅告警提示
				return false
			}
			return origins[strings.ToLower(origin)]
		},
	})
	return func(c *gin.Context) {
		// v2.1.2：**静态资源与页面不走 CORS**——Vite 产物带 crossorigin Origin 头，
		// 被 CORS 拦成 403 会让 JS 全挂、页面永久卡启动占位。静态资源同源部署、
		// 无凭据语义，不受跨域策略约束。
		p := c.Request.URL.Path
		if !strings.HasPrefix(p, "/api") {
			c.Next()
			return
		}

		// v2.1.5（安卓登录 403 二轮根治 · 方案 A+C）：**服务端不再 403 拒绝跨站 Origin**。
		//
		// 2.1.2/2.1.4 两轮把「同源判定」不断放宽（剥端口容忍 → Sec-Fetch-Site 信任），
		// 但安卓实测仍 403——WebView 请求头形态不可控（老内核/X5/App 内部代理），
		// 任何基于「请求头判定同源」的方案都有覆盖不到的形态。
		// 根因是 gin-contrib/cors 对**不允许的 Origin 一律 AbortWithStatus(403)**——
		// 这是超出 CORS 标准的过度防御：CORS 标准模型里跨站读取本就由**浏览器**拦
		// （服务端不下发 ACAO 即可），服务端主动 403 反而把"服务端判定不了的同源请求"
		// （Host 被网关/中继改写）一并误杀。
		//
		// 新口径：**凭据边界才是真正的防线，服务端不再替浏览器做跨域裁决**——
		//   ① 白名单命中（跨端调试场景，且未配 *）→ 仍走 cors 中间件反射 ACAO，行为不变；
		//   ② 其余一律直接放行处理、不下发 ACAO：
		//      - 同源请求（安卓/桌面/任何网关形态，页面与 API 同源）→ 浏览器不做 CORS
		//        校验，畅通；
		//      - 跨站请求 → 无 ACAO，浏览器拦读取，数据不泄露；写请求虽到达业务层，
		//        但 SameSite=Lax 保证 cookie 不随跨站发送、Bearer 跨站拿不到 ⇒
		//        需凭据的接口全部 401。限流 / loginGuard / 白名单语义全部保留。
		// P10 口径不变：* 配置只告警不生效（不反射 ACAO）。
		origin := c.GetHeader("Origin")
		if origin != "" {
			origins, all := originsLoader()
			if !all && origins[strings.ToLower(origin)] {
				cc(c)
				return
			}
		}

		// 方案 B 诊断：Origin host 与 Host host 失配时记一条画像（5s 节流防刷屏）。
		// 用途：安卓中继形态的实锤取证——验证「WebView 请求头形态」推断，
		// 并区分 403 来自应用层（本中间件已放行，不可能再发）还是网关层。
		if origin != "" && diagnoseAllow() {
			if u, err := url.Parse(origin); err == nil && u.Host != "" &&
				!strings.EqualFold(u.Hostname(), hostnameOnly(c.Request.Host)) {
				logger.InfoS("cors-origin-mismatch",
					"path", p,
					"origin", origin,
					"host", c.Request.Host,
					"referer", c.GetHeader("Referer"),
					"sec_fetch_site", c.GetHeader("Sec-Fetch-Site"),
					"ua", truncateUA(c.GetHeader("User-Agent")))
			}
		}
		c.Next()
	}
}

// 诊断日志 5 秒节流：diagnoseAllow 允许记录时返回 true 并刷新静默窗。
var (
	diagnoseMu      sync.Mutex
	diagnoseMutedTo time.Time
)

func diagnoseAllow() bool {
	diagnoseMu.Lock()
	defer diagnoseMu.Unlock()
	now := time.Now()
	if now.Before(diagnoseMutedTo) {
		return false
	}
	diagnoseMutedTo = now.Add(5 * time.Second)
	return true
}

// truncateUA 截短 User-Agent，避免日志超长。
func truncateUA(ua string) string {
	if len(ua) > 80 {
		return ua[:80]
	}
	return ua
}

// hostnameOnly 取 Host 的 hostname 部分（去掉端口）。
func hostnameOnly(host string) string {
	if host == "" {
		return ""
	}
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}
	return host
}
