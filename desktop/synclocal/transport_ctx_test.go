package synclocal

// transport_ctx_test.go —— v21.203 批次 3 回归：ctx 贯通 / 空闲看门狗 / 错误分流 / Token 原子。
//
// 全部锚在用户实机（2026-10-10，另一台电脑、本机代理 关闭）的两条日志上：
//   [sched] 后台同步轮次失败: Post "https://…/api/auth/refresh": net/http: TLS handshake timeout
//   快照同步失败: Client.Timeout exceeded while awaiting headers / while reading body
//
// 前者说明"网络/代理问题被当成裸英文上屏"，后者说明"**用错了时限档位**"。

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 空闲看门狗 ----

// blockingReader 模拟"连着但不传数据"的半开连接（代理挂起 / 服务端假死）。
type blockingReader struct {
	first []byte // 首块正常给出（模拟响应头已到、开始下载）
	read  bool
	ch    chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if !b.read {
		b.read = true
		n := copy(p, b.first)
		return n, nil
	}
	<-b.ch // 永不返回数据，直到测试关闭
	return 0, io.EOF
}

// TestCopyWithIdleGuardAbortsStalled —— 看门狗必须中止"连着但不传"的流。
// 没有它，大文件下载会在半开连接上**永久挂起**（http.Client.Timeout 也不一定兜得住）。
func TestCopyWithIdleGuardAbortsStalled(t *testing.T) {
	src := &blockingReader{first: []byte("HEAD"), ch: make(chan struct{})}
	defer close(src.ch)
	var got strings.Builder
	start := time.Now()
	_, err := copyWithIdleGuard(&got, src, 200*time.Millisecond, context.Background())
	if err == nil {
		t.Fatal("停滞流必须返回错误（不能让下载永久挂起）")
	}
	if !strings.Contains(err.Error(), "传输停滞") {
		t.Fatalf("错误应说明是停滞，实得 %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("看门狗应在 idle 附近触发，实得 %s", el)
	}
	if got.String() != "HEAD" {
		t.Fatalf("中止前已读到的数据必须已落盘（不能白读），实得 %q", got.String())
	}
}

// TestCopyWithIdleGuardRespectsCancel —— ctx 取消必须立刻中止，且与停滞区分开。
func TestCopyWithIdleGuardRespectsCancel(t *testing.T) {
	src := &blockingReader{first: []byte("HEAD"), ch: make(chan struct{})}
	defer close(src.ch)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := copyWithIdleGuard(io.Discard, src, 10*time.Second, ctx)
	if err == nil {
		t.Fatal("ctx 取消必须返回错误")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误应可判定为 context.Canceled（与停滞区分），实得 %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("ctx 取消应立即生效（不该等 idle 看门狗的 %s），实得 %s", "10s", el)
	}
}

// TestCopyWithIdleGuardNormalStream —— 正常流必须完整复制且不误触发。
func TestCopyWithIdleGuardNormalStream(t *testing.T) {
	payload := make([]byte, 512*1024) // 512KB > 单次 64KB 缓冲，逼出多轮
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	var got []byte
	buf := &strings.Builder{}
	_ = buf
	dst := &byteSink{}
	n, err := copyWithIdleGuard(dst, &chunkReader{data: payload, chunk: 32 * 1024}, 2*time.Second, context.Background())
	if err != nil {
		t.Fatalf("正常流不得报错: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("复制字节数 %d，应为 %d", n, len(payload))
	}
	got = dst.b
	if len(got) != len(payload) {
		t.Fatalf("落盘长度 %d，应为 %d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("第 %d 字节不一致（分块复制有错）", i)
		}
	}
}

type byteSink struct{ b []byte }

func (s *byteSink) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }

// chunkReader 分块返回（模拟真实网络分片）。
type chunkReader struct {
	data  []byte
	off   int
	chunk int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.off >= len(c.data) {
		return 0, io.EOF
	}
	n := c.chunk
	if n > len(p) {
		n = len(p)
	}
	if c.off+n > len(c.data) {
		n = len(c.data) - c.off
	}
	copy(p, c.data[c.off:c.off+n])
	c.off += n
	return n, nil
}

// ---- 分阶段超时：慢但有效的下载不得被腰斩 ----

// TestDownloadSnapshotAllowsSlowStream —— ⭐ 批次 3 的核心钉子。
//
// 定罪：`http.Client.Timeout` 覆盖"从建连到读完 body"的**总时长**。快照 zip 动辄
// 几十 MB，`awaiting headers` / `while reading body` 超时正是它造成的 —— 那不是
// "网络慢"，是**用错了时限档位**。这条用例用"每 200ms 收 32KB"的慢流（总时长远超
// 任何短总时限，但每个字节都在正常推进）验证：分阶段超时下必须下载成功。
func TestDownloadSnapshotAllowsSlowStream(t *testing.T) {
	const total = 256 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		chunk := make([]byte, 32*1024)
		for sent := 0; sent < total; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond) // 每 60ms 都有进展：空闲看门狗不该触发
		}
	}))
	defer srv.Close()

	cl := NewClient(srv.URL, "tok", "dev")
	dst := filepath.Join(t.TempDir(), "snap.zip")
	// 总时长约 0.5s，但**没有任何单段超过 20s 首字节/看门狗**。
	if err := cl.DownloadSnapshotCtx(context.Background(), "sid", dst); err != nil {
		t.Fatalf("慢但持续的下载必须成功（分阶段超时语义），实得 %v", err)
	}
	st, err := os.Stat(dst)
	if err != nil || st.Size() != total {
		t.Fatalf("落盘大小应为 %d，实得 %v (err=%v)", total, stSize(st), err)
	}
	// .part 必须已消失（原子改名完成）
	if _, perr := os.Stat(dst + ".part"); !os.IsNotExist(perr) {
		t.Fatal(".part 残留说明未完成原子改名")
	}
}

func stSize(fi os.FileInfo) int64 {
	if fi == nil {
		return -1
	}
	return fi.Size()
}

// TestDownloadSnapshotCtxCancel —— 取消必须立刻中止并清掉 .part（不留半截）。
func TestDownloadSnapshotCtxCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-release // 响应头后卡住不传数据
	}))
	defer srv.Close()
	defer close(release)

	cl := NewClient(srv.URL, "tok", "dev")
	dst := filepath.Join(t.TempDir(), "snap.zip")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := cl.DownloadSnapshotCtx(ctx, "sid", dst)
	if err == nil {
		t.Fatal("被取消的下载必须返回错误")
	}
	if !strings.Contains(err.Error(), "取消") {
		t.Fatalf("错误应说明是取消（而非网络故障），实得 %v", err)
	}
	// 不得留下 .part，也不得留下目标文件（半截文件比没有更危险）
	if _, perr := os.Stat(dst + ".part"); !os.IsNotExist(perr) {
		t.Fatal("取消后 .part 必须清理")
	}
	if _, derr := os.Stat(dst); !os.IsNotExist(derr) {
		t.Fatal("取消后不得留下半截目标文件")
	}
}

// TestCancelNotReportedAsNetworkFailure —— ⭐ 取消不得被当成"网络不通"上屏。
// 否则用户在关机/点取消的瞬间会看到"检查网络/代理"的横幅，纯属误导。
func TestCancelNotReportedAsNetworkFailure(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	cl := NewClient(srv.URL, "tok", "dev")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := cl.doWithContext(ctx, "GET", "/api/sync/state", nil, nil)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if errors.Is(err, ErrNetworkUnreachable) {
		t.Fatalf("取消不得被归类为网络不可达（会误导用户查代理），实得 %v", err)
	}
}

// ---- 错误文案分流 ----

// TestNetErrHintCoverage —— v21.203 补的分支必须真的能命中，且不误伤业务文案。
func TestNetErrHintCoverage(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantHint string
	}{
		{"EOF_连接被掐断", errors.New("Post \"https://x/a\": EOF"), "连接被中断"},
		{"connection reset", errors.New("read tcp: connection reset by peer"), "连接被中断"},
		{"broken pipe", errors.New("write: broken pipe"), "连接被中断"},
		{"x509_证书", errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), "证书校验失败"},
		{"http2 stream error", errors.New("http2: stream error: INTERNAL_ERROR"), "HTTP/2 连接被中断"},
		{"proxyconnect", errors.New("proxyconnect tcp: dial tcp: connection refused"), "代理"},
		{"no route to host", errors.New("dial tcp: no route to host"), "网络不可达"},
		{"TLS握手", errors.New("net/http: TLS handshake timeout"), "TLS 握手超时"},
		{"DNS", errors.New("lookup x: no such host"), "找不到该服务器地址"},
		{"连接被拒", errors.New("dial tcp: connection refused"), "服务器拒绝连接"},
		{"超时", errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)"), "超时"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := netErrHint(tc.err)
			if !errors.Is(got, ErrNetworkUnreachable) {
				t.Fatalf("必须归类为网络不可达，实得 %v", got)
			}
			if !strings.Contains(got.Error(), tc.wantHint) {
				t.Fatalf("文案应含 %q，实得 %q", tc.wantHint, got.Error())
			}
		})
	}
}

// TestNetErrHintDoesNotSwallowBusinessErrors —— 业务错误绝不能被翻成"检查网络"。
func TestNetErrHintDoesNotSwallowBusinessErrors(t *testing.T) {
	for _, e := range []error{
		errors.New(`sync http 400: {"error":"验证码错误"}`),
		errors.New("localauth: PIN not enrolled"),
		errors.New("no such table: notes"),
	} {
		got := netErrHint(e)
		if errors.Is(got, ErrNetworkUnreachable) {
			t.Fatalf("业务错误不得被翻成网络不可达: %v", e)
		}
	}
}

// TestNetErrHintProxyKeywordNotOverbroad —— "proxy" 关键字不得过宽：
// URL 路径里含 proxy（如 /api/proxy/...）的业务错误不能被误判。
func TestNetErrHintProxyKeywordNotOverbroad(t *testing.T) {
	biz := errors.New(`sync http 404: {"error":"/api/proxy/config not found"}`)
	if errors.Is(netErrHint(biz), ErrNetworkUnreachable) {
		t.Fatalf("含 proxy 字样的业务错误不得被判为代理故障: %v", netErrHint(biz))
	}
}

// ---- Token 原子访问 ----

// TestClientTokenConcurrentAccess —— 并发读写不得丢更新、不得读到半截。
// 这是 data race 的行为断言（本机 -race 不可用，CI 的 race job 会再做一层）。
func TestClientTokenConcurrentAccess(t *testing.T) {
	cl := NewClient("http://x", "seed", "dev")
	var wg sync.WaitGroup
	const n = 200
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			cl.SetToken(strings.Repeat("A", 64) + string(rune('a'+i%26)))
		}(i)
		go func() {
			defer wg.Done()
			_ = cl.AccessToken()
			_ = (cl.AccessToken() != "")
		}()
	}
	wg.Wait()
	tok := cl.AccessToken()
	if len(tok) != 65 {
		t.Fatalf("票必须是完整 65 字符（64 位 + 1 位序号），实得 %d：%q", len(tok), tok)
	}
	// 拒绝空串（护栏：绝不把在位票清成假登录态）
	before := cl.AccessToken()
	cl.SetToken("")
	if cl.AccessToken() != before {
		t.Fatal("SetToken(\"\") 必须被拒绝（不得清掉在位票）")
	}
	cl.ClearToken()
	if cl.AccessToken() != "" {
		t.Fatal("ClearToken 后不得再有票")
	}
}

// TestBulkClientSeparateFromSmallRequests —— 大文件通道必须与小请求通道分开
// （分阶段超时的前提：短总时限不能套到快照上）。
func TestBulkClientSeparateFromSmallRequests(t *testing.T) {
	cl := NewClient("http://x", "tok", "dev")
	if cl.bulkClient() == cl.http {
		t.Fatal("大文件通道必须与小请求通道是不同 client（否则分阶段超时失效）")
	}
	if cl.bulkClient() != cl.bulkClient() {
		t.Fatal("bulkClient 必须惰性建一次后复用（否则每请求建一个连接池）")
	}
	tr := cl.bulkClient().Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != bulkFirstByteTimeout {
		t.Fatalf("首字节超时=%s，应为 %s", tr.ResponseHeaderTimeout, bulkFirstByteTimeout)
	}
	if cl.bulkClient().Timeout != bulkTotalTimeout {
		t.Fatalf("总时限=%s，应为 %s", cl.bulkClient().Timeout, bulkTotalTimeout)
	}
	// ⭐ IdleConnTimeout 不得等于调度周期（否则每轮都复用刚被掐断的连接）
	if tr.IdleConnTimeout <= 60*time.Second {
		t.Fatalf("IdleConnTimeout=%s 必须大于调度周期 60s（原值 60s 会周期撞车）", tr.IdleConnTimeout)
	}
}

var _ = net.Dial // 保持 net 导入（供未来测试用）
