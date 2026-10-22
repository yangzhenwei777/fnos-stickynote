package backup

import (
	"fmt"
	"os"
	"path/filepath"
)

// VerifyArchive 解开一个 .snbak 做完整性校验（不改动线上数据），返回内部 manifest。
// 步骤：读 master → EnvelopeOpen(GCM tag+AAD 验) → 解包到临时目录（zip-slip 防护）→
// 逐文件比对 manifest.file_hashes → 对 notepad.db 跑 integrity_check。
// 复用于：CLI verify-backup、管理页"验证此备份"、CreateBackup 产档后的自检。
func VerifyArchive(env Env, snbakPath string) (*InternalManifest, error) {
	master, err := ReadMasterKey(env.DataDir)
	if err != nil {
		return nil, fmt.Errorf("verify: read master key: %w", err)
	}
	envBytes, err := os.ReadFile(snbakPath)
	if err != nil {
		return nil, fmt.Errorf("verify: read archive: %w", err)
	}
	payload, gotID, err := EnvelopeOpen(envBytes, master, env.capOrDefault())
	if err != nil {
		return nil, err // 已包装为 ErrDecryptFailed/ErrBackupTooLarge
	}
	tmp, err := os.MkdirTemp("", "snbak-verify-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	computed, err := ExtractArchive(payload, tmp)
	if err != nil {
		return nil, fmt.Errorf("verify: extract: %w", err)
	}
	manBytes, ok := computed["manifest.json"]
	_ = manBytes
	if !ok {
		return nil, fmt.Errorf("verify: manifest.json missing from archive")
	}
	// computed 是 rel→sha；manifest 也在其中，需从磁盘读 manifest 原文再解析
	mj, err := os.ReadFile(filepath.Join(tmp, "manifest.json"))
	if err != nil {
		return nil, err
	}
	man, err := ParseInternalManifest(mj)
	if err != nil {
		return nil, err
	}
	// §106 守卫：系统备份自检面只认自己的灾备归档；PORTABLE 便携包出现在这里即身份异常
	// （配错远端路径/他人包），fail-closed 拒验，绝不"顺手验通过"。
	if man.BackupType == BackupTypePortable {
		return nil, fmt.Errorf("verify: %w: portable (PORTABLE) archive is not a server backup", ErrAttachmentMissing)
	}
	if gotID != man.BackupID {
		return nil, fmt.Errorf("verify: backup_id mismatch envelope=%s manifest=%s", gotID, man.BackupID)
	}
	// 逐文件哈希比对（含 db/jwt/uploads；manifest 自身不在 file_hashes 内）
	for rel, want := range man.FileHashes {
		got, exists := computed[rel]
		if !exists {
			return nil, fmt.Errorf("verify: %w: %s missing", ErrAttachmentMissing, rel)
		}
		if got != want {
			return nil, fmt.Errorf("verify: %s sha256 mismatch (corrupted archive)", rel)
		}
	}
	// 反向校验（审计 GPT-07）：实际解包文件集合必须 ⊆ manifest 声明 ∪ {manifest.json}，
	// 拒绝归档夹带 manifest 之外的额外文件（否则 placeIn 会把未声明文件复制进正式 upload 目录）。
	for rel := range computed {
		if rel == "manifest.json" {
			continue
		}
		if _, declared := man.FileHashes[rel]; !declared {
			return nil, fmt.Errorf("verify: archive contains undeclared file not in manifest: %s", rel)
		}
	}
	if err := IntegrityCheck(filepath.Join(tmp, "notepad.db")); err != nil {
		return nil, err
	}
	return man, nil
}

// ExtractArchiveToFile 把已校验的 .snbak 解包到 destDir（CLI restore-backup 用；
// 只落文件、不改线上数据。换库由运维停服后执行或切片 3 的 Web 恢复事务）。
func ExtractArchiveToFile(env Env, snbakPath, destDir string) error {
	master, err := ReadMasterKey(env.DataDir)
	if err != nil {
		return err
	}
	envBytes, err := os.ReadFile(snbakPath)
	if err != nil {
		return err
	}
	payload, _, err := EnvelopeOpen(envBytes, master, env.capOrDefault())
	if err != nil {
		return err
	}
	_, err = ExtractArchive(payload, destDir)
	return err
}
