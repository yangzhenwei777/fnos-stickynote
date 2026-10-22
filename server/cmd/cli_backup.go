package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"stickynote/backup"
)

// 备份/恢复 CLI（v1.0.167 切片 1）。**全部在 database.Init 之前处理**（与 backup-db 同理：
// 恢复要打开"待恢复的旧库"，先 Init 会先把线上库迁移再恢复 → 语义错乱）。
//
//   sticky-note backup <out.snbak> [source.db]        产出一份本地加密 .snbak
//   sticky-note verify-backup <in.snbak>               解开校验（GCM+AAD+逐文件哈希+integrity）
//   sticky-note restore-backup <in.snbak> <out-dir>    离线**校验+解包到 out-dir**（不覆盖线上）
//
// 完整的"崩溃可恢复的原子换库"属切片 3 的 server/restore 事务；本 CLI 只做**只读校验与解包**，
// 灾备换库由停服后人工/后续 Web 恢复执行——与既有 backup-db「只产快照不自动覆盖」的一贯风格一致。

// backupEnv 从环境变量/参数推导备份运行环境（不触发 config.Load 的 MkdirAll 副作用）。
func backupEnv(srcDB string) (backup.Env, error) {
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	if srcDB != "" {
		dataDir = filepath.Dir(srcDB)
	}
	uploadDir := os.Getenv("UPLOAD_DIR")
	if uploadDir == "" {
		uploadDir = filepath.Join(dataDir, "upload")
	}
	ver := os.Getenv("APP_VERSION")
	if ver == "" {
		ver = appVersion
	}
	return backup.Env{DataDir: dataDir, UploadDir: uploadDir, AppVersion: strings.TrimPrefix(ver, "v"), SchemaVer: backupSchemaVersion}, nil
}

// backupSchemaVersion 是备份归档自描述用的 schema 整数（建备份表时=1；后续结构变更递增）。
const backupSchemaVersion = 1

// runBackupCLI 处理 backup / verify-backup / restore-backup，返回退出码。
func runBackupCLI(args []string) int {
	switch args[0] {
	case "backup":
		return cliCreateBackup(args[1:])
	case "verify-backup":
		return cliVerifyBackup(args[1:])
	case "restore-backup":
		return cliRestoreBackup(args[1:])
	}
	return 1
}

func cliCreateBackup(rest []string) int {
	if len(rest) < 1 || rest[0] == "" {
		fmt.Println("Usage: sticky-note backup <out.snbak> [source.db]")
		return 1
	}
	out := rest[0]
	var srcDB string
	if len(rest) > 1 {
		srcDB = rest[1]
	}
	env, err := backupEnv(srcDB)
	if err != nil {
		fmt.Println("Error:", err)
		return 1
	}
	unlock, err := acquireBackupLock(env.DataDir)
	if err != nil {
		fmt.Println("Error:", err)
		return 1
	}
	defer unlock()

	bid, err := backup.NewBackupID()
	if err != nil {
		fmt.Println("Error: gen backup id:", err)
		return 1
	}
	res, err := backup.CreateBackup(env, bid, false)
	if err != nil {
		fmt.Println("Error: create backup:", err)
		return 1
	}
	// CreateBackup 落在 tmp 目录；移动到目标路径
	if err := moveFile(res.SnbakPath, out); err != nil {
		fmt.Println("Error: move archive to target:", err)
		return 1
	}
	// 产档后自检（解不开就不算成功）
	if _, err := backup.VerifyArchive(env, out); err != nil {
		fmt.Println("Error: created archive failed self-verify:", err)
		return 1
	}
	fmt.Printf("Backup created: %s (%d bytes, sha256=%s…)\n", out, res.SnbakSize, res.SnbakSha256[:16])
	return 0
}

func cliVerifyBackup(rest []string) int {
	if len(rest) < 1 || rest[0] == "" {
		fmt.Println("Usage: sticky-note verify-backup <in.snbak>")
		return 1
	}
	env, err := backupEnv("")
	if err != nil {
		fmt.Println("Error:", err)
		return 1
	}
	man, err := backup.VerifyArchive(env, rest[0])
	if err != nil {
		fmt.Println("FAIL:", err)
		return 1
	}
	fmt.Printf("OK: backup_id=%s instance_id=%s app=%s users=%d notes=%d attachments=%d\n",
		man.BackupID, man.InstanceID, man.AppVersion, man.Stats.UserCount, man.Stats.NoteCount, man.Stats.AttachmentCount)
	return 0
}

func cliRestoreBackup(rest []string) int {
	if len(rest) < 2 || rest[0] == "" || rest[1] == "" {
		fmt.Println("Usage: sticky-note restore-backup <in.snbak> <extract-dir>")
		fmt.Println("  仅校验并解包 notepad.db / jwt_secret.enc / uploads 到 extract-dir，不覆盖线上数据。")
		fmt.Println("  换库=停服后把解出的文件移回数据目录，或等 Web 恢复（切片 3）。")
		return 1
	}
	env, err := backupEnv("")
	if err != nil {
		fmt.Println("Error:", err)
		return 1
	}
	man, err := backup.VerifyArchive(env, rest[0])
	if err != nil {
		fmt.Println("FAIL: archive not verifiable, refusing to extract:", err)
		return 1
	}
	if err := backup.ExtractArchiveToFile(env, rest[0], rest[1]); err != nil {
		fmt.Println("Error: extract:", err)
		return 1
	}
	fmt.Printf("Extracted (verified) archive backup_id=%s to %s — review then move into data dir while server is STOPPED.\n",
		man.BackupID, rest[1])
	return 0
}

// acquireBackupLock 轻量互斥（Grok §8：防与存活 server / 并发 CLI 同时跑）。
// 用 O_CREATE|O_EXCL 锁文件承载；调用方 defer unlock 释放。
func acquireBackupLock(dataDir string) (func(), error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dataDir, ".backup.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another backup/restore appears to be running (%s exists); if stale, remove it", lockPath)
		}
		return nil, err
	}
	_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
	_ = f.Close()
	return func() { os.Remove(lockPath) }, nil
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// 跨盘 rename 失败 → 复制后删源
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}
