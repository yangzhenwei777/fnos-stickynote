package handler

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"stickynote/auth"
	"stickynote/logger"
	"stickynote/model"
)

type registerRequest struct {
	Username         string `json:"username" binding:"required,min=3,max=50"`
	Password         string `json:"password" binding:"required,min=6"` // v21.96：12 → 6（见 minPasswordLen）
	SecurityQuestion string `json:"security_question"`
	SecurityAnswer   string `json:"security_answer"`
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Remember bool   `json:"remember"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=6"` // v21.96：12 → 6（见 minPasswordLen）
}

type updateSecurityQuestionRequest struct {
	// v21.90 P0（H-02）：修改安全问题必须验证当前密码——
	// 否则窃取的 access token 可改安全问题→走找回密码重置密码，实现持久账户接管。
	CurrentPassword  string `json:"current_password" binding:"required"`
	SecurityQuestion string `json:"security_question"`
	SecurityAnswer   string `json:"security_answer"`
}

// minSecurityAnswerLen 安全答案最短长度（v21.90 P2：低熵答案是找回密码 weakest link；
// v21.95 B-6 审计 P2 提升至 8，与密码强度对齐，提高找回密码答案熵值）。
const minSecurityAnswerLen = 8

// GetSetupStatus 返回系统是否需要初始化（无用户 = 需要设置管理员）。
//
// v21.96 附注：此处原先挂着一段 validatePasswordStrength 的注释（「≥12 位 + 四类字符至少三类」），
// 而函数本身与该校验毫无关系——典型的注释漂移，会让后来者误以为初始化流程包含密码强度校验。
// 强度函数已随本次调整迁至 auth_utils.go 并改名，注释同步归位。
func GetSetupStatus(c *gin.Context) {
	count, err := model.CountUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取用户状态失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"needs_setup": count == 0})
}

func Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		bindError(c, err)
		return
	}

	// v21.96：只卡最小长度 6 位（minPasswordLen），不再强制字符复杂度。
	// 弱密码照常放行，强度分级随响应回传，由前端提示用户自行决定是否更换。
	if err := validatePasswordLength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 防爆破（v18.4）：与登录/找回密码共用同一 guard。注册此前无任何频次限制，
	// 可批量注册刷库，也可借 409 差异逐个枚举已占用用户名。
	if locked, until := guard.locked(req.Username, c.ClientIP()); locked {
		logger.AuditEvent("register", req.Username, c.ClientIP(), "blocked", "locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}

	count, _ := model.CountUsers()

	if count > 0 {
		publicCfg, _ := model.GetPublicConfigs()
		if publicCfg["allow_register"] != "true" {
			c.JSON(http.StatusForbidden, gin.H{"error": "管理员已关闭注册"})
			return
		}
	}

	// 事务化注册（v18.7）：count 判定与 insert 原子完成，堵双 admin 竞态窗口
	user, err := model.RegisterUser(req.Username, req.Password, req.SecurityQuestion, req.SecurityAnswer)
	if err != nil {
		// 用户名冲突计入失败。统一返回 400 + 模糊话术，配合 rate limit
		// 消除"注册成功 201 / 用户已存在 409"的枚举预言机。
		guard.fail(req.Username, c.ClientIP())
		logger.AuditEvent("register", req.Username, c.ClientIP(), "failed", "username conflict")
		time.Sleep(200 * time.Millisecond) // 固定延时消除时序侧信道
		// v21.107 P2-6：在保持不确定表述（防枚举语义不变）的前提下给用户可操作线索。
		c.JSON(http.StatusBadRequest, gin.H{"error": "注册失败：用户名可能已被占用或服务繁忙，请更换用户名或稍后再试；若已拥有账号请直接登录"})
		return
	}

	guard.success(req.Username, c.ClientIP())
	strength := passwordStrengthLevel(req.Password) // v21.96：弱密码已放行，此处仅用于提示与审计留痕
	logger.AuditEvent("register", req.Username, c.ClientIP(), "success", "role: "+user.Role+" pwd_strength: "+strength)
	access, refresh, err := issueTokenPair(c, user, false)
	if err != nil { // v21.67 P2：token 签发失败显式上报，不再空 token 下发
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发登录凭证失败"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"user":              user,
		"access_token":      access,
		"refresh_token":     refresh,
		"password_strength": strength,
	})
}

func Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入用户名和密码"})
		return
	}

	// 登录防爆破：锁定中直接拒绝（即使密码正确），话术统一不暴露细节
	if locked, until := guard.locked(req.Username, c.ClientIP()); locked {
		logger.AuditEvent("login", req.Username, c.ClientIP(), "blocked", "locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}

	user, err := model.AuthenticateUser(req.Username, req.Password)
	if err != nil {
		guard.fail(req.Username, c.ClientIP())
		// v21.58 P3：对外统一文案（防账户状态枚举）；审计日志保留真实原因（account disabled）
		if errors.Is(err, model.ErrAccountDisabled) {
			logger.AuditEvent("login", req.Username, c.ClientIP(), "failed", "account disabled")
		} else {
			logger.AuditEvent("login", req.Username, c.ClientIP(), "failed", "bad credentials")
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	guard.success(req.Username, c.ClientIP())
	logger.AuditEvent("login", req.Username, c.ClientIP(), "success", "")

	// v20.2 TOTP: 若用户开启两步验证且无有效信任设备，返回 totp_required
	if user.TotpEnabled {
		if tc, _ := c.Cookie("trusted_device"); tc != "" {
			// v21.52 P6：校验 type=trusted_device 且 ver 与当前 token_version 一致
			if claims, err := parseTrustedDeviceToken(tc); err == nil &&
				claims["user_id"] == float64(user.ID) {
				if ver, ok := claims["ver"].(float64); !ok || int64(ver) != user.TokenVersion {
					// 版本不匹配 = 已吊销，走 TOTP 流程
					totpToken, err := generateTOTPToken(user.ID)
					if err != nil {
						logger.Error("login: generate totp token: %v", err)
						c.JSON(http.StatusInternalServerError, gin.H{"error": "签发临时凭证失败"})
						return
					}
					c.JSON(http.StatusOK, gin.H{
						"totp_required": true,
						"totp_token":    totpToken,
						"user":          gin.H{"id": user.ID, "username": user.Username},
					})
					return
				}
				access, refresh, err := issueTokenPair(c, user, req.Remember)
				if err != nil { // v21.67 P2：token 签发失败显式上报
					c.JSON(http.StatusInternalServerError, gin.H{"error": "签发登录凭证失败"})
					return
				}
				c.JSON(http.StatusOK, gin.H{"user": user, "access_token": access, "refresh_token": refresh})
				return
			}
		}
		// 需要 TOTP：签发 2 分钟临时 totp_token
		totpToken, err := generateTOTPToken(user.ID)
		if err != nil {
			logger.Error("login: generate totp token: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "签发临时凭证失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"totp_required": true,
			"totp_token":    totpToken,
			"user":          gin.H{"id": user.ID, "username": user.Username},
		})
		return
	}

	access, refresh, err := issueTokenPair(c, user, req.Remember)
	if err != nil { // v21.67 P2：token 签发失败显式上报
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发登录凭证失败"})
		return
	}
	resp := gin.H{
		"user":          user,
		"access_token":  access,
		"refresh_token": refresh,
	}
	// COOKIE_SECURE 被显式强制开启而当前是 HTTP 访问：cookie 会被浏览器静默丢弃，
	// 必须大声告诉管理员，而不是表现为"登录成功却马上掉线"。
	if os.Getenv("COOKIE_SECURE") == "true" && !isHTTPS(c) {
		resp["warning"] = "服务端已开启 COOKIE_SECURE，但当前为 HTTP 访问：浏览器将丢弃登录 Cookie，无法保持登录。请改用 HTTPS 访问，或联系管理员关闭该配置。"
	}
	c.JSON(http.StatusOK, resp)
}

func Logout(c *gin.Context) {
	// 吊销该用户全部已签发 refresh token（v18.2）：
	// fnOS 反代/iframe 环境下 Set-Cookie 清除未必生效（Path 改写、第三方 cookie 策略），
	// 仅靠清 cookie 无法保证登出——残留 refresh cookie 会被前端 tryRefresh 静默复活会话。
	// 递增 token_version 后旧 refresh 一律 401，服务端层面根治"退出后自动登录"。
	// 代价：该账号所有设备会话同时失效（退出=全端退出），个人 NAS 场景可接受。
	if userID, exists := c.Get("userID"); exists {
		if id, ok := userID.(int64); ok {
			if err := model.BumpTokenVersion(id); err != nil {
				// 吊销失败不阻断登出（cookie 仍清、前端仍清本地状态），但记录日志
				logger.Error("Logout: failed to bump token_version for user %d: %v", id, err)
			}
		}
	}
	clearAuthCookies(c)
	c.JSON(http.StatusOK, gin.H{"message": "已登出"})
}

// Refresh 用 refresh token 换取新的 access token。
// refresh token 的 token_version 必须与数据库一致（改密/重置后旧 refresh 全部失效）。
// token 读取：请求体优先（cookie 不可用环境走头通道），cookie 兜底。
func Refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	refreshStr := ""
	if err := c.ShouldBindJSON(&req); err == nil && req.RefreshToken != "" {
		refreshStr = req.RefreshToken
	} else if cookie, err := c.Cookie(refreshCookieName); err == nil {
		refreshStr = cookie
	}
	if refreshStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "无会话凭证"})
		return
	}

	claims, err := auth.ParseToken(refreshStr)
	if err != nil || claims.Type != "refresh" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "会话凭证无效"})
		return
	}

	user, err := model.GetUserByID(claims.UserID)
	if err != nil || user.TokenVersion != claims.TokenVersion {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "会话已失效，请重新登录"})
		return
	}

	// v21.94 P3-7：多设备 refresh 会话驱动的一次性轮转。
	// v21.93 的 users.refresh_jti 单值模型每账号同一时间仅一个有效 refresh，
	// 多设备互相顶掉。现改为 refresh_sessions 表：
	//   - 一次登录 = 一个 family；轮转在 family 内延续（旧 jti 吊销、同 family 签新）
	//   - 重放（已吊销 jti 再现）→ 撤销整个 family（只波及该设备，不误伤其他设备）
	//   - 查无会话（存量 v21.93 单 jti 用户）→ 迁移进会话表后继续，不强制重登
	refreshTTL := refreshSessionTTL
	if claims.ExpiresAt != nil {
		remain := time.Until(claims.ExpiresAt.Time)
		if remain > time.Minute {
			refreshTTL = remain
		}
	}

	sess, serr := model.FindRefreshSessionByJTI(claims.ID)
	var sessFamily string
	switch {
	case serr == nil:
		sessFamily = sess.FamilyID
		if sess.RevokedAt != nil {
			// ⭐ v21.217（生产实证·桌面端弱网反复掉登录的**真正根治**）：重放宽限期。
			//
			// 定罪（211b~211f 五轮实机日志 + 联网核实）：refresh 一次一换在**弱网**
			// 下存在无法从客户端消除的窗口——服务端已轮转成功（旧票吊销、新票签发），
			// 但响应因网络未能到达客户端；客户端重试/重开即用已吊销旧票 → 走到这里
			// → 原实现**撤销整个 family** → 用户被迫重新登录 → 死循环。
			// Okta 官方文档对该场景的原话："new tokens issued by Okta might not reach
			// the client app… Okta offers a grace period — the previous token remains
			// valid for a configured amount of time to allow clients to get the new
			// token."；RFC 9700 同款建议（reuse grace period 防误杀真实用户），
			// Auth0 默认 30s。桌面端在 GFW/DPI 环境下该窗口出现频率极高。
			//
			// 修法：已吊销 jti 在 **60s 宽限期**内重现且 family 仍活跃（= 先前轮转
			// 确实成功过、新票只是没送达）→ **不撤族**，续签同族新票正常返回。
			// family 已全撤（真重放攻击/登出/改密）→ 维持撤族 + 401，安全语义不变。
			// 安全权衡：重放攻击窗从「永久」缩到「吊销后 60s 内且族未死」，与
			// Auth0/Okta 生产默认同级。
			if time.Since(*sess.RevokedAt) <= refreshReuseGrace {
				if alive, aerr := model.HasActiveRefreshSession(sessFamily); aerr == nil && alive {
					access, gerr := auth.GenerateAccessToken(user.ID, user.Username, user.Role, user.TokenVersion)
					if gerr != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "签发会话失败"})
						return
					}
					newRefresh, newJTI, gerr := auth.GenerateRefreshToken(user.ID, user.TokenVersion, refreshTTL, claims.Remember)
					if gerr != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "签发会话失败"})
						return
					}
					if gerr := model.CreateRefreshSession(user.ID, sessFamily, newJTI, sess.DeviceName, time.Now().Add(refreshTTL)); gerr != nil {
						logger.Error("Refresh: grace re-issue for family %s: %v", sessFamily, gerr)
						c.JSON(http.StatusInternalServerError, gin.H{"error": "签发会话失败"})
						return
					}
					logger.AuditEvent("refresh", user.Username, c.ClientIP(), "grace-reissued",
						fmt.Sprintf("reuse within grace window (family=%s, old_jti=%s); re-issued new jti=%s", sess.FamilyID, claims.ID, newJTI))
					setRefreshCookie(c, newRefresh, claims.Remember)
					setAccessCookie(c, access)
					c.JSON(http.StatusOK, gin.H{"user": user, "access_token": access, "refresh_token": newRefresh})
					return
				}
			}
			// 重放检测：宽限期外（或 family 已死）→ 撤销整个 family + 401
			// v21.97（审计 L-03）：审计日志补 family_id，便于溯源被吊销的会话族
			logger.AuditEvent("refresh", user.Username, c.ClientIP(), "blocked", fmt.Sprintf("refresh token replay detected (family=%s, jti=%s)", sess.FamilyID, claims.ID))
			_ = model.RevokeRefreshFamily(sess.FamilyID)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "会话已失效，请重新登录"})
			return
		}
		if time.Now().After(sess.ExpiresAt) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "会话已过期，请重新登录"})
			return
		}
	case errors.Is(serr, model.ErrSessionNotFound):
		// 存量 v21.93 单 jti 用户：users.refresh_jti 匹配则迁移（以该 jti 为新 family），否则拒绝
		if user.RefreshJTI == "" || user.RefreshJTI != claims.ID {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "会话已失效，请重新登录"})
			return
		}
		sessFamily = claims.ID
		if err := model.CreateRefreshSession(user.ID, sessFamily, claims.ID, "", time.Now().Add(refreshTTL)); err != nil {
			logger.Error("Refresh: migrate legacy session for user %d: %v", user.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "签发会话失败"})
			return
		}
	default:
		logger.Error("Refresh: find refresh session for user %d: %v", user.ID, serr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发会话失败"})
		return
	}

	access, err := auth.GenerateAccessToken(user.ID, user.Username, user.Role, user.TokenVersion)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发会话失败"})
		return
	}
	// v21.90 P1（N-06/M-03 审计核对）：refresh token 轮换——每次 refresh 签发新 refresh，
	// cookie 与响应体双通道同步更新，前端本地存储随之替换。
	// v21.108 P1-9：新票继承旧票 claims.Remember（「记住我」语义全程一致），
	// 不再由 cookie 持久性反推 TTL 阈值。
	newRefresh, newJTI, err := auth.GenerateRefreshToken(user.ID, user.TokenVersion, refreshTTL, claims.Remember)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发会话失败"})
		return
	}
	// R-1 修复（v1.0.134 审计缺陷）：轮转改为「单事务 + CAS + fail-closed」原子操作。
	// 旧实现用 5 次独立 DB 调用、且把吊销错误 `_ =` 丢弃 → 并发持同一旧 token 可双花、
	// 旧票吊销失败被静默忽略。现改为 model.RotateRefreshSessionTx 在一个事务内完成
	// ① CAS 吊销旧 jti ② 同 family 签新会话 ③ touch 旧票 ④ 更新 users.refresh_jti，
	// 任一写失败整体回滚（fail-closed：宁可本次换票失败，不留下"旧票仍可用"的中间态）。
	rotated, err := model.RotateRefreshSessionTx(user.ID, sessFamily, claims.ID, newJTI, "", time.Now().Add(refreshTTL))
	if err != nil {
		logger.Error("Refresh: rotate refresh session for user %d: %v", user.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发会话失败"})
		return
	}
	if !rotated {
		// CAS 未命中：旧 jti 已被吊销（重放或并发抢用）。撤销整个 family 并返 401，
		// 与上方重放检测同源，保证一次性 token 语义不被双花击穿。
		// v21.97（审计 L-03）：审计日志补 family_id，便于溯源
		logger.AuditEvent("refresh", user.Username, c.ClientIP(), "blocked", fmt.Sprintf("rotated=false: old jti already revoked (replay/concurrent) family=%s", sessFamily))
		_ = model.RevokeRefreshFamily(sessFamily)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "会话已失效，请重新登录"})
		return
	}
	setRefreshCookie(c, newRefresh, claims.Remember) // v21.108 P1-9：持久性=登录时 remember 选择（经 claims 继承），TTL 猜测废除
	setAccessCookie(c, access)
	c.JSON(http.StatusOK, gin.H{"user": user, "access_token": access, "refresh_token": newRefresh})
}

const (
	accessCookieName  = "access_token"
	refreshCookieName = "refresh_token"

	// accessCookieMaxAge 与 access token JWT 有效期对齐（2 小时）
	accessCookieMaxAge = 2 * 3600
	// refreshRememberMaxAge 勾选"记住我"时的 refresh 有效期（30 天）
	refreshRememberMaxAge = 30 * 24 * 3600
	// refreshSessionTTL 未勾选"记住我"时 refresh token 的 JWT 有效期（7 天）；
	// cookie 为会话级（浏览器关闭即弃），JWT 有效期只是兜底
	refreshSessionTTL  = 7 * 24 * time.Hour
	refreshRememberTTL = 30 * 24 * time.Hour
)

// ⭐ v21.217：rotation 重放宽限期（RFC 9700 / Okta / Auth0 同款）。
// 已吊销 jti 在此窗口内重现且 family 仍活跃 ⇒ 判为「服务端已轮转但响应
// 未达客户端」（弱网常态），续签同族新票而非撤族。60s 对齐 Auth0 默认档。
// ⚠ var 非 const：rotation 既有钉测需置 0 恢复严格语义（一次性 token 的
// "轮转后旧票必 401"契约仍被钉住，只是限定在宽限期外成立）。
var refreshReuseGrace = 60 * time.Second

// cookieSecure 决定是否给 cookie 加 Secure 标志（v18 自动模式）：
//   - 环境变量 COOKIE_SECURE 显式 true/false 时以环境变量为准（管理员强制）；
//   - 未设置时跟随请求协议（HTTPS 才加）——局域网 HTTP 用户零配置不踩坑，
//     HTTPS 反代用户也无需手动配置。
