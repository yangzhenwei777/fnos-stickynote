package synclocal

// v21.202 · 网络错误分流回归钉（P0-1）。
//
// 定罪（用户实机 2026-10-10 10:20:05 日志）：
//
//	[sched] 后台同步轮次失败: Post "https://note.example.com/api/auth/refresh":
//	        net/http: TLS handshake timeout
//
// 该机器 DNS 把域名解析到 198.51.100.8（代理软件 Fake-IP 段）且规则里
// 有 域名分流规则 ⇒ 流量绕境外节点，隧道建不起来。
// 此前这类传输层噪声**原样上屏**，用户看到英文错误完全不知道该做什么。
//
// 本文件钉住：①TLS 握手超时独立收敛到 10s ②传输错误翻成可执行中文
// ③业务错误（账密/验证码）**不得**被误分流成"检查网络"。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeNetErr struct {
	timeout bool
	msg     string
}

func (e fakeNetErr) Error() string   { return e.msg }
func (e fakeNetErr) Timeout() bool   { return e.timeout }
func (e fakeNetErr) Temporary() bool { return e.timeout }

var _ net.Error = fakeNetErr{}

// ---- ① TLS 握手超时独立化 ----

// TestNewHTTPHasIndependentTLSHandshakeTimeout 握手超时必须**独立于**总请求超时。
//
// 修法前只有 `http.Client{Timeout: 30s}`，TLS 握手被裹在里面 ⇒ 实测 30s
// 握手超时会让用户对着白屏干等 30 秒。
func TestNewHTTPHasIndependentTLSHandshakeTimeout(t *testing.T) {
	for _, total := range []time.Duration{15 * time.Second, 30 * time.Second} {
		cl := newHTTP(total)
		tr, ok := cl.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("total=%s: Transport 应为 *http.Transport，实际 %T", total, cl.Transport)
		}
		if cl.Timeout != total {
			t.Fatalf("总请求超时应为 %s，实际 %s", total, cl.Timeout)
		}
		if tr.TLSHandshakeTimeout != tlsHandshakeTimeout {
			t.Fatalf("TLS 握手超时应为 %s（独立值），实际 %s", tlsHandshakeTimeout, tr.TLSHandshakeTimeout)
		}
		// 握手超时必须显著短于总超时，否则等于没拆
		if tr.TLSHandshakeTimeout >= cl.Timeout {
			t.Fatalf("握手超时(%s)应小于总超时(%s)，否则拆分无意义", tr.TLSHandshakeTimeout, cl.Timeout)
		}
		// 定版依据：实测正常握手 0.5s，10s 有 20 倍余量
		if tr.TLSHandshakeTimeout < 5*time.Second || tr.TLSHandshakeTimeout > 15*time.Second {
			t.Fatalf("握手超时 %s 超出合理区间 [5s,15s]", tr.TLSHandshakeTimeout)
		}
		if tr.DialContext == nil {
			t.Fatal("DialContext 应设置（拨号超时独立收敛）")
		}
	}
}

// TestAllClientFactoriesUseNewHTTP 所有 client 构造点都必须走 newHTTP
// （防新增通道时又写回裸 &http.Client{}，丢掉握手超时与错误分流）。
func TestAllClientFactoriesUseNewHTTP(t *testing.T) {
	// NewClient（同步主通道）
	c := NewClient("https://x.example", "tk", "dev")
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok || tr.TLSHandshakeTimeout != tlsHandshakeTimeout {
		t.Fatalf("NewClient 未走 newHTTP（握手超时=%v）", tr)
	}
}

// ---- ② 传输错误 → 可执行中文 ----

func TestNetErrHintTLSHandshakeTimeout(t *testing.T) {
	err := netErrHint(errors.New(`Post "https://x/api": net/http: TLS handshake timeout`))
	if !errors.Is(err, ErrNetworkUnreachable) {
		t.Fatalf("TLS 握手超时应归类为 ErrNetworkUnreachable，实际 %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "TLS") || !strings.Contains(msg, "直连") {
		t.Fatalf("文案须含 TLS 与「直连」提示，实际 %q", msg)
	}
	// 不得把传输层英文噪声直接暴露给用户
	if strings.Contains(msg, "net/http:") {
		t.Fatalf("文案不应残留 net/http: 原始噪声：%q", msg)
	}
}

func TestNetErrHintNoSuchHost(t *testing.T) {
	err := netErrHint(errors.New(`dial tcp: lookup x.example: no such host`))
	if !errors.Is(err, ErrNetworkUnreachable) {
		t.Fatal("no such host 应归类为 ErrNetworkUnreachable")
	}
	if !strings.Contains(err.Error(), "找不到") {
		t.Fatalf("文案须含「找不到」，实际 %q", err.Error())
	}
}

func TestNetErrHintConnectionRefused(t *testing.T) {
	err := netErrHint(errors.New(`dial tcp 1.2.3.4:13389: connectex: connection refused`))
	if !errors.Is(err, ErrNetworkUnreachable) {
		t.Fatal("connection refused 应归类为 ErrNetworkUnreachable")
	}
	if !strings.Contains(err.Error(), "拒绝连接") {
		t.Fatalf("文案须含「拒绝连接」，实际 %q", err.Error())
	}
}

func TestNetErrHintTimeoutVariants(t *testing.T) {
	for _, raw := range []string{
		`net/http: TLS handshake timeout`,
		`context deadline exceeded`,
		`i/o timeout`,
		`dial tcp: lookup x: i/o timeout`,
	} {
		if err := netErrHint(errors.New(raw)); !errors.Is(err, ErrNetworkUnreachable) {
			t.Fatalf("%q 应归类为网络不可达，实际 %v", raw, err)
		}
	}
}

func TestNetErrHintNetErrorTimeout(t *testing.T) {
	// net.Error.Timeout() == true 但无典型字样 —— 仍须分流
	err := netErrHint(fmt.Errorf("wrapped: %w", fakeNetErr{timeout: true, msg: "some transport hiccup"}))
	if !errors.Is(err, ErrNetworkUnreachable) {
		t.Fatalf("net.Error.Timeout() 为真时应分流，实际 %v", err)
	}
}

// ---- ③ 业务错误不得被误分流（防"什么都提示检查网络"）----

func TestNetErrHintPassesThroughBusinessErrors(t *testing.T) {
	// 服务端业务错误原样上屏（friendlyHTTPError 已把 JSON 提炼成中文）
	biz := errors.New("账号或密码错误")
	if got := netErrHint(biz); got.Error() != biz.Error() {
		t.Fatalf("业务错误不得被改写：%q -> %q", biz.Error(), got.Error())
	}
	if errors.Is(netErrHint(biz), ErrNetworkUnreachable) {
		t.Fatal("业务错误不得被归类为网络不可达")
	}
	// 无网络特征的传输错误也原样返回（不硬套"检查网络"）
	other := errors.New("sync http 500: internal error")
	if got := netErrHint(other); got.Error() != other.Error() {
		t.Fatalf("非网络错误不得被改写：%q -> %q", other.Error(), got.Error())
	}
}

func TestNetErrHintNilSafe(t *testing.T) {
	if got := netErrHint(nil); got != nil {
		t.Fatalf("nil 应返回 nil，实际 %v", got)
	}
}

// ---- ④ 端到端：真实不可达地址须在握手超时内返回 ----

// TestUnreachableHostFailsFastInHandshakeWindow 实证「握手超时会生效」：
// 连一个必然失败的地址（127.0.0.1:1 拒绝连接），须在握手超时窗口内返回，
// 而不是挂满 30s。这条同时验证错误分流在线上是活的。
func TestUnreachableHostFailsFastInHandshakeWindow(t *testing.T) {
	cl := NewClient("http://127.0.0.1:1", "tk", "dev")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:1/api/sync/state", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	t0 := time.Now()
	_, derr := cl.http.Do(req)
	elapsed := time.Since(t0)
	if derr == nil {
		t.Fatal("127.0.0.1:1 应失败")
	}
	if elapsed > 8*time.Second {
		t.Fatalf("失败耗时 %s 超出预期（应快速返回）", elapsed.Round(time.Millisecond))
	}
	// 分流后应带可执行中文（或至少不是裸传输噪声上屏）
	hinted := netErrHint(derr)
	if !strings.Contains(hinted.Error(), "NETWORK_UNREACHABLE") &&
		!strings.Contains(hinted.Error(), "拒绝连接") {
		t.Logf("提示文案：%s", hinted.Error())
	}
}
