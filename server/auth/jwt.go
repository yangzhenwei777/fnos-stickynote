package auth

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret []byte

func Init(secret string) {
	jwtSecret = []byte(secret)
}

// GetJWTSecret 暴露 JWT 密钥供 handler 层签发特殊 token（信任设备/TOTP临时）。
func GetJWTSecret() []byte {
	return jwtSecret
}

// 双 token 机制：
// - access token：短有效期（2h），Path=/ 全站携带，用于 API 鉴权
// - refresh token：长有效期（记住我 30 天，否则会话级），Path=/api/auth 限定，
//   只用于换取新 access token，无法直接调用业务 API
// claims 携带 token_version，改密码/重置密码时版本号 +1，
// 使所有已签发 refresh token 立即失效（全局吊销）。

const AccessTokenTTL = 2 * time.Hour

type Claims struct {
	UserID       int64  `json:"user_id"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	Type         string `json:"type"` // "access" | "refresh"
	TokenVersion int64  `json:"ver"`
	// Remember v21.108 P1-9：登录时「记住我」选择随 refresh token 携带，
	// 轮转时继承——修复旧实现以「剩余 TTL>3.5 天」猜测 remember 的缺陷
	// （不勾选记住我的会话 refresh 一次即被升级为 30 天持久 cookie，复选框形同虚设）。
	Remember bool `json:"rm,omitempty"`
	jwt.RegisteredClaims
}

func GenerateAccessToken(userID int64, username, role string, version int64) (string, error) {
	claims := Claims{
		UserID:       userID,
		Username:     username,
		Role:         role,
		Type:         "access",
		TokenVersion: version,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// GenerateRefreshToken 签发 refresh token，并**返回其 jti 供调用方落库**。
//
// v21.93 P1（R-01/H-04 审计核对）：一次性轮转。
// v21.90 仅在 token 内塞了随机 jti，既不存储也不校验，注释却称
// 「旧 token 由 token_version 全局吊销兜底」——而 Refresh 路径从不 bump
// token_version。实际效果只是「换新票」（replacement），旧票在过期前
// （7 天 / 记住我 30 天）仍可反复换发，泄露窗口并未缩短。
//
// 现在 jti 随签发返回并由 handler 写入 users.refresh_jti，Refresh 侧比对，
// 不符即判定重放并全局吊销（见 handler/auth.go Refresh）。
//
// remember（v21.108 P1-9）：登录时的「记住我」选择，编入 token 供轮转继承。
func GenerateRefreshToken(userID int64, version int64, ttl time.Duration, remember bool) (token string, jti string, err error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	jti = hex.EncodeToString(buf)
	claims := Claims{
		UserID:       userID,
		Type:         "refresh",
		TokenVersion: version,
		Remember:     remember,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	signed := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	str, err := signed.SignedString(jwtSecret)
	if err != nil {
		return "", "", err
	}
	return str, jti, nil
}

func ParseToken(tokenString string) (*Claims, error) {
	// 强制 HS256（v18.6）：不限制签名算法时，用同一密钥自签 HS384/HS512
	// 的 token 也会被默认接受——算法面收得越窄，算法混淆类攻击面越小。
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}
	return nil, jwt.ErrSignatureInvalid
}

// GenerateTrustedDeviceToken 签发信任设备 token（v20.2 TOTP 配套）。
// 有效期 30 天，用于跳过二次验证。
// v21.52 P6 审计修复：绑定 token_version（ver），改密/登出全局吊销时
// 信任设备 token 同步失效；校验侧必须验证 type=trusted_device。
// v21.58 P6 审计修复：SignedString 错误显式返回（此前忽略可返回空串写入 Cookie）。
func GenerateTrustedDeviceToken(userID, tokenVersion int64) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": userID,
		"type":    "trusted_device",
		"ver":     tokenVersion,
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(30 * 24 * time.Hour).Unix(),
	})
	return token.SignedString(jwtSecret)
}
