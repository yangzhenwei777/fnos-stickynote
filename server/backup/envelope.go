package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"stickynote/crypto"
)

// .snbak 归档信封（V1 = enc=1 整文件 AES-256-GCM，二进制、带 AAD 绑定）。
//
// 二进制布局：
//   magic[4]="SNBK" | format_ver[1]=1 | enc[1]=1
//   | id_len[1] | backup_id[id_len]           // 36 字符 UUID
//   | plaintext_size[8 BE]                     // tar.gz 明文字节数（Seal 前已知，进 AAD）
//   | kid[8 raw]                               // = sha256(master)[:8]（进 AAD）
//   | sealed[nonce(12)|ciphertext|tag(16)]
//
// AAD = magic ‖ format_ver ‖ enc ‖ backup_id ‖ plaintext_size ‖ kid
// → 绑定"这个密文是什么格式、属于哪个 backup、由哪把钥匙(kid)"，防：
//   - enc 字段被改（enc=1→2 走错误解析路径 → AAD 不符 → GCM 认证失败）
//   - archive ABC 伪装成 archive XYZ（backup_id 在 AAD，tag 校验失败）
//   - 换 kid（密钥归属篡改）。
// 主密钥 32B 随机，归档加密密钥 = DeriveKey(master,"archive-encryption")（HKDF salt=nil）。

const (
	envMagic  = "SNBK"
	envVerV1  = 1
	envEncV1  = 1 // 整文件 AES-256-GCM
	envEncV2  = 2 // 二期分块（V1 不产，Open 拒绝以防误用未定义格式）
	envIDLen  = 36
	kidRawLen = 8
)

// DefaultArchiveCapBytes 是归档大小帽（非"内存帽"；SealGCM 整文件进内存，峰值 ≈ 2×归档）。
// 有效帽在调用处取 min(此值, ProviderCapability.MaxObjectSize)。真机内存实测后可下调。
const DefaultArchiveCapBytes int64 = 256 * 1024 * 1024 // 256MB

// EnvelopeSeal 把 tar.gz 明文 payload 封装成 .snbak 字节。maxBytes<=0 用默认帽。
func EnvelopeSeal(payload []byte, backupID string, master []byte, maxBytes int64) ([]byte, error) {
	if len(master) != masterKeyLen {
		return nil, fmt.Errorf("seal: master key length %d", len(master))
	}
	if maxBytes <= 0 {
		maxBytes = DefaultArchiveCapBytes
	}
	// V1 整文件 GCM 需要把明文整体交给 AEAD → 归档大小即内存边界。
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("%w: archive %d bytes > cap %d", ErrBackupTooLarge, len(payload), maxBytes)
	}
	if len(backupID) != envIDLen {
		return nil, fmt.Errorf("seal: backup_id must be 36-char UUID, got %d", len(backupID))
	}
	archiveKey, err := crypto.DeriveKey(master, "archive-encryption")
	if err != nil {
		return nil, fmt.Errorf("seal: derive archive key: %w", err)
	}
	kid := kidRaw(master)
	ptSize := uint64(len(payload))

	aad := buildAAD(backupID, ptSize, kid)
	sealed, err := crypto.SealGCM(payload, archiveKey, aad)
	if err != nil {
		return nil, fmt.Errorf("seal: SealGCM: %w", err)
	}

	var idb [1]byte
	idb[0] = byte(len(backupID))
	sizeBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(sizeBuf, ptSize)

	var head bytes.Buffer
	head.WriteString(envMagic)
	head.WriteByte(envVerV1)
	head.WriteByte(envEncV1)
	head.Write(idb[:])
	head.WriteString(backupID)
	head.Write(sizeBuf)
	head.Write(kid[:])
	head.Write(sealed)
	return head.Bytes(), nil
}

// EnvelopeOpen 解析并解密 .snbak，返回 payload 与 backupID。aad 从头部字段重建，
// 任一字段被篡改 → GCM 认证失败（ErrDecryptFailed）。
func EnvelopeOpen(env, master []byte, maxBytes int64) (payload []byte, backupID string, err error) {
	if len(master) != masterKeyLen {
		return nil, "", fmt.Errorf("open: master key length %d", len(master))
	}
	if maxBytes <= 0 {
		maxBytes = DefaultArchiveCapBytes
	}
	// magic(4)+ver(1)+enc(1)+idlen(1) = 7 起
	if len(env) < 7 {
		return nil, "", fmt.Errorf("%w: envelope too short", ErrDecryptFailed)
	}
	if string(env[:4]) != envMagic {
		return nil, "", fmt.Errorf("%w: bad magic", ErrDecryptFailed)
	}
	ver := env[4]
	enc := env[5]
	idLen := int(env[6])
	if ver != envVerV1 || enc != envEncV1 {
		// V1 只认 enc=1；enc=2（二期分块）或未知 → 拒绝，不"按 header 选解析路径"。
		return nil, "", fmt.Errorf("%w: unsupported format_ver=%d enc=%d", ErrDecryptFailed, ver, enc)
	}
	// 7 + idLen + 8(size) + 8(kid) + 12(nonce) + 16(tag) 至少
	if len(env) < 7+idLen+8+kidRawLen+12+16 {
		return nil, "", fmt.Errorf("%w: envelope truncated", ErrDecryptFailed)
	}
	backupID = string(env[7 : 7+idLen])
	off := 7 + idLen
	sizeBuf := env[off : off+8]
	ptSize := binary.BigEndian.Uint64(sizeBuf)
	off += 8
	var kid [kidRawLen]byte
	copy(kid[:], env[off:off+kidRawLen])
	off += kidRawLen
	sealed := env[off:]

	if int64(ptSize) > maxBytes {
		return nil, "", fmt.Errorf("%w: declared archive %d > cap %d", ErrBackupTooLarge, ptSize, maxBytes)
	}

	aad := buildAAD(backupID, ptSize, kid)
	archiveKey, err := crypto.DeriveKey(master, "archive-encryption")
	if err != nil {
		return nil, "", fmt.Errorf("open: derive archive key: %w", err)
	}
	payload, err = crypto.OpenGCM(sealed, archiveKey, aad)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrDecryptFailed, err)
	}
	// 实际明文长度须与声明一致（防声明超大占位再喂小密文的尺寸混淆）
	if uint64(len(payload)) != ptSize {
		return nil, "", fmt.Errorf("%w: payload size %d != declared %d", ErrDecryptFailed, len(payload), ptSize)
	}
	return payload, backupID, nil
}

// kidRaw 归档头里的 kid 原始 8 字节 = sha256(master)[:8]（与 MasterKeyKid hex 前 16 字符同源）。
func kidRaw(master []byte) [kidRawLen]byte {
	sum := sha256.Sum256(master)
	var k [kidRawLen]byte
	copy(k[:], sum[:kidRawLen])
	return k
}

// buildAAD = magic ‖ ver(1) ‖ enc(1) ‖ backupID ‖ plaintext_size(8 BE) ‖ kid(8)。
func buildAAD(backupID string, ptSize uint64, kid [kidRawLen]byte) []byte {
	var b bytes.Buffer
	b.WriteString(envMagic)
	b.WriteByte(envVerV1)
	b.WriteByte(envEncV1)
	b.WriteString(backupID)
	sz := make([]byte, 8)
	binary.BigEndian.PutUint64(sz, ptSize)
	b.Write(sz)
	b.Write(kid[:])
	return b.Bytes()
}
