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

	// 篡改签名必须拒绝
	bad := tok[:len(tok)-1] + "A"
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
