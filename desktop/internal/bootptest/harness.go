// Package bootptest 批次 5 桌面集成测试共享工装：进程外服务端（cmd/snboot-testserver）。
// 存在理由：同进程 httptest 会让 syncsrv 与桌面共用 database.DB 全局（句柄乒乓产生
// 假通过路径）。仅供测试 import，不进交付面。
package bootptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 进程外服务端工装（cmd/snboot-testserver）：真 HTTP + 独立库进程。----
// 同进程 httptest 会让 syncsrv 与桌面共用 database.DB 全局（句柄乒乓会产生假通过路径），
// 批次 5 集成面一律走子进程。

// Server 一个跑着的进程外服务端。URL 为客户端应连的地址；data/addr 固定后可 Restart
// （断网-回归用例要求"同一个 URL、同一份库"回来）。
type Server struct {
	URL   string
	data  string
	addr  string
	cmd   *exec.Cmd
	alive bool
}

// FreeAddr 探一个当前空闲的 127.0.0.1 端口（交给 StartAt，之后可原地 Restart）。
func FreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探空闲端口失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// Start 临时数据目录 + 随机端口（批次 5 起的默认形态）。
func Start(t *testing.T) *Server {
	t.Helper()
	return start(t, t.TempDir(), "127.0.0.1:0")
}

// StartAt 固定数据目录与监听地址。测试须先建好 dataDir（其删除清理会晚于本函数注册的
// 杀进程清理，Windows 才删得掉库文件）。
func StartAt(t *testing.T, dataDir, addr string) *Server {
	t.Helper()
	return start(t, dataDir, addr)
}

// Restart 以同址同库重新拉起（模拟网络恢复/服务端回归）。
func (ts *Server) Restart(t *testing.T) {
	t.Helper()
	ts.Stop()
	*ts = *start(t, ts.data, ts.addr)
}

// Stop 杀掉服务端进程（模拟断网）。幂等。
func (ts *Server) Stop() {
	if ts == nil || ts.cmd == nil || ts.cmd.Process == nil || !ts.alive {
		return
	}
	_ = ts.cmd.Process.Kill()
	_ = ts.cmd.Wait()
	ts.alive = false
}

// start 拉起子进程并等它自报 ADDR。固定地址时自带 bind 重试（见 testserver main）。
func start(t *testing.T, data, addr string) *Server {
	t.Helper()
	bin := buildTestServer(t)
	cmd := exec.Command(bin, "-data", data, "-addr", addr)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ts := &Server{data: data, addr: addr, cmd: cmd, alive: true}
	t.Cleanup(ts.Stop)
	br := bufio.NewReader(stdout)
	var base string
	for i := 0; i < 200; i++ {
		n, _, rerr := br.ReadLine()
		if rerr != nil {
			t.Fatalf("testserver stdout: %v", rerr)
		}
		if strings.HasPrefix(string(n), "ADDR http://") {
			base = strings.TrimPrefix(string(n), "ADDR ")
			break
		}
	}
	if base == "" {
		t.Fatal("testserver ADDR line not found")
	}
	u, err := url.Parse(base)
	if err == nil {
		ts.addr = u.Host // 随机端口时把真实地址钉下来，供后续 Restart 复用
	}
	ts.URL = base
	for i := 0; i < 50; i++ {
		if resp, err := http.Get(base + "/api/sync/state"); err == nil {
			resp.Body.Close()
			return ts
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("testserver 未在预期时间内就绪")
	return nil
}

var (
	tsBuildOnce sync.Once
	tsBin       string
	tsBinErr    error
)

// moduleRoot 向上找含 cmd/snboot-testserver 的目录（desktop 模块根）。
func moduleRoot() string {
	cwd, _ := os.Getwd()
	for d := cwd; ; {
		if st, err := os.Stat(filepath.Join(d, "cmd", "snboot-testserver")); err == nil && st.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return cwd
		}
		d = parent
	}
}

func buildTestServer(t *testing.T) string {
	t.Helper()
	tsBuildOnce.Do(func() {
		bin := filepath.Join(os.TempDir(), "snboot-testserver.exe")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/snboot-testserver")
		cmd.Dir = moduleRoot() // 兼容从模块根本包或子包目录运行 go test
		if out, err := cmd.CombinedOutput(); err != nil {
			tsBinErr = fmt.Errorf("build testserver: %v | %s", err, out)
			return
		}
		tsBin = bin
	})
	if tsBinErr != nil {
		t.Skipf("testserver build unavailable: %v", tsBinErr)
	}
	return tsBin
}

func (ts *Server) Seed(t *testing.T, title string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/test/seed?title="+url.QueryEscape(title), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("seed status %d", resp.StatusCode)
	}
}

func (ts *Server) ResetEpoch(t *testing.T) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/test/reset-epoch", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("reset-epoch status %d", resp.StatusCode)
	}
}

// Token 领取服务端签发的真 access token（sync 端点鉴权中间件校验 JWT）。
func (ts *Server) Token(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/test/token")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.AccessToken == "" {
		t.Fatal("token unavailable")
	}
	return out.AccessToken
}

func (ts *Server) HasNote(t *testing.T, title string) int {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/test/has-note?title=" + url.QueryEscape(title))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Count int `json:"count"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		t.Fatal("decode")
	}
	return out.Count
}
