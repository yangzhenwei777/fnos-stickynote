package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// 两层 manifest（v4.3-final A5）：
//   - InternalManifest：进 .snbak 加密载荷内，权威，**不含 archive_sha256**（自引用消除）、
//     含 instance_id、含 stats（user/note/attachment 数，加密内可见即可）、含逐文件 sha256。
//   - ExternalIndex：远端明文 index/<backup_id>.json，**discovery 元数据非可信根**、
//     去 stats（第三方网盘不该看到用户有多少笔记）、含 archive_sha256（.snbak 整体哈希）。

// BackupTypePortable 是 v20 §106 便携备份（.snbak PORTABLE）的内部 manifest 类型标记。
// 同一 SNBK Envelope（format=1/enc=1 不变），仅 manifest 增此字段；服务端整站恢复/自检
// 见到该值必须拒绝——便携包是单用户脱敏导出（无 .jwt_secret、TOTP 已剔除），不是灾备归档。
const BackupTypePortable = "PORTABLE"

// InternalManifest 是加密归档内的权威清单。
//
// ServerEpoch（v1.0.175 R0）：快照采集时**冻结快照库内**的 server_epoch（v20 §75——绝不可从
// live 库读，否则归档声明的代际可能与包内库状态错位）；恢复侧据此计算 new=max(pre,restored)+1。
// 增量字段：legacy 归档无此键，omitempty 解码为 0（走同一 max() 语义，天然兼容）。
type InternalManifest struct {
	FormatVersion int    `json:"format_version"`
	BackupID      string `json:"backup_id"`
	// —— v20 §106/§111 增补（omitempty：server 归档不写、legacy 解码为零值，双向兼容）——
	BackupType      string `json:"backup_type,omitempty"`           // "" = SERVER 灾备 | PORTABLE 便携
	BackupSetID     string `json:"backup_set_id,omitempty"`         // 便携导出会话标识（审计/去重）
	SyncProtocolVer int    `json:"sync_protocol_version,omitempty"` // 产包时桌面 sync 协议代
	InstanceID      string `json:"instance_id"`                     // slice-1 审核：包内必须含 instance_id
	CreatedAt       string `json:"created_at"`                      // UTC RFC3339
	Trigger         string `json:"trigger"`                         // manual | scheduled
	AppVersion      string `json:"app_version"`
	SchemaVersion   int    `json:"schema_version"`
	Enc             int    `json:"enc"`
	ServerEpoch     int64  `json:"server_epoch,omitempty"`
	Database        struct {
		Path   string `json:"path"`
		Sha256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"database"`
	JWTSecretIncluded bool `json:"jwt_secret_included"`
	Attachments       struct {
		Count      int64             `json:"count"`
		TotalBytes int64             `json:"total_bytes"`
		Files      []AttachmentEntry `json:"files"`
	} `json:"attachments"`
	Stats struct {
		UserCount       int `json:"user_count"`
		NoteCount       int `json:"note_count"`
		AttachmentCount int `json:"attachment_count"`
	} `json:"stats"`
	// FileHashes：归档内各相对路径 → sha256（恢复时逐文件校验）。不含 manifest 自身。
	FileHashes map[string]string `json:"file_hashes"`
}

// AttachmentEntry 归档内一个附件文件。
type AttachmentEntry struct {
	Name   string `json:"name"` // uploads/<uuid>.ext 的 basename
	Sha256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ExternalIndex 是远端明文索引（discovery，非可信根）。
type ExternalIndex struct {
	FormatVersion    int    `json:"format_version"`
	BackupID         string `json:"backup_id"`
	InstanceID       string `json:"instance_id"`
	CreatedAt        string `json:"created_at"`
	AppVersion       string `json:"app_version"`
	SchemaVersion    int    `json:"schema_version"`
	EncAlgo          string `json:"enc_algo"`
	Enc              int    `json:"enc"`
	Kid              string `json:"kid"`
	ArchiveSizeBytes int64  `json:"archive_size_bytes"`
	ArchiveSha256    string `json:"archive_sha256"`
}

// Marshal/MarshalIndent helpers（错误包装统一）。
func marshalIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return b, nil
}

// ParseInternalManifest 解析归档内 manifest。
func ParseInternalManifest(b []byte) (*InternalManifest, error) {
	var m InternalManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse internal manifest: %w", err)
	}
	if m.FormatVersion != 1 {
		return nil, fmt.Errorf("unsupported manifest format_version %d", m.FormatVersion)
	}
	return &m, nil
}

// ParseExternalIndex 解析远端明文 index。
func ParseExternalIndex(b []byte) (*ExternalIndex, error) {
	var x ExternalIndex
	if err := json.Unmarshal(b, &x); err != nil {
		return nil, fmt.Errorf("parse external index: %w", err)
	}
	return &x, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := openRO(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func sha256Bytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
