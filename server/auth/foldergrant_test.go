package auth

import (
	"testing"
	"time"
)

func TestFolderGrantSignParse(t *testing.T) {
	Init("test-secret-folder-grant")
	tok, exp, err := SignFolderGrant(7, 42, 3, "abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || exp.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("token/exp 异常: %q %v", tok, exp)
	}
	g, err := ParseFolderGrant(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if g.UserID != 7 || g.FolderID != 42 || g.TokenVersion != 3 || g.Fingerprint != "abcdef0123456789" {
		t.Fatalf("claims 不符: %+v", g)
	}

	// 篡改签名必须拒绝。注意：签名是 32 字节的 base64.RawURLEncoding（43 字符），
	// 末位字符只含 4 个数据位 + 2 个填充位——翻转末位可能解码出同一字节而误判"未篡改"；
	// 固定写 "A" 也会在原末位恰为 A 时令牌未变。故取签名区中部并确定性翻转。
	i := len(tok) - 10
	badBytes := []byte(tok)
	if badBytes[i] == 'A' {
		badBytes[i] = 'B'
	} else {
		badBytes[i] = 'A'
	}
	bad := string(badBytes)
	if _, err := ParseFolderGrant(bad); err == nil {
		t.Fatal("篡改令牌应拒绝")
	}
	// 缺失分隔符/空串
	if _, err := ParseFolderGrant("garbage"); err == nil {
		t.Fatal("格式错误应拒绝")
	}
}

func TestFolderGrantSecretRotationInvalidates(t *testing.T) {
	Init("secret-a")
	tok, _, err := SignFolderGrant(1, 2, 0, "fp")
	if err != nil {
		t.Fatal(err)
	}
	Init("secret-b")
	if _, err := ParseFolderGrant(tok); err == nil {
		t.Fatal("密钥变更后旧令牌应失效")
	}
}
