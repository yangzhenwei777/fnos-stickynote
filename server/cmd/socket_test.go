package cmd

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// skipIfLongPath Windows AF_UNIX 的 sockaddr_un 路径上限 108 字节，
// 临时目录路径过长时跳过（Linux CI/真机不受影响，真机矩阵覆盖完整链）。
func skipIfLongPath(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" && len(path) >= 104 {
		t.Skipf("windows AF_UNIX path too long (%d bytes): %s", len(path), path)
	}
}

func TestDefaultSocketPath(t *testing.T) {
	t.Setenv("TRIM_APPDEST", "/var/apps/stickynote")
	if got := DefaultSocketPath(); got != filepath.Join("/var/apps/stickynote", "app.sock") {
		t.Fatalf("DefaultSocketPath with TRIM_APPDEST = %q", got)
	}
	t.Setenv("TRIM_APPDEST", "   ")
	if got := DefaultSocketPath(); got != "" {
		t.Fatalf("DefaultSocketPath with blank TRIM_APPDEST = %q, want empty", got)
	}
	t.Setenv("TRIM_APPDEST", "")
	if got := DefaultSocketPath(); got != "" {
		t.Fatalf("DefaultSocketPath without TRIM_APPDEST = %q, want empty", got)
	}
}

func TestListenUnixSocketCreatesAndChmods(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.sock")
	skipIfLongPath(t, path)

	ln, err := listenUnixSocket(path)
	if err != nil {
		t.Fatalf("listenUnixSocket: %v", err)
	}
	defer ln.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file missing after listen: %v", err)
	}
	if addr, ok := ln.Addr().(*net.UnixAddr); !ok || addr.Name != path {
		t.Fatalf("listener addr = %v, want unix %s", ln.Addr(), path)
	}
	// 权限断言仅在 POSIX 有意义（Windows chmod 为 no-op）。
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat socket: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o770 {
			t.Fatalf("socket perm = %o, want 770", perm)
		}
	}
}

func TestListenUnixSocketCleansStaleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.sock")
	skipIfLongPath(t, path)

	// 模拟异常退出遗留的 socket 文件（普通文件同型占位）。
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}
	ln, err := listenUnixSocket(path)
	if err != nil {
		t.Fatalf("listenUnixSocket should remove stale file first: %v", err)
	}
	defer ln.Close()
}

func TestListenUnixSocketFailsWhenParentMissing(t *testing.T) {
	// 父目录不存在：Remove 报 not exist（通过），Listen 必败——fail-closed 契约。
	path := filepath.Join(t.TempDir(), "no-such-dir", "app.sock")
	if _, err := listenUnixSocket(path); err == nil {
		t.Fatal("listenUnixSocket should fail when parent dir missing")
	}
}

// TestSocketHTTPRoundTrip 方案 §3.1 Go 侧用例：经 unix socket 发 HTTP 请求，
// 直打健康端点断言 200——网关转发链路的最后一跳（socket → handler）等价验证。
func TestSocketHTTPRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.sock")
	skipIfLongPath(t, path)

	ln, err := listenUnixSocket(path)
	if err != nil {
		t.Fatalf("listenUnixSocket: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		cleanupUnixSocket(path)
	}()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket file missing: %v", err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
	resp, err := client.Get("http://stub/api/health")
	if err != nil {
		t.Fatalf("GET via unix socket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCleanupUnixSocketIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.sock")
	cleanupUnixSocket(path) // 不存在 → 静默通过
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupUnixSocket(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket file should be removed, stat err = %v", err)
	}
	cleanupUnixSocket("") // 空路径 → 静默通过
}
