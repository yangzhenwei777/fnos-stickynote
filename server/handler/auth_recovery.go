package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"stickynote/logger"
	"stickynote/model"
)

// unifiedSecurityQuestionPrompt 找回密码的统一提示话术
// （v21.93 P3：H-05 用户名枚举 oracle 修复）。
//
// 此前的防枚举设计：真实用户返回其真实安全问题；不存在的用户返回由用户名
// **确定性派生**的「假问题」（FNV-1a 哈希取模问题池）。问题在于——确定性
// 意味着攻击者可以本地计算 fakeSecurityQuestion(u) 并与响应比对：不一致即
// 说明该账号真实存在（除非真实用户选的问题恰好与假问题相同）。
// 也就是说，这个「防枚举」措施本身成了一个部分有效的账号存在性预言机。
//
// 现在改为统一话术：不论账号是否存在、是否设置过安全问题，一律返回同一句提示，
// 响应中不含任何与账号相关的信息。
//
// 代价（已知取舍）：用户必须记得自己当初设置的问题与答案。若忘记，将无法通过
// 安全问题自助重置密码，需联系管理员重置。安全问题本就是「只有本人知道」的
// 第二因子，这一取舍与安全问题的定位一致。
const unifiedSecurityQuestionPrompt = "请回答您注册时设置的安全问题"

func GetSecurityQuestion(c *gin.Context) {
	username := c.Query("username")
	if username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供用户名"})
		return
	}

	// 防枚举（v18.3）：与登录共用同一 guard——锁定中直接 429（统一话术，不暴露账号状态）。
	if locked, until := guard.locked(username, c.ClientIP()); locked {
		logger.AuditEvent("security_question", username, c.ClientIP(), "blocked", "locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}

	// v21.95 N-03'（审计 P2）：用户存在/不存在**对称计失败**。
	// 此前仅"不存在"计失败 → 攻击者枚举时可用"请求不累积失败"区分目标是否真实存在。
	// 现无论存在与否都计一次失败（IP 总量维度 + 用户名|IP 维度），配合统一话术彻底消除枚举预言机。
	// 正常用户低频查询安全问题的使用场景不受影响（阈值 5 次/10 分钟，且成功重置会清零）。
	_, _ = model.GetUserByUsername(username)
	guard.fail(username, c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"security_question": unifiedSecurityQuestionPrompt})
}

type verifyAnswerRequest struct {
	Username       string `json:"username" binding:"required"`
	SecurityAnswer string `json:"security_answer" binding:"required"`
}

func VerifyAnswer(c *gin.Context) {
	var req verifyAnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// 防爆破（v18.3）：与登录共用同一 guard——安全答案本质是第二口令，
	// 不限制的的话可被撞库/枚举用户名。失败计入同一 用户名|IP 与 IP 总量维度。
	// v21.52 P5：追加账号全局维度（account:<username>），防分散来源 IP 持续爆破。
	if locked, until := guard.locked(req.Username, c.ClientIP()); locked {
		logger.AuditEvent("verify_answer", req.Username, c.ClientIP(), "blocked", "locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}
	if locked, until := guard.accountLocked(req.Username); locked {
		logger.AuditEvent("verify_answer", req.Username, c.ClientIP(), "blocked", "account locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}

	// 统一失败话术（v18.7）：用户不存在 / 未设安全问题 / 答案错误
	// 全部返回同一 400「安全答案错误」，响应不再是枚举预言机。
	valid, err := model.VerifySecurityAnswer(req.Username, req.SecurityAnswer)
	if err != nil || !valid {
		guard.fail(req.Username, c.ClientIP())
		guard.accountFail(req.Username)
		logger.AuditEvent("verify_answer", req.Username, c.ClientIP(), "failed", fmt.Sprintf("err=%v valid=%v", err, valid))
		c.JSON(http.StatusBadRequest, gin.H{"error": "安全答案错误"})
		return
	}
	guard.accountSuccess(req.Username)
	c.JSON(http.StatusOK, gin.H{"message": "验证成功"})
}

func ForgotPassword(c *gin.Context) {
	var req forgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		bindError(c, err)
		return
	}

	// 防爆破（v18.3）：与登录/答案校验共用同一 guard，锁定期直接拒绝
	// v21.52 P5：追加账号全局维度，防分散来源 IP 爆破安全答案。
	if locked, until := guard.locked(req.Username, c.ClientIP()); locked {
		logger.AuditEvent("forgot_password", req.Username, c.ClientIP(), "blocked", "locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}
	if locked, until := guard.accountLocked(req.Username); locked {
		logger.AuditEvent("forgot_password", req.Username, c.ClientIP(), "blocked", "account locked until "+until.Format("15:04:05"))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("尝试次数过多", until)})
		return
	}

	// Verify security answer first（失败话术统一，同 VerifyAnswer）
	valid, err := model.VerifySecurityAnswer(req.Username, req.SecurityAnswer)
	if err != nil || !valid {
		guard.fail(req.Username, c.ClientIP())
		guard.accountFail(req.Username)
		logger.AuditEvent("forgot_password", req.Username, c.ClientIP(), "failed", fmt.Sprintf("err=%v valid=%v", err, valid))
		c.JSON(http.StatusBadRequest, gin.H{"error": "安全答案错误"})
		return
	}

	// v21.96：只卡最小长度 6 位（minPasswordLen），不再强制字符复杂度
	if err := validatePasswordLength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := model.UpdatePasswordByUsername(req.Username, req.NewPassword); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码重置失败"})
		return
	}

	// 重置成功 = 已通过第二因子校验，清零该 用户名|IP 的失败记录（同登录成功语义）
	guard.success(req.Username, c.ClientIP())
	guard.accountSuccess(req.Username)
	strength := passwordStrengthLevel(req.NewPassword) // v21.96：仅提示与审计留痕
	logger.AuditEvent("forgot_password", req.Username, c.ClientIP(), "success", "password reset pwd_strength: "+strength)
	c.JSON(http.StatusOK, gin.H{
		"message":           "密码重置成功",
		"password_strength": strength,
	})
}

func ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		bindError(c, err)
		return
	}

	// v21.96：只卡最小长度 6 位（minPasswordLen），不再强制字符复杂度
	if err := validatePasswordLength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("userID")
	username, _ := c.Get("username")

	user, err := model.AuthenticateUser(username.(string), req.CurrentPassword)
	if err != nil {
		// v21.105 P0-3：业务校验失败（当前密码错误）不再返回 401——
		// 前端 401 拦截器会把一切非豁免端点的 401 当作会话过期，
		// 误触发 refresh→重试→clearTokens→跳登录页，用户永远看不到
		// 「当前密码错误」提示（操作逻辑审查 P0-3）。改为 400 业务语义。
		logger.AuditEvent("change_password", username.(string), c.ClientIP(), "failed", "bad current password") // v21.108 审计补齐
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前密码错误"})
		return
	}

	if err := model.UpdatePassword(user.ID, req.NewPassword); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码修改失败"})
		return
	}

	// 密码已改（UpdatePassword 内部递增 token_version），其他设备的会话全部失效；
	// 本会话重新签发新版本 token 保持登录。
	// v21.105 P0-2：新 token 除 Set-Cookie 外必须随响应体下发——前端本地
	// storage 持有旧 token 且请求头优先于 cookie，仅靠 cookie 下发会导致
	// 本会话下一个请求仍带旧 token 被 401，用户被静默踢回登录页
	// （操作逻辑审查 P0-2，实测复现）。
	resp := gin.H{}
	fresh, err := model.GetUserByID(user.ID)
	if err == nil {
		if access, refresh, terr := issueTokenPair(c, fresh, false); terr != nil {
			logger.Error("changePassword: reissue token failed: %v", terr) // v21.67 P2
		} else {
			resp["access_token"] = access
			resp["refresh_token"] = refresh
		}
	}

	_ = userID                                         // used for auth check
	strength := passwordStrengthLevel(req.NewPassword) // v21.96：仅提示与审计留痕
	logger.AuditEvent("change_password", username.(string), c.ClientIP(), "success", "pwd_strength: "+strength)
	resp["message"] = "密码修改成功"
	resp["password_strength"] = strength
	c.JSON(http.StatusOK, resp)
}

func UpdateSecurityQuestion(c *gin.Context) {
	var req updateSecurityQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	userID, _ := c.Get("userID")
	username, _ := c.Get("username")

	// v21.90 P0（H-02）：先验证当前密码（敏感操作二次认证）
	if _, err := model.AuthenticateUser(username.(string), req.CurrentPassword); err != nil {
		logger.AuditEvent("security_question_update", username.(string), c.ClientIP(), "failed", "bad password")
		// v21.105 P0-3：401 → 400（业务密码错误误触发前端会话过期链路，见 ChangePassword 注释）
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前密码错误"})
		return
	}

	// v21.90 P2：安全答案最短长度校验（去首尾空格后）；v21.95 B-6 提升至 8
	if ans := strings.TrimSpace(req.SecurityAnswer); ans != "" && len([]rune(ans)) < minSecurityAnswerLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("安全答案至少 %d 个字符", minSecurityAnswerLen)})
		return
	}

	if err := model.UpdateSecurityQuestion(userID.(int64), req.SecurityQuestion, req.SecurityAnswer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "安全问题修改失败"})
		return
	}

	logger.AuditEvent("security_question_update", username.(string), c.ClientIP(), "success", "")
	c.JSON(http.StatusOK, gin.H{"message": "安全问题修改成功"})
}

// ---- v20.2 TOTP 辅助函数 ----

// parseTrustedDeviceToken 解析信任设备 JWT（MapClaims，不适用 ParseToken 的 Claims 结构体）。
// v21.52 P6 审计修复：校验 type=trusted_device 且 ver 与用户当前 token_version 一致，
// 使改密/登出全局吊销对信任设备 token 同样生效（此前 trusted cookie 泄露后 30 天内仍可用）。
