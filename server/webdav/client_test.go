package webdav

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/webdav"
)

// newFakeServer 起一个内存 WebDAV 假服务端（httptest，监听 127.0.0.1）。
// needAuth=true 时强制 Basic 凭据 user/pass。
func newFakeServer(t *testing.T, needAuth bool) *httptest.Server {
	t.Helper()
	h := &webdav.Handler{
		Prefix:     "/dav",
		FileSystem: webdav.NewMemFS(),
		LockSystem: webdav.NewMemLS(),
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if needAuth {
			u, p, ok := r.BasicAuth()
			if !ok || u != "user" || p != "pass" {
				w.Header().Set("WWW-Authenticate", `Basic realm="stickynote"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clientFor(srv *httptest.Server, opt Options) *Client {
	opt.URL = strings.TrimRight(srv.URL, "/") + "/dav/"
	c, err := New(opt)
	if err != nil {
		panic(err)
	}
	return c
}

func TestClientPrimitives(t *testing.T) {
	srv := newFakeServer(t, false)
	c := clientFor(srv, Options{AllowLoopback: true})

	if err := c.Test(); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if err := c.MkdirAll("/bk"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	payload := []byte("hello stickynote backup")
	if err := c.PutBytes("/bk/a.txt", payload); err != nil {
		t.Fatalf("PutBytes: %v", err)
	}
	got, err := c.Get("/bk/a.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("Get mismatch: %q", got)
	}
	size, exists, err := c.Head("/bk/a.txt")
	if err != nil || !exists {
		t.Fatalf("Head: exists=%v err=%v", exists, err)
	}
	if size != int64(len(payload)) {
		t.Fatalf("Head size=%d want %d", size, len(payload))
	}
	// Head 不存在 → exists=false, err=nil
	_, exists, err = c.Head("/bk/nope.txt")
	if err != nil || exists {
		t.Fatalf("Head(missing) exists=%v err=%v", exists, err)
	}
	names, err := c.ListNames("/bk")
	if err != nil {
		t.Fatalf("ListNames: %v", err)
	}
	if len(names) != 1 || names[0] != "a.txt" {
		t.Fatalf("ListNames=%v", names)
	}
	// Move a.txt → b.txt
	if err := c.Move("/bk/a.txt", "/bk/b.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, ok, _ := c.Head("/bk/a.txt"); ok {
		t.Fatal("源文件 MOVE 后仍存在")
	}
	if _, ok, _ := c.Head("/bk/b.txt"); !ok {
		t.Fatal("目标文件 MOVE 后不存在")
	}
	// Delete
	if err := c.Delete("/bk/b.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := c.Head("/bk/b.txt"); ok {
		t.Fatal("Delete 后仍存在")
	}
}

func TestPutLocalVerified(t *testing.T) {
	srv := newFakeServer(t, false)
	c := clientFor(srv, Options{AllowLoopback: true})
	if err := c.MkdirAll("/bk"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "snap.snbak")
	data := bytes.Repeat([]byte("X"), 4096)
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := c.PutLocalVerified(src, "/bk/snap.snbak", "/bk/snap.snbak.part-1")
	if err != nil {
		t.Fatalf("PutLocalVerified: %v", err)
	}
	if got != int64(len(data)) {
		t.Fatalf("verified size=%d want %d", got, len(data))
	}
	body, err := c.Get("/bk/snap.snbak")
	if err != nil || !bytes.Equal(body, data) {
		t.Fatalf("远端内容不符 err=%v len=%d", err, len(body))
	}
}

func TestProbeMoveReportsSupport(t *testing.T) {
	srv := newFakeServer(t, false)
	c := clientFor(srv, Options{AllowLoopback: true})
	if err := c.MkdirAll("/bk"); err != nil {
		t.Fatal(err)
	}
	cap := c.ProbeMove("/bk")
	if !cap.SupportsMove {
		t.Fatal("x/net/webdav 支持 MOVE，ProbeMove 误报不支持")
	}
	// 探针应清理干净，目录仍空
	names, err := c.ListNames("/bk")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("探针未清理: %v", names)
	}
}

func TestAuthFailureClassified(t *testing.T) {
	srv := newFakeServer(t, true) // 需要 user/pass
	c := clientFor(srv, Options{AllowLoopback: true, User: "user", Pass: "wrong"})
	err := c.Test()
	if err == nil {
		t.Fatal("错误密码应鉴权失败")
	}
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("应归类为 ErrAuth，得 %v", err)
	}
	// 正确凭据可通过
	ok := clientFor(srv, Options{AllowLoopback: true, User: "user", Pass: "pass"})
	if err := ok.Test(); err != nil {
		t.Fatalf("正确凭据 Test 失败: %v", err)
	}
}

func TestSSRFBlocksLoopbackWhenDisallowed(t *testing.T) {
	srv := newFakeServer(t, false) // httptest 在 127.0.0.1
	c := clientFor(srv, Options{AllowLoopback: false})
	err := c.Test()
	if err == nil {
		t.Fatal("禁环回时连 127.0.0.1 应被 SSRF 护栏拦截")
	}
	if !errors.Is(err, ErrBlockedAddr) {
		t.Fatalf("应归类 ErrBlockedAddr，得 %v", err)
	}
}

func TestSchemeRejected(t *testing.T) {
	for _, bad := range []string{"file:///etc/passwd", "gopher://x", "ftp://y"} {
		if _, err := New(Options{URL: bad}); !errors.Is(err, ErrBlockedURL) {
			t.Fatalf("%s 应被拒（ErrBlockedURL），得 %v", bad, err)
		}
	}
}

// TestIsCloudMetadata（v21.133 L-03）：SSRF 云元数据段拦截。含阿里云 IMDS 所在的
// 100.64.0.0/10（RFC6598，非 ip.IsPrivate 覆盖）；并反向断言段外/公网/私网不被本函数误伤。
func TestIsCloudMetadata(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
		desc string
	}{
		{"169.254.169.254", true, "AWS/GCP/OpenStack IMDS"},
		{"100.100.100.200", true, "阿里云 IMDS（100.64/10 段内）"},
		{"100.64.0.1", true, "100.64/10 下界"},
		{"100.127.255.255", true, "100.64/10 上界"},
		{"fd00:ec2::254", true, "IMDS IPv6"},
		{"100.128.0.1", false, "紧邻段外（/10 之上），非元数据"},
		{"8.8.8.8", false, "公网"},
		{"192.0.2.38", false, "RFC1918 私网（由 allowPrivate 策略处理，非本函数）"},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("非法测试 IP %q", c.ip)
		}
		if got := isCloudMetadata(ip); got != c.want {
			t.Errorf("isCloudMetadata(%s)=%v 期望 %v（%s）", c.ip, got, c.want, c.desc)
		}
	}
}
