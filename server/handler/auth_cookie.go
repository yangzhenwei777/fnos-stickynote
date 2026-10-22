package handler

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"stickynote/appbase"
	"stickynote/auth"
	"stickynote/logger"
	"stickynote/model"
)

func cookieSecure(c *gin.Context) bool {
	switch os.Getenv("COOKIE_SECURE") {
	case "true":
		return true
	case "false":
		return false
	}
	return isHTTPS(c)
}

// isHTTPS 判断当前请求是否经 HTTPS 到达（直连 TLS 或反代透传 X-Forwarded-Proto）。
func isHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

// issueTokenPair 签发 access + refresh 双 token 并下发 httpOnly cookie。
// httpOnly 使 JS 无法读取，SameSite=Lax 阻断跨站 POST 携带（CSRF 防护）。
// 同时返回两个 token 供响应体携带：cookie 不可用的环境（如部分内嵌 webview）
// 前端可改用 Authorization 头通道。
//
// P2-8 安全取舍：响应体 token 被前端存入 localStorage/sessionStorage，
// 理论上扩大 XSS 利用面（纯 httpOnly cookie 无此暴露面）。这是为兼容 fnOS
// webview 环境（cookie 不可用）做的已知取舍。
func issueTokenPair(c *gin.Context, user *model.User, remember bool) (access string, refresh string, err error) {
	refreshTTL := refreshSessionTTL
	if remember {
		refreshTTL = refreshRememberTTL
	}
	access, err = auth.GenerateAccessToken(user.ID, user.Username, user.Role, user.TokenVersion)
	if err != nil {
		return "", "", err
	}
	// v21.108 P1-9：remember 编入 refresh token claims，供轮转时继承持久性语义
	refresh, jti, err := auth.GenerateRefreshToken(user.ID, user.TokenVersion, refreshTTL, remember)
	if err != nil {
		return "", "", err
	}
	// v21.93 P1：登录即登记当前有效 jti，使一次性轮转从首次换票起就生效
	// （否则旧票会因「库中 jti 为空」被判定为存量首次换票，多活一轮）。
	// 登记失败不阻断登录（与 Logout 中 bump 失败的处理一致，可用性优先），
	// 仅降级为下次换票时补登记。
	if err := model.UpdateRefreshJTI(user.ID, jti); err != nil {
		logger.Error("issueTokenPair: persist refresh jti for user %d: %v", user.ID, err)
	}
	// v21.94 P3-7：登录即创建 refresh 会话（一次登录 = 一个 family，多设备共存）。
	// 登记失败不阻断登录（可用性优先）；该设备下次换票时若查无会话会走存量迁移路径。
	family := uuid.NewString()
	if err := model.CreateRefreshSession(user.ID, family, jti, "", time.Now().Add(refreshTTL)); err != nil {
		logger.Error("issueTokenPair: create refresh session for user %d: %v", user.ID, err)
	}
	setAccessCookie(c, access)
	setRefreshCookie(c, refresh, remember)
	return access, refresh, nil
}

func setAccessCookie(c *gin.Context, token string) {
	c.SetSameSite(http.SameSiteLaxMode)
	// v2.1.2：cookie 作用域跟随挂载点。子路径（fnOS 网关 /app/<appname>/）下必须是
	// /app/<appname>/，否则浏览器按 Path 前缀匹配不会发送；根路径下等价于 /。
	c.SetCookie(accessCookieName, token, accessCookieMaxAge,
		appbase.CookiePath(prefixOf(c), "/"), "", cookieSecure(c), true)
}

// setRefreshCookie 的 Path 限定为 /api/auth，refresh token 不会随业务请求传输，
// 缩小被窃听的暴露面；remember=false 时为会话级 cookie（浏览器关闭即失效）。
func setRefreshCookie(c *gin.Context, token string, remember bool) {
	maxAge := 0 // 会话级
	if remember {
		maxAge = refreshRememberMaxAge
	}
	c.SetSameSite(http.SameSiteLaxMode)
	// v2.1.2：refresh token 的 scope 更窄（原本 /api/auth），子路径下必须变成
	// /app/<appname>/api/auth，否则刷新请求完全不带该 cookie ⇒ 会话静默掉线。
	c.SetCookie(refreshCookieName, token, maxAge,
		appbase.CookiePath(prefixOf(c), "/api/auth"), "", cookieSecure(c), true)
}

func clearAuthCookies(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	// 清除时 Path 必须与写入一致，否则删不掉（子路径下尤其明显）。
	c.SetCookie(accessCookieName, "", -1,
		appbase.CookiePath(prefixOf(c), "/"), "", cookieSecure(c), true)
	c.SetCookie(refreshCookieName, "", -1,
		appbase.CookiePath(prefixOf(c), "/api/auth"), "", cookieSecure(c), true)
}

// prefixOf 取本次请求的挂载前缀（站点根为空串）。
// 由 router 的 appBaseHandler 在**路由匹配前**写入 context，见 server/appbase。
func prefixOf(c *gin.Context) string {
	return appbase.FromContext(c.Request.Context())
}
