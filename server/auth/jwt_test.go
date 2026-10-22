package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// T-1（v1.0.134 审计整改）：JWT 签发/校验单元测试。
// 覆盖：签发+往返校验、密钥不匹配拒绝、过期拒绝、算法限定（仅 HS256）、
// refresh token jti 透传、token_version 透传（供 handler 做全局吊销比对）。

func TestGenerateAndParseAccessToken(t *testing.T) {
	Init("t1-secret")
	tok, err := GenerateAccessToken(42, "alice", "user", 7)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	claims, err := ParseToken(tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.UserID != 42 {
		t.Fatalf("UserID 应为 42，实际 %d", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Fatalf("Username 应为 alice，实际 %s", claims.Username)
	}
	if claims.Role != "user" {
		t.Fatalf("Role 应为 user，实际 %s", claims.Role)
	}
	if claims.Type != "access" {
		t.Fatalf("Type 应为 access，实际 %s", claims.Type)
	}
	if claims.TokenVersion != 7 {
		t.Fatalf("TokenVersion 应为 7，实际 %d", claims.TokenVersion)
	}
}

// 密钥不匹配：换密钥后旧 token 必须被拒（防密钥轮换后旧票复用）。
func TestParseTokenRejectsWrongSecret(t *testing.T) {
	Init("secret-A")
	tok, _ := GenerateAccessToken(1, "bob", "user", 1)
	Init("secret-B")
	if _, err := ParseToken(tok); err == nil {
		t.Fatal("密钥不匹配的 token 应被拒绝")
	}
}

// 过期 token 必须被拒（用负 ttl 的 refresh token 模拟过期）。
func TestParseTokenRejectsExpired(t *testing.T) {
	Init("exp-secret")
	tok, _, err := GenerateRefreshToken(1, 1, -time.Hour, false)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	if _, err := ParseToken(tok); err == nil {
		t.Fatal("过期 token 应被拒绝")
	}
}

// 算法限定（v18.6）：仅接受 HS256，自签 HS384 token 必须被拒（防算法混淆攻击）。
func TestParseTokenRejectsNonHS256(t *testing.T) {
	Init("alg-secret")
	claims := jwt.MapClaims{"user_id": 1, "type": "access"}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString([]byte("alg-secret"))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	if _, err := ParseToken(signed); err == nil {
		t.Fatal("非 HS256 算法 token 应被拒绝")
	}
}

// refresh token 的 jti 必须随 ParseToken 正确透传（R-1 一次性轮转校验依赖）。
func TestRefreshTokenJTI(t *testing.T) {
	Init("jti-secret")
	tok, jti, err := GenerateRefreshToken(1, 3, time.Hour, false)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	if jti == "" {
		t.Fatal("jti 不应为空")
	}
	claims, err := ParseToken(tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.ID != jti {
		t.Fatalf("jti 透传失败：claims.ID=%s, 期望 %s", claims.ID, jti)
	}
	if claims.Type != "refresh" {
		t.Fatalf("Type 应为 refresh，实际 %s", claims.Type)
	}
}

// token_version 透传：handler 据此比对用户存储版本做全局吊销。
func TestParseTokenPreservesVersion(t *testing.T) {
	Init("ver-secret")
	tok, _ := GenerateAccessToken(1, "u", "user", 99)
	claims, err := ParseToken(tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.TokenVersion != 99 {
		t.Fatalf("TokenVersion 应透传为 99，实际 %d", claims.TokenVersion)
	}
}
