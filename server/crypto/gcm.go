package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

// 二进制 AES-256-GCM 原语（供备份归档信封使用）。
//
// 与 EncryptRaw/DecryptRaw 的区别（v1.0.167 备份功能，审核 GPT P0 约束）：
//   - EncryptRaw 返回 **base64 字符串**且无 AAD——不能直接当二进制归档信封
//     （备份 .snbak 是二进制文件，需原样写字节、且要把 SNBK 头/backup_id/kid
//     纳入认证防篡改）。故备份侧用本文件的 SealGCM/OpenGCM，不复用 EncryptRaw。
//   - jwt_secret.enc 仍可用 EncryptRaw（就 32 字节，base64 无妨，落 DB 列）。
//
// 输出格式：[nonce(12B nonceLen)][ciphertext‖tag(GCM 16B)]，与 EncryptRaw 布局一致，
// 区别仅在 **不 base64** + **支持 AAD**。

// SealGCM 使用 32 字节原始密钥 AES-256-GCM 加密明文 → 二进制 [nonce][密文+tag]，aad 参与认证。
func SealGCM(plaintext, key, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("seal: new cipher: %w", err)
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("seal: new gcm: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("seal: generate nonce: %w", err)
	}
	// Seal 把密文+tag 追加到 dst 之后；这里用 nil dst 得到独立切片。
	ciphertext := aesgcm.Seal(nil, nonce, plaintext, aad)
	out := make([]byte, 0, nonceLen+len(ciphertext))
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// OpenGCM 解密 SealGCM 产出的二进制 [nonce][密文+tag]，aad 必须与加密时一致，否则认证失败。
func OpenGCM(sealed, key, aad []byte) ([]byte, error) {
	if len(sealed) < nonceLen+16 {
		return nil, fmt.Errorf("open: invalid sealed length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("open: new cipher: %w", err)
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("open: new gcm: %w", err)
	}
	nonce := sealed[:nonceLen]
	ciphertext := sealed[nonceLen:]
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		// 不区分泌钥错误/数据损坏/AAD 不符——对外统一"认证失败"，不泄露原因细节。
		return nil, fmt.Errorf("open: authentication failed")
	}
	return plaintext, nil
}
