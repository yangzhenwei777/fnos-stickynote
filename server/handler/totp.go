package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"stickynote/auth"
	"stickynote/crypto"
	"stickynote/database"
	"stickynote/logger"
	"stickynote/model"
)

const (
	totpIssuer       = "Sticky note" // v21.133 L-01：随应用改名 Sticky note（原「便签」）；仅影响新生成的 otpauth 标签，校验用密钥不受影响
	trustedDeviceTTL = 30 * 24 * time.Hour
	recoveryCodeLen  = 10
	recoveryCodeCnt  = 8
)

// ---- TOTP 密钥加密 ----

// totpEncryptKey 派生 TOTP 密钥加密子密钥（v21.90 P2：HKDF error 显式传播）。
func totpEncryptKey() ([]byte, error) {
	return crypto.DeriveKey(auth.GetJWTSecret(), "totp-secret-encryption")
}

func encryptTOTP(secret string) (string, error) {
	key, err := totpEncryptKey()
	if err != nil {
		return "", err
	}
	return crypto.EncryptRaw([]byte(secret), key)
}

func decryptTOTP(encoded string) (string, error) {
	key, err := totpEncryptKey()
	if err != nil {
		return "", err
	}
	b, err := crypto.DecryptRaw(encoded, key)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---- 启用 TOTP ----

func EnableTOTP(c *gin.Context) {
	userID, _ := c.Get("userID")
	u, err := model.GetUserByID(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取用户信息失败"})
		return
	}
	// v21.151：已启用时给出可行动文案（旧文案「操作无效」曾让用户以为步骤出错，
	// 实际是个人中心显示层未加载真实状态——见 TOTPStatus）。
	if u.TotpEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "两步验证已启用（如需重置请先关闭）"})
		return
	}

	// 生成密钥
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: u.Username,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成密钥失败"})
		return
	}

	encrypted, err := encryptTOTP(key.Secret())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "加密密钥失败"})
		return
	}
	// v21.58 P2 审计修复：持久化错误必须上报——
	// 否则 UI 返回 200 + secret 但库中未保存，用户以为已配置、实际未保存（认证状态不一致半状态）。
	if _, err := database.DB.Exec(
		"UPDATE users SET totp_secret = ? WHERE id = ?", encrypted, u.ID); err != nil {
		logger.Error("EnableTOTP: failed to store secret for user %d: %v", u.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存两步验证配置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"secret":      key.Secret(),
		"otpauth_url": key.URL(),
		"message":     "请用验证器 App 扫描二维码，然后输入当前验证码确认",
	})
}

// ---- 查询 TOTP 状态 ----

// TOTPStatus 返回当前用户两步验证启用状态（v21.151，生产 Bug1）。
// 背景：个人中心此前仅在本次会话内临时维护「已启用」态，页面刷新后一律回到「未启用」，
// 用户再点「开启两步验证」会被 EnableTOTP 正确拒绝（已启用）——显示层与真实态脱节。
// 本端点给出单一事实源，前端挂载时拉取。
func TOTPStatus(c *gin.Context) {
	userID, _ := c.Get("userID")
	u, err := model.GetUserByID(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取用户信息失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"totp_enabled": u.TotpEnabled})
}

// ---- 确认开启 TOTP ----

type confirmTOTPRequest struct {
	Code string `json:"code" binding:"required"`
}

func ConfirmTOTP(c *gin.Context) {
	userID, _ := c.Get("userID")
	u, err := model.GetUserByID(userID.(int64))
	if err != nil || u.TotpSecret == "" || u.TotpEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "操作无效"})
		return
	}

	var req confirmTOTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	secret, err := decryptTOTP(u.TotpSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "解密密钥失败"})
		return
	}
	if !totp.Validate(req.Code, secret) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码错误，请重试"})
		return
	}

	// v21.58 P3 审计修复：totp_enabled=1 与恢复码生成合并**同一事务**——
	// 全部成功才提交并返回恢复码；任一失败整体回滚，杜绝
	// "恢复码已生成但 totp_enabled=0" 或 "UI 显示已开启但库中未保存" 的半状态。
	tx, err := database.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "开启两步验证失败"})
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		"UPDATE users SET totp_enabled = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?", u.ID); err != nil {
		logger.Error("ConfirmTOTP: failed to enable totp for user %d: %v", u.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "开启两步验证失败"})
		return
	}

	recoveryCodes, err := insertRecoveryCodesTx(tx, u.ID)
	if err != nil {
		logger.Error("ConfirmTOTP: failed to generate recovery codes for user %d: %v", u.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成恢复码失败"})
		return
	}

	if err := tx.Commit(); err != nil {
		logger.Error("ConfirmTOTP: commit failed for user %d: %v", u.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "开启两步验证失败"})
		return
	}
	logger.AuditEvent("totp_enable", c.GetString("username"), c.ClientIP(), "success", "")
	c.JSON(http.StatusOK, gin.H{"message": "两步验证已开启", "recovery_codes": recoveryCodes})
}

// ---- 关闭 TOTP（本人） ----

func DisableTOTP(c *gin.Context) {
	userID, _ := c.Get("userID")
	u, err := model.GetUserByID(userID.(int64))
	if err != nil || !u.TotpEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "两步验证未开启"})
		return
	}

	// v21.93 P0（R-02/H-01 审计核对，v21.90 未闭环）：关闭 TOTP 必须
	//   「当前密码 + 第二因子（TOTP 码 or 恢复码）」
	// —————————————————————————————————————————————
	// v21.90 的实现把第二因子校验包在 `if req.Code != ""` 里，code 留空即整段跳过，
	// 只要当前密码正确就能关掉 MFA；而 2FA 存在的意义正是防「密码已泄露/被撞库/被钓鱼」，
	// 该实现使 2FA 在最关键的威胁模型下形同虚设。本轮修正为：
	//   1) code 必填（空 → 400，不再静默跳过）
	//   2) TOTP 码或恢复码二选一，任一通过即可（changelog 承诺的「恢复码」途径此前从未实现）
	//   3) 密钥解密失败 fail-closed（此前 serr != nil 时不校验反而放行）
	//   4) 失败计入账号全局 + IP 双维度防爆破；锁定中直接 429

	// 防爆破前置：与登录/TOTP 登录共用 loginGuard 维度，话术统一不暴露账号状态
	if locked, until := guard.accountLocked(u.Username); locked {
		logger.AuditEvent("totp_disable", u.Username, c.ClientIP(), "blocked", "account locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}
	if locked, until := guard.locked(u.Username, c.ClientIP()); locked { // v21.108 P1-11: 捕获 until 供剩余时长话术
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		Code            string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入当前密码"})
		return
	}
	username, _ := c.Get("username")
	if _, err := model.AuthenticateUser(username.(string), req.CurrentPassword); err != nil {
		logger.AuditEvent("totp_disable", username.(string), c.ClientIP(), "failed", "wrong current password")
		// v21.105 P0-3：401 → 400（业务密码错误误触发前端会话过期链路，见 ChangePassword 注释）
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前密码错误"})
		return
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入验证码或恢复码"})
		return
	}
	secret, serr := decryptTOTP(u.TotpSecret)
	if serr != nil {
		// fail-closed：密钥解不出来不允许放行关闭（v21.93 修复；此前 fail-open 直接放行）
		logger.Error("DisableTOTP: decrypt totp secret for user %d: %v", u.ID, serr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "验证配置异常，请稍后重试"})
		return
	}
	if !totp.Validate(code, secret) && !matchRecoveryCode(u.ID, code) {
		guard.fail(u.Username, c.ClientIP())
		guard.accountFail(u.Username)
		logger.AuditEvent("totp_disable", u.Username, c.ClientIP(), "failed", "invalid code or recovery code")
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码或恢复码错误"})
		return
	}
	guard.success(u.Username, c.ClientIP())
	guard.accountSuccess(u.Username)

	// v21.58 P2 审计修复：关闭 TOTP 必须递增 token_version——
	// 否则旧 30 天 trusted_device Cookie 在重新启用 TOTP 后仍被认可（认证生命周期缺口）。
	// 关闭即全局吊销该用户全部信任设备，重新启用需重新验证。
	tx, err := database.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"UPDATE users SET totp_secret = '', totp_enabled = 0, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		u.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	if _, err := tx.Exec("DELETE FROM totp_recovery WHERE user_id = ?", u.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	logger.AuditEvent("totp_disable", c.GetString("username"), c.ClientIP(), "success", "")

	// v21.105 P0-2：token_version 已递增（v21.58 安全语义保留），本会话须重新
	// 签发新版本 token 保持登录，且必须随响应体下发（仅 Set-Cookie 时前端
	// storage 旧 token 优先于 cookie，下一请求必 401 被踢回登录页）。
	resp := gin.H{"message": "两步验证已关闭"}
	if fresh, err := model.GetUserByID(u.ID); err == nil {
		if access, refresh, terr := issueTokenPair(c, fresh, false); terr != nil {
			logger.Error("disableTotp: reissue token failed: %v", terr)
		} else {
			resp["access_token"] = access
			resp["refresh_token"] = refresh
		}
	}
	c.JSON(http.StatusOK, resp)
}

// ---- 管理员关闭用户 TOTP ----

func AdminDisableTOTP(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户ID"})
		return
	}
	u, err := model.GetUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	// v21.58 P2：管理员关闭同样递增 token_version（吊销该用户全部 trusted_device）
	tx, err := database.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"UPDATE users SET totp_secret = '', totp_enabled = 0, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	if _, err := tx.Exec("DELETE FROM totp_recovery WHERE user_id = ?", id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	logger.AuditEvent("totp_admin_disable", c.GetString("username"), c.ClientIP(), "success",
		fmt.Sprintf("disabled user %s (%d)", u.Username, id))
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "关闭TOTP", strconv.FormatInt(id, 10), u.Username, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "已关闭该用户的两步验证"})
}

// ---- 登录时验证 TOTP ----

const totpTokenTTL = 2 * time.Minute

type totpVerifyRequest struct {
	TOTPToken string `json:"totp_token" binding:"required"`
	Code      string `json:"code" binding:"required"`
	Remember  bool   `json:"remember"`
}

func VerifyTOTP(c *gin.Context) {
	var req totpVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	claims, err := auth.ParseToken(req.TOTPToken)
	if err != nil || claims.Type != "totp" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证已过期，请重新登录"})
		return
	}

	u, err := model.GetUserByID(claims.UserID)
	if err != nil || !u.TotpEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "操作无效"})
		return
	}

	// 防爆破
	// v21.90 P0（N-01 审计核对）：追加账号全局维度——原实现仅 userID+IP，
	// 攻击者换 IP 可对同一账号分布式爆破 6 位 TOTP。复用 v21.52 account:* 机制双维度并行。
	if locked, until := guard.accountLocked(u.Username); locked {
		logger.AuditEvent("totp_verify", u.Username, c.ClientIP(), "blocked", "account locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("验证码尝试次数过多", until)})
		return
	}
	guardKey := fmt.Sprintf("totp_login|%d|%s", u.ID, c.ClientIP())
	if locked, until := guard.locked(guardKey, c.ClientIP()); locked { // v21.108 P1-11: 捕获 until 供剩余时长话术
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("验证码尝试次数过多", until)})
		return
	}

	secret, err := decryptTOTP(u.TotpSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "解密密钥失败"})
		return
	}
	if !totp.Validate(req.Code, secret) {
		guard.fail(guardKey, c.ClientIP())
		guard.accountFail(u.Username) // v21.90 N-01：计入账号全局维度
		// 也尝试恢复码
		if matchRecoveryCode(u.ID, req.Code) {
			guard.success(guardKey, c.ClientIP())
			guard.accountSuccess(u.Username)
			issueTOTPLoginResponse(c, u, req.Remember)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码错误"})
		return
	}

	guard.success(guardKey, c.ClientIP())
	guard.accountSuccess(u.Username)
	logger.AuditEvent("totp_verify", u.Username, c.ClientIP(), "success", "")
	issueTOTPLoginResponse(c, u, req.Remember)
}

// issueLoginResponse 签发登录 token 和信任设备 cookie。
func issueTOTPLoginResponse(c *gin.Context, u *model.User, remember bool) {
	access, refresh, err := issueTokenPair(c, u, remember)
	if err != nil { // v21.67 P2：token 签发失败显式上报
		logger.Error("issueTOTPLoginResponse: issue token pair: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发登录凭证失败"})
		return
	}
	// v21.66 P1-1：trusted_device 仅在用户勾选「记住我」时创建；否则清理旧 cookie，
	// 遵循用户选择（此前无条件创建 30 天信任设备，与「记住我」语义不符）。
	if remember {
		trusted, err := auth.GenerateTrustedDeviceToken(u.ID, u.TokenVersion)
		if err != nil { // v21.58 P6：签名失败不静默（防止空 token 写入 Cookie）
			logger.Error("issueTOTPLoginResponse: sign trusted device token: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "签发信任设备凭证失败"})
			return
		}
		// v21.126 P2 修复：trusted_device Cookie 必须显式声明 SameSite=Lax，与
		// 主认证路径 auth_cookie.go 保持一致；此前只在清除分支设 SameSite，
		// 签发分支遗漏会依赖浏览器对省略 SameSite 的默认行为（Chrome Lax /
		// 其他不一定），存在跨站携带风险。gin 语义：SetSameSite 必须在
		// SetCookie 之前调用才会写入 Cookie 头。
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie("trusted_device", trusted, int(trustedDeviceTTL.Seconds()), "/", "", cookieSecure(c), true)
	} else {
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie("trusted_device", "", -1, "/", "", cookieSecure(c), true)
	}
	c.JSON(http.StatusOK, gin.H{"user": u, "access_token": access, "refresh_token": refresh})
}

// ---- 恢复码 ----

// insertRecoveryCodesTx 在**传入事务**内生成并写入 8 个恢复码（v21.58 P4 审计修复）：
// rand.Read/插入错误必须上报（否则 UI 显示恢复码但库中未存）；任一失败由调用方整体回滚。
// 供 generateRecoveryCodes（独立事务）与 ConfirmTOTP（与 totp_enabled 合并事务）复用。
func insertRecoveryCodesTx(tx *sql.Tx, userID int64) ([]string, error) {
	codes := make([]string, recoveryCodeCnt)
	seen := map[string]bool{}
	for i := 0; i < recoveryCodeCnt; i++ {
		for {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err != nil {
				return nil, fmt.Errorf("generate recovery code: %w", err)
			}
			code := hex.EncodeToString(b)[:recoveryCodeLen]
			if seen[code] {
				continue
			}
			seen[code] = true
			hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
			if err != nil {
				return nil, fmt.Errorf("hash recovery code: %w", err)
			}
			if _, err := tx.Exec(
				"INSERT INTO totp_recovery (user_id, code_hash) VALUES (?, ?)", userID, string(hash)); err != nil {
				return nil, fmt.Errorf("store recovery code: %w", err)
			}
			codes[i] = code
			break
		}
	}
	return codes, nil
}

func generateRecoveryCodes(userID int64) ([]string, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin recovery codes: %w", err)
	}
	defer tx.Rollback()

	codes, err := insertRecoveryCodesTx(tx, userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit recovery codes: %w", err)
	}
	return codes, nil
}

func matchRecoveryCode(userID int64, code string) bool {
	// v21.58 P3 审计修复：原子消费——事务 + 条件 UPDATE（WHERE id=? AND used=0）
	// + RowsAffected==1 才视为消费成功，杜绝并发下同一恢复码被使用两次（TOCTOU）。
	tx, err := database.DB.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		"SELECT id, code_hash FROM totp_recovery WHERE user_id = ? AND used = 0", userID,
	)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var hash string
		if err := rows.Scan(&id, &hash); err != nil {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)) != nil {
			continue
		}
		result, err := tx.Exec(
			"UPDATE totp_recovery SET used = 1 WHERE id = ? AND user_id = ? AND used = 0", id, userID)
		if err != nil {
			return false
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			return false // 已被并发请求消费 → 本次失败
		}
		if err := tx.Commit(); err != nil {
			return false
		}
		return true
	}
	return false
}
