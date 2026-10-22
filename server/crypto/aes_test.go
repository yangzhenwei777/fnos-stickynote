package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

func TestEncryptDecrypt(t *testing.T) {
	plain := "Hello 世界！笔记内容包含特殊字符 <>&\"'"
	pwd := "我的密码123"
	enc, err := Encrypt(plain, pwd)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := Decrypt(enc, pwd)
	if err != nil {
		t.Fatal(err)
	}
	if dec != plain {
		t.Errorf("decoded mismatch: got %q", dec)
	}
}

func TestDecryptWrongPassword(t *testing.T) {
	enc, _ := Encrypt("secret", "correct")
	_, err := Decrypt(enc, "wrong")
	if err == nil {
		t.Error("expected error with wrong password")
	}
}

func TestEncryptRandomness(t *testing.T) {
	e1, _ := Encrypt("x", "pwd")
	e2, _ := Encrypt("x", "pwd")
	if e1 == e2 {
		t.Error("two encryptions should differ (different salts)")
	}
}

func TestDecryptCorrupted(t *testing.T) {
	enc, _ := Encrypt("data", "pwd")
	_, err := Decrypt(enc[:len(enc)-5], "pwd")
	if err == nil {
		t.Error("expected error with corrupted ciphertext")
	}
	_, err = Decrypt("!!!not-valid-base64", "pwd")
	if err == nil {
		t.Error("expected error with invalid base64")
	}
}

// ---- v21.52 P2 审计修复：加密格式判别（magic 前缀替代数值阈值）----

// TestP2NewFormatHasMagic 新格式密文必须以 "NPD1" magic 开头。
func TestP2NewFormatHasMagic(t *testing.T) {
	enc, err := Encrypt("hello", "pass1234")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(enc)
	if len(raw) < 8 || string(raw[:4]) != cryptoMagic {
		t.Fatalf("new format missing magic prefix: got %q", string(raw[:4]))
	}
}

// TestP2LegacyFormatCompat 旧格式（无 magic，随机 salt 开头）必须能按 4096 迭代解密。
// 此前的数值阈值判断（Uint32(data[:4]) >= 4096）会把随机 salt 误判为新格式
// 并当 iteration 使用——旧数据解密失败甚至 CPU 耗尽。magic 判别彻底修复。
func TestP2LegacyFormatCompat(t *testing.T) {
	plain := "legacy note"
	legacy := legacyEncryptForTest(plain, "legacypass")
	dec, err := Decrypt(legacy, "legacypass")
	if err != nil {
		t.Fatalf("legacy decrypt failed: %v", err)
	}
	if dec != plain {
		t.Fatalf("legacy decrypt mismatch: got %q", dec)
	}
}

// TestP2HugeIterRejected 超限 PBKDF2 iteration（> maxIter）必须被拒绝，防资源耗尽。
func TestP2HugeIterRejected(t *testing.T) {
	iterBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(iterBuf, uint32(maxIter+1))
	huge := cryptoMagic + string(iterBuf) + strings.Repeat("A", saltLen+nonceLen+16)
	if _, err := Decrypt(base64.StdEncoding.EncodeToString([]byte(huge)), "x"); err == nil {
		t.Fatal("huge iteration should be rejected")
	}
}

// TestP2ShortDataError 长度不足/损坏数据必须明确报错而非 panic。
func TestP2ShortDataError(t *testing.T) {
	if _, err := Decrypt(base64.StdEncoding.EncodeToString([]byte("short")), "x"); err == nil {
		t.Fatal("short data should error")
	}
}

// legacyEncryptForTest 复刻旧格式（无 magic，4096 迭代）：salt16+nonce12+cipher。
func legacyEncryptForTest(plaintext, password string) string {
	salt := make([]byte, saltLen)
	rand.Read(salt)
	key := pbkdf2.Key([]byte(password), salt, 4096, keyLen, sha256.New)
	block, _ := aes.NewCipher(key)
	aesgcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, nonceLen)
	rand.Read(nonce)
	out := append(salt, nonce...)
	out = append(out, aesgcm.Seal(nil, nonce, []byte(plaintext), nil)...)
	return base64.StdEncoding.EncodeToString(out)
}
