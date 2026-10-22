package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 回归：fnOS 容器无系统 /tmp 会让备份自检/在线恢复的 os.MkdirTemp("") 报
// "stat /tmp: no such file or directory"。Load 须在未显式配置 TMPDIR 时把它钉到
// 数据盘下可写目录（见 config.go v21.134 注释）。

func withJWTSecret(t *testing.T) {
	t.Helper()
	orig, had := os.LookupEnv("JWT_SECRET")
	os.Setenv("JWT_SECRET", "test-secret-for-config-load")
	t.Cleanup(func() {
		if had {
			os.Setenv("JWT_SECRET", orig)
		} else {
			os.Unsetenv("JWT_SECRET")
		}
	})
}

func TestLoadPinsTMPDIRWhenUnset(t *testing.T) {
	origTMP, hadTMP := os.LookupEnv("TMPDIR")
	t.Cleanup(func() {
		if hadTMP {
			os.Setenv("TMPDIR", origTMP)
		} else {
			os.Unsetenv("TMPDIR")
		}
	})
	withJWTSecret(t)

	dataDir := t.TempDir()
	os.Unsetenv("TMPDIR")

	cfg := Load(0, dataDir, "", "", "")

	want := filepath.Join(cfg.DataDir, "tmp")
	if got := os.Getenv("TMPDIR"); got != want {
		t.Fatalf("TMPDIR = %q, want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("tmp dir %s not created (err=%v isdir=%v)", want, err, fi != nil && fi.IsDir())
	}
}

func TestLoadRespectsExistingTMPDIR(t *testing.T) {
	origTMP, hadTMP := os.LookupEnv("TMPDIR")
	t.Cleanup(func() {
		if hadTMP {
			os.Setenv("TMPDIR", origTMP)
		} else {
			os.Unsetenv("TMPDIR")
		}
	})
	withJWTSecret(t)

	dataDir := t.TempDir()
	custom := t.TempDir()
	os.Setenv("TMPDIR", custom)

	Load(0, dataDir, "", "", "")

	if got := os.Getenv("TMPDIR"); got != custom {
		t.Fatalf("TMPDIR = %q, want unchanged %q (operator intent must be respected)", got, custom)
	}
	// 已显式配置时不应额外创建 DataDir/tmp。
	if _, err := os.Stat(filepath.Join(dataDir, "tmp")); err == nil {
		t.Fatalf("unexpected %s created when TMPDIR was pre-set", filepath.Join(dataDir, "tmp"))
	}
}
