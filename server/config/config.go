package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Port            int
	DataDir         string
	WebDir          string
	UploadDir       string
	ShareDirs       string // ⚠️ 死参数（v21.57 P5 审计确认）：-share-dirs 历史兼容保留，本项目从不消费，不参与文件访问授权
	JWTSecret       string
	JWTSecretSource string // "env", "file", "generated"
}

func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "notepad.db")
}

func (c *Config) LogDir() string {
	return filepath.Join(c.DataDir, "logs")
}

func Load(port int, dataDir, webDir, uploadDir, shareDirs string) *Config {
	cfg := &Config{
		Port:      port,
		DataDir:   dataDir,
		WebDir:    webDir,
		UploadDir: uploadDir,
		ShareDirs: shareDirs,
	}

	if cfg.Port <= 0 {
		if v := os.Getenv("PORT"); v != "" {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				cfg.Port = p
			}
		}
		if cfg.Port <= 0 {
			cfg.Port = 13389
		}
	}

	if cfg.DataDir == "" {
		if v := os.Getenv("DATA_DIR"); v != "" {
			cfg.DataDir = v
		} else {
			cfg.DataDir = "./data"
		}
	}

	if cfg.WebDir == "" {
		if v := os.Getenv("WEB_DIR"); v != "" {
			cfg.WebDir = v
		} else {
			cfg.WebDir = "./www"
		}
	}

	if cfg.UploadDir == "" {
		if v := os.Getenv("UPLOAD_DIR"); v != "" {
			cfg.UploadDir = v
		} else {
			cfg.UploadDir = filepath.Join(cfg.DataDir, "upload")
		}
	}

	if cfg.ShareDirs == "" {
		if v := os.Getenv("SHARE_DIRS"); v != "" {
			cfg.ShareDirs = v
		}
	}

	if v := os.Getenv("JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
		cfg.JWTSecretSource = "env"
	}

	// 创建数据目录结构（v21.52 P13 审计修复：目录创建失败不应继续以不确定状态运行）
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		fmt.Printf("[FATAL] create data dir %s: %v\n", cfg.DataDir, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "logs"), 0700); err != nil {
		fmt.Printf("[FATAL] create logs dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.UploadDir, 0700); err != nil {
		fmt.Printf("[FATAL] create upload dir %s: %v\n", cfg.UploadDir, err)
		os.Exit(1)
	}

	// v21.134：fnOS 应用容器可能没有系统 /tmp，而备份产档自检（backup/verify.go）与在线恢复
	//（restore/restore.go 两处）都用 os.MkdirTemp("")——它经 os.TempDir() 在 Unix 上回退到 "/tmp"，
	// 目录缺失即报 "stat /tmp: no such file or directory"，导致备份/恢复整体不可用（与 WebDAV 服务商无关，
	// 自检发生在上传之前）。未显式配置 TMPDIR 时，把临时目录固定到数据盘下的可写目录并设入 TMPDIR，
	// 一并覆盖这三处及任何依赖系统临时目录的库（如大附件 multipart 溢写）。已显式设置则尊重运维意图不覆盖。
	// fail-closed 与上方目录创建同范式：建不出来即存储故障，拒绝以不确定状态启动。
	if os.Getenv("TMPDIR") == "" {
		tmpDir := filepath.Join(cfg.DataDir, "tmp")
		if err := os.MkdirAll(tmpDir, 0700); err != nil {
			fmt.Printf("[FATAL] create tmp dir %s: %v\n", tmpDir, err)
			os.Exit(1)
		}
		if err := os.Setenv("TMPDIR", tmpDir); err != nil {
			fmt.Printf("[FATAL] set TMPDIR=%s: %v\n", tmpDir, err)
			os.Exit(1)
		}
	}

	if cfg.JWTSecret == "" {
		cfg.JWTSecret = loadOrCreateJWTSecret(cfg.DataDir)
		if _, err := os.ReadFile(filepath.Join(cfg.DataDir, ".jwt_secret")); err == nil {
			cfg.JWTSecretSource = "file"
		} else {
			cfg.JWTSecretSource = "generated"
		}
	}

	return cfg
}

// loadOrCreateJWTSecret 持久化 JWT 密钥：环境变量 JWT_SECRET 未设置时，
// 优先复用数据目录下的 .jwt_secret 文件；不存在则随机生成并写入（0600 原子写）。
// 避免此前"每次重启随机生成密钥导致全部 token 失效、用户被强制下线"的问题。
// v21.90 P2（审计低危项）：改为临时文件 + rename 原子写入——原 os.WriteFile 非原子，
// 并发启动可能读到半写文件（supervisor 串行启动已缓解，但防御纵深补齐）。
// v21.133 GPT-10（审计中危）：持久化失败在生产模式 fail-closed——密钥写不进磁盘意味着
// 存储故障，若继续用内存临时密钥，重启后所有 token 静默失效、用户被强制下线且无从察觉。
// 因此默认 [FATAL] 退出（与本文件 upload 目录创建失败同范式）。仅开发/测试可用
// NOTEPAD_ALLOW_EPHEMERAL_JWT=1 显式降级为临时密钥（[WARN] 不退出）。
func loadOrCreateJWTSecret(dataDir string) string {
	secretFile := filepath.Join(dataDir, ".jwt_secret")
	if b, err := os.ReadFile(secretFile); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	secret := generateRandomSecret()
	tmpFile := secretFile + ".tmp"
	persistErr := os.WriteFile(tmpFile, []byte(secret+"\n"), 0600)
	if persistErr == nil {
		if err := os.Rename(tmpFile, secretFile); err != nil {
			persistErr = err
		}
	}
	if persistErr != nil {
		os.Remove(tmpFile)
		if os.Getenv("NOTEPAD_ALLOW_EPHEMERAL_JWT") == "1" {
			fmt.Println("[WARN] persist JWT secret failed:", persistErr, "(ephemeral secret allowed via NOTEPAD_ALLOW_EPHEMERAL_JWT=1; tokens invalid on restart)")
			return secret
		}
		fmt.Printf("[FATAL] persist JWT secret to %s failed: %v — refusing to start with an ephemeral key (all tokens would silently invalidate on restart). Fix DATA_DIR writability, or set JWT_SECRET, or (dev/test only) NOTEPAD_ALLOW_EPHEMERAL_JWT=1.\n", secretFile, persistErr)
		os.Exit(1)
	}
	return secret
}

// ShareDirPaths 已随 v18.4 死代码清理移除：ShareDirs 是上游遗留的 CLI 死参数，
// 仅为兼容 fnOS 启动命令而保留接收（见 main.go），从无任何消费代码。

func generateRandomSecret() string {
	b := make([]byte, 32)
	// v21.52 P12 审计修复：crypto/rand 失败时不能静默返回零值密钥（会导致可预测 JWT secret）。
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
