package handler

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"stickynote/auth"
)

// minPasswordLen 密码最小长度（v21.96 用户需求：取消强密码强制执行策略）。
//
// 历史沿革：
//   - v21.95 B-6（审计 P2）曾要求「≥12 位 + 大写/小写/数字/符号至少三类」，
//     直接后果是用户用 123456 这类弱密码注册时被服务端 400 拒绝、无法完成注册。
//   - v21.96 按用户明确要求改为「仅提示、不强制」：唯一硬下限是 6 位
//     （防空密码与误触提交），字符复杂度**不再作为拦截条件**。
//
// 安全取舍（已知并接受）：弱密码账号更易被撞库/爆破。缓解手段仍是既有三道防线——
// loginGuard 账号+IP 双维度防爆破、TOTP 二次认证、找回密码安全问题，
// 而非在注册入口用复杂度规则把用户挡在门外。
const minPasswordLen = 6

// validatePasswordLength 密码最小长度校验（v21.96）。
//
// 仅校验长度下限；字符复杂度不做任何拦截。返回的错误文案由调用方直接下发给前端。
func validatePasswordLength(pwd string) error {
	if len([]rune(pwd)) < minPasswordLen {
		return fmt.Errorf("密码至少 %d 位", minPasswordLen)
	}
	return nil
}

// passwordStrengthLevel 返回密码强度分级：weak / medium / strong（v21.96）。
//
// 纯提示用途，**绝不用于拦截**——弱密码允许注册/创建/修改，由前端渲染提示条
// 并在提交成功后 toast 提醒，最终是否更换由用户自行决定。
//
// 分级规则（与前端 PasswordStrengthBar 保持同向，不要求逐位一致）：
//
//	strong: 长度 ≥12 且字符类别 ≥3
//	medium: 长度 ≥8  且字符类别 ≥2
//	weak:   其余（含长度刚好 6~7 的合法密码）
func passwordStrengthLevel(pwd string) string {
	runes := []rune(pwd)
	classes := 0
	if charsInRange(runes, 'A', 'Z') {
		classes++
	}
	if charsInRange(runes, 'a', 'z') {
		classes++
	}
	if charsInRange(runes, '0', '9') {
		classes++
	}
	other := 0
	for _, r := range runes {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			other++
			break
		}
	}
	if other > 0 {
		classes++
	}

	switch {
	case len(runes) >= 12 && classes >= 3:
		return "strong"
	case len(runes) >= 8 && classes >= 2:
		return "medium"
	default:
		return "weak"
	}
}

// charsInRange 判断 rune 切片中是否含 [lo, hi] 区间内的字符。
func charsInRange(runes []rune, lo, hi rune) bool {
	for _, r := range runes {
		if r >= lo && r <= hi {
			return true
		}
	}
	return false
}

type forgotPasswordRequest struct {
	Username       string `json:"username" binding:"required"`
	SecurityAnswer string `json:"security_answer" binding:"required"`
	NewPassword    string `json:"new_password" binding:"required,min=6"` // v21.96：12 → 6（见 minPasswordLen）
}

func parseTrustedDeviceToken(tokenStr string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		return auth.GetJWTSecret(), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		return nil, err
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("invalid claims")
	}
	if claims["type"] != "trusted_device" {
		return nil, errors.New("invalid token type")
	}
	return claims, nil
}

// generateTOTPToken 签发 2 分钟有效的临时 token（用于 TOTP 验证步骤）。
func generateTOTPToken(userID int64) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": userID,
		"type":    "totp",
		"exp":     time.Now().Add(2 * time.Minute).Unix(),
	})
	// v21.58 P6：签名错误显式返回（此前忽略，异常时可返回空 token）
	return token.SignedString(auth.GetJWTSecret())
}
