//go:build windows

package dpapi

import (
	"crypto/rand"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 自描述头：magic + version + nonce。
const (
	blobMagic   = "SDP1"
	blobVersion = byte(1)
	nonceLen    = 8
	headerLen   = len(blobMagic) + 1 + nonceLen // 13
)

// appEntropy 应用绑定熵（固定常量，随版本演进可加代际后缀，勿随机器变化——变化即自毁可迁移性）。
var appEntropy = []byte("stickynote-desktop/p3-localauth/v1")

// Protect 用户域 DPAPI 加密。输入为空返回错误（空凭据落盘是 bug 不是特性）。
func Protect(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("dpapi: empty plaintext")
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// 载荷只加密正文；自描述头（magic+version+nonce）明文侧袋，绑定关系由 nonce∈entropy 承担。
	out, err := callCryptProtect(buildEntropy(nonce), plaintext)
	if err != nil {
		return nil, err
	}
	res := make([]byte, 0, headerLen+len(out))
	res = append(res, blobMagic...)
	res = append(res, blobVersion)
	res = append(res, nonce...)
	res = append(res, out...)
	return res, nil
}

// Unprotect 解密 Protect 产出的自描述 blob。一切失败统一 ErrCorrupted（防细节 oracle）。
func Unprotect(blob []byte) ([]byte, error) {
	if len(blob) < headerLen || string(blob[:4]) != blobMagic || blob[4] != blobVersion {
		return nil, ErrCorrupted
	}
	nonce := blob[5:headerLen]
	cipher := blob[headerLen:]
	plain, err := callCryptUnprotect(buildEntropy(nonce), cipher)
	if err != nil {
		return nil, ErrCorrupted
	}
	return plain, nil
}

func buildEntropy(nonce []byte) []byte {
	e := make([]byte, 0, len(appEntropy)+nonceLen)
	e = append(e, appEntropy...)
	e = append(e, nonce...)
	return e
}

func callCryptProtect(entropy, data []byte) ([]byte, error) {
	dbIn := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	dbEnt := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var dbOut windows.DataBlob
	// unsafe.Pointer 转换即钉住 Go 侧缓冲；DPAPI 只读入参。UI 全禁、无提示（后台服务语义）。
	err := windows.CryptProtectData(&dbIn, nil, &dbEnt, 0, (*windows.CryptProtectPromptStruct)(nil), windows.CRYPTPROTECT_UI_FORBIDDEN, &dbOut)
	if err != nil {
		return nil, err
	}
	return copyOut(&dbOut)
}

func callCryptUnprotect(entropy, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, ErrCorrupted
	}
	dbIn := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	dbEnt := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var dbOut windows.DataBlob
	err := windows.CryptUnprotectData(&dbIn, nil, &dbEnt, 0, (*windows.CryptProtectPromptStruct)(nil), windows.CRYPTPROTECT_UI_FORBIDDEN, &dbOut)
	if err != nil {
		return nil, err
	}
	return copyOut(&dbOut)
}

// copyOut 拷贝 DPAPI 分配的本地堆缓冲并 LocalFree（x/sys 语义：err==nil 即成功；
// LocalFree 返回值/错误忽略——微软契约：非 NULL 返回才可疑，此处不改变释放事实）。
func copyOut(out *windows.DataBlob) ([]byte, error) {
	if out.Size == 0 || out.Data == nil {
		return nil, ErrCorrupted
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(out.Data)), int(out.Size))
	res := make([]byte, len(b))
	copy(res, b)
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return res, nil
}
