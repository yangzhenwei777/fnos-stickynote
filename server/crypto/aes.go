package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

const (
	saltLen   = 16
	nonceLen  = 12      // GCM standard
	keyLen    = 32      // AES-256
	pbkdfIter = 600_000 // OWASP 2023 建议下限；兼容旧密文：旧数据 4096 次迭代解密不受影响，新加密统一用此值

	// 格式判别（v21.52 P2 审计修复）：显式 magic 前缀替代数值阈值。
	// 旧格式（v21.49 前）：[salt 16B][nonce 12B][密文]，迭代固定 4096。
	// 新格式（v21.49 起）：["NPD1" 4B][iter 4B BE][salt 16B][nonce 12B][密文]。
	// 此前用 binary.BigEndian.Uint32(data[:4]) >= 4096 判新格式——旧格式前 4 字节是
	// 随机 salt 几乎必然 ≥4096，导致旧密文被误判为新格式、随机 salt 被当 iteration
	// （可达数十亿次 PBKDF2 → 解密失败 + CPU 资源耗尽）。
	cryptoMagic = "NPD1"
	legacyIter  = 4096
	minIter     = 4096
	maxIter     = 2_000_000
)

// Encrypt 使用密码加密明文 → base64(["NPD1"][iter 4B BE][salt][nonce][密文+GCM tag])。
func Encrypt(plaintext, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := pbkdf2.Key([]byte(password), salt, pbkdfIter, keyLen, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := aesgcm.Seal(nil, nonce, []byte(plaintext), nil)

	// magic(4B) + iter(4B BE) + salt + nonce + 密文
	iterBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(iterBuf, uint32(pbkdfIter))
	out := make([]byte, 0, 4+4+saltLen+nonceLen+len(ciphertext))
	out = append(out, cryptoMagic...)
	out = append(out, iterBuf...)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt 使用密码解密 base64 密文 → 明文。
// 格式判别（v21.52 P2 修复）：前缀 "NPD1" = 新格式（读 4B iter 并校验范围）；
// 否则视为旧格式（无 magic，迭代固定 4096）。不再用数值阈值，杜绝随机 salt 误判。
func Decrypt(encoded, password string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid encoding")
	}
	var iter int
	if len(data) >= 4+4+saltLen+nonceLen+16 && string(data[:4]) == cryptoMagic {
		// 新格式：magic + iter(4B)
		iter = int(binary.BigEndian.Uint32(data[4:8]))
		if iter < minIter || iter > maxIter {
			return "", fmt.Errorf("invalid PBKDF2 iteration count: %d", iter)
		}
		data = data[8:]
	} else {
		// 旧格式：无 magic，迭代固定 4096
		iter = legacyIter
	}
	if len(data) < saltLen+nonceLen+16 {
		return "", fmt.Errorf("invalid ciphertext")
	}
	salt := data[:saltLen]
	nonce := data[saltLen : saltLen+nonceLen]
	ciphertext := data[saltLen+nonceLen:]

	key := pbkdf2.Key([]byte(password), salt, iter, keyLen, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ---- 原始密钥加密（供服务端加密用，不依赖用户密码） ----

// EncryptRaw 使用 32 字节原始密钥 AES-256-GCM 加密明文 → base64([nonce][密文+GCM tag])。
func EncryptRaw(plaintext []byte, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := aesgcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, nonceLen+len(ciphertext))
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// DecryptRaw 使用 32 字节原始密钥 AES-256-GCM 解密 base64 密文 → 明文。
func DecryptRaw(encoded string, key []byte) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid encoding")
	}
	if len(data) < nonceLen+16 {
		return nil, fmt.Errorf("invalid ciphertext")
	}
	nonce := data[:nonceLen]
	ciphertext := data[nonceLen:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aesgcm.Open(nil, nonce, ciphertext, nil)
}

// DeriveKey 从主密钥派生 32 字节子密钥（HKDF-SHA256）。
// v21.90 P2（审计低危项）：panic 改为显式 error 返回——crypto/rand 失败极罕见，
// 但生产代码不应以崩溃响应（调用方已具备错误传播链）。
func DeriveKey(master []byte, info string) ([]byte, error) {
	reader := hkdf.New(sha256.New, master, nil, []byte(info))
	key := make([]byte, 32)
	if _, err := reader.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}
