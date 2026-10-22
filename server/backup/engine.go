package backup

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stickynote/crypto"
)

// Env 是备份引擎运行所需的路径/版本注入（不直接依赖 config 包，便于测试）。
type Env struct {
	DataDir    string // 含 notepad.db、.jwt_secret、.backup_master_key、.instance_id、sync-tmp/
	UploadDir  string // 附件目录（fpk 是 data 的兄弟目录，非 data/upload）
	AppVersion string // 如 1.0.167
	SchemaVer  int    // 递增 schema 整数（建备份表时的值）
	ArchiveCap int64  // 归档大小帽（<=0 用 DefaultArchiveCapBytes）
}

func (e Env) dbPath() string { return filepath.Join(e.DataDir, "notepad.db") }
func (e Env) tmpDir() string { return filepath.Join(e.DataDir, "sync-tmp") }
func (e Env) capOrDefault() int64 {
	if e.ArchiveCap > 0 {
		return e.ArchiveCap
	}
	return DefaultArchiveCapBytes
}

// Result 一次本地备份的产物（供上传层/记录层使用）。
type Result struct {
	BackupID       string
	SnbakPath      string // 落盘的 .snbak（staging 目录），上传成功后由上层移动/清理
	SnbakSha256    string
	SnbakSize      int64
	ArchiveKey     string // 归档内 manifest 摘要（调试用）
	Index          ExternalIndex
	Stats          struct{ UserCount, NoteCount, AttachmentCount int }
	Incomplete     bool // true = 有引用附件缺失但被显式允许降级生成（不可用于灾备恢复）
	MissingUploads []string
}

// CreateBackup 在 GC 暂停窗口内产出一个本地加密 .snbak（切片 1：不触网，仅产文件 + 外部 index 结构）。
// 步骤遵循 v4.3-final A1/A3/A5：SnapshotTo(独立连接) → 从快照读引用附件+stats（同一时间点）→
// 校验附件存在（缺则 fail-closed，allowMissing=true 才降级 INCOMPLETE）→ jwt 包裹 → 内部 manifest
// （含 instance_id、不含 archive_sha256、不含 restore_generation）→ tar.gz → EnvelopeSeal(整文件 GCM+AAD)。
func CreateBackup(env Env, backupID string, allowMissing bool) (*Result, error) {
	if backupID == "" {
		return nil, fmt.Errorf("CreateBackup: empty backupID")
	}
	master, err := LoadOrCreateMasterKey(env.DataDir)
	if err != nil {
		return nil, err
	}
	instID, err := LoadOrCreateInstanceID(env.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(env.tmpDir(), 0700); err != nil {
		return nil, fmt.Errorf("mkdir tmp: %w", err)
	}

	var res *Result
	// 采集窗口持 GC 锁：暂停 GC/物理删除，但不挡上传/笔记保存（v4.3-final A3）。
	err = WithGCPaused(func() error {
		snapPath := filepath.Join(env.tmpDir(), backupID+".db")
		defer os.Remove(snapPath)
		if err := SnapshotTo(snapPath, env.dbPath()); err != nil {
			return err
		}
		files, missing, stats, err := collectFromSnapshot(snapPath, env.UploadDir)
		if err != nil {
			return err
		}
		if len(missing) > 0 && !allowMissing {
			return fmt.Errorf("%w: %d referenced attachment(s) missing on disk", ErrAttachmentMissing, len(missing))
		}
		// jwt_secret 单独加密进包（恢复后 auth.Init 重读；TOTP 依赖链）。
		jwtEnc, err := wrapJWTSecret(env.DataDir, master)
		if err != nil {
			return err
		}
		// 文件哈希 → 内部 manifest（含 stats、instance_id；不含 archive_sha256/generation）→ 打包 → 信封。
		dbSha, dbSize, err := hashFile(snapPath)
		if err != nil {
			return err
		}
		man := InternalManifest{
			FormatVersion: 1, BackupID: backupID, InstanceID: instID,
			CreatedAt: time.Now().UTC().Format(time.RFC3339), Trigger: "manual",
			AppVersion: env.AppVersion, SchemaVersion: env.SchemaVer, Enc: envEncV1,
			ServerEpoch:       stats.ServerEpoch, // R0：来自快照同源（上方 collectFromSnapshot）
			JWTSecretIncluded: len(jwtEnc) > 0,
			FileHashes:        map[string]string{},
		}
		man.Database.Path = arcDB
		man.Database.Sha256 = dbSha
		man.Database.Size = dbSize
		man.FileHashes[arcDB] = dbSha
		if len(jwtEnc) > 0 {
			man.FileHashes[arcJWT] = sha256Bytes(jwtEnc)
		}
		for _, f := range files {
			man.Attachments.Files = append(man.Attachments.Files, AttachmentEntry{Name: f.Name, Sha256: f.Sha256, Size: f.Size})
			man.FileHashes[arcUploads+f.Name] = f.Sha256
		}
		man.Attachments.Count = int64(len(files))
		var tot int64
		for _, f := range files {
			tot += f.Size
		}
		man.Attachments.TotalBytes = tot
		man.Stats.UserCount, man.Stats.NoteCount, man.Stats.AttachmentCount = stats.UserCount, stats.NoteCount, len(files)

		manBytes, err := marshalIndent(man)
		if err != nil {
			return err
		}
		entries := make([]FileEntry, 0, len(files))
		for _, f := range files {
			entries = append(entries, FileEntry{Name: f.Name, AbsPath: f.AbsPath})
		}
		payload, err := BuildArchive(ArchiveInput{
			ManifestBytes: manBytes, DBPath: snapPath, JWTSecretEnc: jwtEnc, Uploads: entries,
		})
		if err != nil {
			return err
		}
		sealed, err := EnvelopeSeal(payload, backupID, master, env.capOrDefault())
		if err != nil {
			return err
		}
		outPath := filepath.Join(env.tmpDir(), backupID+".snbak")
		f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("create .snbak: %w", err)
		}
		if _, err := f.Write(sealed); err != nil {
			f.Close()
			return fmt.Errorf("write .snbak: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close .snbak: %w", err)
		}
		// 写盘后按实际文件字节算 archive sha（外部 index 用；payload hash 不能代表 .snbak）。
		archSha, archSize, err := hashFile(outPath)
		if err != nil {
			return err
		}
		idx := ExternalIndex{
			FormatVersion: 1, BackupID: backupID, InstanceID: instID,
			CreatedAt: man.CreatedAt, AppVersion: env.AppVersion, SchemaVersion: env.SchemaVer,
			EncAlgo: "AES-256-GCM", Enc: envEncV1, Kid: MasterKeyKid(master),
			ArchiveSizeBytes: archSize, ArchiveSha256: archSha,
		}
		res = &Result{
			BackupID: backupID, SnbakPath: outPath, SnbakSha256: archSha, SnbakSize: archSize,
			Index: idx, MissingUploads: missing, Incomplete: len(missing) > 0 && allowMissing,
		}
		res.Stats.UserCount, res.Stats.NoteCount, res.Stats.AttachmentCount = stats.UserCount, stats.NoteCount, len(files)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// fileOnDisk 采集到的一个待打包附件。
type fileOnDisk struct {
	Name, AbsPath, Sha256 string
	Size                  int64
}

type snapshotStats struct {
	UserCount, NoteCount int
	ServerEpoch          int64 // v1.0.175 R0：冻结快照库内的代际（legacy 快照无表/无行=0）
}

// collectFromSnapshot 从一致性快照读"引用附件集合 + stats + server_epoch"（与快照同一时间点），
// 逐个核附件是否存在于 uploadDir；返回 (present, missing, stats, err)。
func collectFromSnapshot(snapPath, uploadDir string) ([]fileOnDisk, []string, snapshotStats, error) {
	db, err := sql.Open("sqlite", "file:"+snapPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, nil, snapshotStats{}, fmt.Errorf("open snapshot ro: %w", err)
	}
	defer db.Close()

	var st snapshotStats
	_ = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&st.UserCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM notes WHERE deleted_at IS NULL").Scan(&st.NoteCount)
	// epoch 与包内容同快照同源（v20 §75）：只读快照库，绝不读 live。
	// 失败（含 legacy 库无 sync_meta 表）容忍为 0——恢复侧 max(pre,0)+1 仍单调。
	_ = db.QueryRow("SELECT server_epoch FROM sync_meta WHERE id = 1").Scan(&st.ServerEpoch)

	// 引用集 = note_uploads 关联的文件 ∪ uploads.status='attached'（加密笔记也走 note_uploads）。
	rows, err := db.Query(`SELECT DISTINCT filename FROM note_uploads
		UNION SELECT filename FROM uploads WHERE status='attached'`)
	if err != nil {
		// note_uploads 老库可能未建/未回填；退回仅 uploads.attached
		rows, err = db.Query(`SELECT filename FROM uploads WHERE status='attached'`)
		if err != nil {
			return nil, nil, st, fmt.Errorf("query attachments: %w", err)
		}
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil && n != "" {
			names = append(names, n)
		}
	}

	var present []fileOnDisk
	var missing []string
	for _, n := range names {
		// 附件名是受控 UUID basename（upload.go:92），仍防穿越。
		base := filepath.Base(n)
		if base != n || strings.ContainsAny(n, `/\`) {
			missing = append(missing, n)
			continue
		}
		p := filepath.Join(uploadDir, base)
		sha, size, herr := hashFile(p)
		if herr != nil {
			missing = append(missing, n)
			continue
		}
		present = append(present, fileOnDisk{Name: base, AbsPath: p, Sha256: sha, Size: size})
	}
	return present, missing, st, nil
}

// wrapJWTSecret 读 DataDir/.jwt_secret 并用派生子密钥 EncryptRaw → jwt_secret.enc 字节。
// 无 .jwt_secret（用 env JWT_SECRET 的部署）→ 返回 nil（恢复时该实例本就靠 env，不阻断备份）。
func wrapJWTSecret(dataDir string, master []byte) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, ".jwt_secret"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read jwt secret: %w", err)
	}
	content := []byte(strings.TrimRight(string(b), "\n"))
	if len(content) == 0 {
		return nil, nil
	}
	wk, err := crypto.DeriveKey(master, "jwt-secret-wrap")
	if err != nil {
		return nil, fmt.Errorf("derive jwt wrap key: %w", err)
	}
	enc, err := crypto.EncryptRaw(content, wk)
	if err != nil {
		return nil, fmt.Errorf("encrypt jwt secret: %w", err)
	}
	return []byte(enc), nil
}

// UnwrapJWTSecret 反向：解密归档里的 jwt_secret.enc（恢复用，slice 3）。slice 1 提供原语+单测。
func UnwrapJWTSecret(jwtEnc []byte, master []byte) ([]byte, error) {
	wk, err := crypto.DeriveKey(master, "jwt-secret-wrap")
	if err != nil {
		return nil, err
	}
	return crypto.DecryptRaw(string(jwtEnc), wk)
}
