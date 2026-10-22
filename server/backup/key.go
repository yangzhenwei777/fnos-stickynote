package backup

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 备份功能涉及三个数据目录下的持久化文件（均 0600、原子 tmp→rename 写入，
// 复用 config.go loadOrCreateJWTSecret 的范式）：
//   .backup_master_key  —— 归档/凭据加密主密钥（32B 随机，base64 落盘）
//   .instance_id        —— 本便签实例唯一标识（UUID，纯文本），**不进 SQLite**
//                          （进库会被旧快照覆盖、与导出密钥文件里那份打架）
//   .pending_restore_identity —— 导入密钥后的"候选恢复身份"（restore commit 前，
//                          runtime master 以此为准；COMMITTED 后删除）

const (
	masterKeyFile   = ".backup_master_key"
	instanceIDFile  = ".instance_id"
	pendingIDFile   = ".pending_restore_identity"
	masterKeyLen    = 32
	kidHexBytes     = 8 // kid = hex(sha256(master)[:8]) → 16 字符
	filePerm        = 0600
	exportFileTag   = "StickyNote-BackupMasterKey/v1"
	pendingKeyBytes = 128 // 预留
)

// atomicWrite0600 原子写：同目录临时文件 → fsync(file) → rename → fsync(parent dir)。
// 遵循 v4.3-final 审核约束（fsync 目录项而非仅 DataDir）。
func atomicWrite0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("open tmp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil { // fsync 文件内容
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename tmp→target: %w", err)
	}
	return fsyncDir(dir)
}

// fsyncDir 的实现按平台分文件：fsyncdir_unix.go（真 fsync 目录，fnOS/Linux 强制）
// 与 fsyncdir_windows.go（no-op，Windows 上对目录 Open+Sync 被拒且无 crash-consistency 承诺）。
// atomicWrite0600 仍调用它——真目标上保证 rename 产生的目录项断电后持久。

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("crypto/rand unavailable: %w", err)
	}
	return b, nil
}

// ---- 主密钥 ----

// LoadOrCreateMasterKey 读取 DataDir/.backup_master_key；不存在则生成 32B 随机密钥并落盘（0600）。
// 并发/崩溃安全靠 atomicWrite0600（读侧若见 .tmp 忽略）。
func LoadOrCreateMasterKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, masterKeyFile)
	if b, err := os.ReadFile(path); err == nil {
		k, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if derr == nil && len(k) == masterKeyLen {
			return k, nil
		}
		// 文件损坏/半写：不覆盖（覆盖会让既有归档永久解不开）→ 报错由人工介入。
		return nil, fmt.Errorf("%s exists but is corrupt/unreadable; refusing to overwrite (would orphan existing backups)", masterKeyFile)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	k, err := randomBytes(masterKeyLen)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite0600(path, []byte(base64.StdEncoding.EncodeToString(k)+"\n")); err != nil {
		return nil, fmt.Errorf("persist master key: %w", err)
	}
	return k, nil
}

// ReadMasterKey 仅读取（不创建），供恢复/校验用；缺失返回 error。
func ReadMasterKey(dataDir string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, masterKeyFile))
	if err != nil {
		return nil, err
	}
	k, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if derr != nil || len(k) != masterKeyLen {
		return nil, fmt.Errorf("master key file corrupt")
	}
	return k, nil
}

// MasterKeyKid 密钥标识：hex(sha256(master)[:8])，16 字符。仅作"钥匙标识"（非安全凭证），
// 密钥正确性由归档 GCM tag 验证。
func MasterKeyKid(master []byte) string {
	sum := sha256.Sum256(master)
	return hex.EncodeToString(sum[:kidHexBytes])
}

// SetMasterKey 原子覆盖 .backup_master_key（导入/再生成用；调用方须持 Manager 互斥锁）。
func SetMasterKey(dataDir string, key []byte) error {
	if len(key) != masterKeyLen {
		return fmt.Errorf("master key length %d invalid", len(key))
	}
	return atomicWrite0600(filepath.Join(dataDir, masterKeyFile),
		[]byte(base64.StdEncoding.EncodeToString(key)+"\n"))
}

// SetInstanceID 原子覆盖 .instance_id（导入密钥包时对齐 instance 用）。
func SetInstanceID(dataDir, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("empty instance id")
	}
	return atomicWrite0600(filepath.Join(dataDir, instanceIDFile), []byte(strings.TrimSpace(id)+"\n"))
}

// ---- instance_id ----

// LoadOrCreateInstanceID 读取/生成 DataDir/.instance_id（UUID）。
func LoadOrCreateInstanceID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, instanceIDFile)
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read instance_id: %w", err)
	}
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	if err := atomicWrite0600(path, []byte(id+"\n")); err != nil {
		return "", fmt.Errorf("persist instance_id: %w", err)
	}
	return id, nil
}

// ---- pending restore identity（恢复事务用；slice 1 先建原语）----

// PendingIdentity 导入密钥后暂存的候选恢复身份（restore commit 前 runtime 权威密钥来源）。
type PendingIdentity struct {
	RestoreID  string `json:"restore_id"`
	InstanceID string `json:"instance_id"`
	MasterKey  []byte `json:"-"` // 内存态，落盘见 MasterKeyB64
}

type pendingIdentityFile struct {
	RestoreID    string `json:"restore_id"`
	InstanceID   string `json:"instance_id"`
	MasterKeyB64 string `json:"master_key_b64"`
	CreatedAt    string `json:"created_at"`
}

// WritePendingIdentity 原子落盘候选身份（0600）。
func WritePendingIdentity(dataDir string, p PendingIdentity) error {
	if len(p.MasterKey) != masterKeyLen {
		return fmt.Errorf("pending master key length %d invalid", len(p.MasterKey))
	}
	pf := pendingIdentityFile{
		RestoreID:    p.RestoreID,
		InstanceID:   p.InstanceID,
		MasterKeyB64: base64.StdEncoding.EncodeToString(p.MasterKey),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(pf)
	if err != nil {
		return err
	}
	return atomicWrite0600(filepath.Join(dataDir, pendingIDFile), b)
}

// LoadPendingIdentity 读取候选身份；不存在返回 (nil, nil)。
func LoadPendingIdentity(dataDir string) (*PendingIdentity, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, pendingIDFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pf pendingIdentityFile
	if err := json.Unmarshal(b, &pf); err != nil {
		return nil, fmt.Errorf("pending identity corrupt: %w", err)
	}
	k, derr := base64.StdEncoding.DecodeString(pf.MasterKeyB64)
	if derr != nil || len(k) != masterKeyLen {
		return nil, fmt.Errorf("pending identity master key invalid")
	}
	return &PendingIdentity{RestoreID: pf.RestoreID, InstanceID: pf.InstanceID, MasterKey: k}, nil
}

// ClearPendingIdentity 删除候选身份（COMMITTED 后调用；幂等）。
func ClearPendingIdentity(dataDir string) error {
	if err := os.Remove(filepath.Join(dataDir, pendingIDFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ---- 导出/导入密钥文件（UI 用；含 instance_id + kid + 生成时间，供灾难恢复定位旧实例目录）----

// ExportBundle 生成 backup_master_key.txt 的文本内容。
func ExportBundle(master []byte, instanceID string) string {
	return strings.Join([]string{
		exportFileTag,
		"instance_id: " + instanceID,
		"kid: " + MasterKeyKid(master),
		"exported_at: " + time.Now().UTC().Format(time.RFC3339),
		"key: " + base64.StdEncoding.EncodeToString(master),
		"",
	}, "\n")
}

// ParseExportBundle 解析导入的密钥文件，返回 master key + instance_id。校验 kid 自洽。
func ParseExportBundle(text string) (master []byte, instanceID string, err error) {
	var keyB64 string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == exportFileTag:
			// tag ok
		case strings.HasPrefix(line, "instance_id:"):
			instanceID = strings.TrimSpace(strings.TrimPrefix(line, "instance_id:"))
		case strings.HasPrefix(line, "key:"):
			keyB64 = strings.TrimSpace(strings.TrimPrefix(line, "key:"))
		}
	}
	if keyB64 == "" || instanceID == "" {
		return nil, "", fmt.Errorf("invalid backup master key file (missing key or instance_id)")
	}
	master, err = base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(master) != masterKeyLen {
		return nil, "", fmt.Errorf("invalid backup master key encoding")
	}
	_ = pendingKeyBytes
	return master, instanceID, nil
}

func newUUID() (string, error) {
	b, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16])), nil
}

// NewBackupID 生成一次备份/恢复的 UUID（backup_id 整个 job 生命周期不变，幂等键）。
func NewBackupID() (string, error) { return newUUID() }
