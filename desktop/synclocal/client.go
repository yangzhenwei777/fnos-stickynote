package synclocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stickynote/syncspec"
)

// SyncUploadError 附件上传通道结构化错误（UPLOAD_HASH_MISMATCH / UPLOAD_IDENTITY_CONFLICT
// 等 syncspec 码）；worker 据此区分"内容分歧转人工面"与"网络类可重试"。
type SyncUploadError struct{ Code string }

func (e *SyncUploadError) Error() string { return "sync upload: " + e.Code }

// client.go —— 服务端 sync API 的 HTTP 客户端（HTTPS + JWT；P0-D 直连，
// 刷新/撤销处理按 §101：401/403 → 停止同步进重认证，绝不动本地数据）。

type Client struct {
	BaseURL string
	// Device 设备号（建连后不变，普通字段）。
	Device string
	// ⭐ v21.203：Token 原是**导出的裸 string**，被请求构造（`"Bearer "+cl.Token`，
	// 6 处）与 worker 的 SetToken/ClearToken 并发读写。SetToken 由登录/换票路径调、
	// 请求由调度轮 goroutine 发 ⇒ 标准 data race，后果是 Authorization 头可能
	// 读到**半截 token**（切片/拼接的撕裂），表现为随机的 401。
	// 现改为 atomic.Value 存 string，**不再导出**（跨包无引用，改导出会让人
	// 绕过原子访问重新引入 race —— grep 确认 desktop 包内无 .Token 用法）。
	token atomic.Value
	http  *http.Client
	// badCredsOn401 仅登录通道置真：401 解释成"账密错误"而非"会话被吊销"。
	badCredsOn401 bool
	// bulk/bulkOnce 惰性构造的大文件通道（v21.203 批次 3）：分阶段超时 +
	// 空闲看门狗，与小 JSON 请求的短总时限通道分开。
	//
	// 为什么不用共享 http：短总时限（30s）是为 state/pull/push 这类小请求定的，
	// 套到几十 MB 的快照 zip 上就是实机日志里的
	// `Client.Timeout exceeded while awaiting headers / while reading body` ——
	// 那不是"网络慢"，是**用错了时限档位**。
	bulkOnce sync.Once
	bulk     *http.Client
}

func NewClient(baseURL, token, device string) *Client {
	cl := &Client{BaseURL: strings.TrimRight(baseURL, "/"), Device: device,
		http: newHTTP(30 * time.Second)}
	cl.SetToken(token)
	return cl
}

// SetToken 原子写票（空串拒绝：绝不许把在位票清成假登录态）。
func (cl *Client) SetToken(token string) {
	if token != "" {
		cl.token.Store(token)
	}
}

// ClearToken 原子清票（会话降级专用，v21.203）。
func (cl *Client) ClearToken() {
	cl.token.Store("")
}

// AccessToken 原子读票。
func (cl *Client) AccessToken() string {
	if v, ok := cl.token.Load().(string); ok {
		return v
	}
	return ""
}

func (cl *Client) do(method, path string, reqBody any, out any) error {
	var rdr io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cl.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cl.AccessToken())
	res, err := cl.http.Do(req)
	if err != nil {
		// v21.202：传输层失败先分流成可执行中文（网络/代理不可达 vs 业务错误）。
		// 日志保留原始英文（排障要原文），上屏用中文。
		hinted := netErrHint(err)
		log.Printf("[sync] transport error: %v", err)
		if errors.Is(hinted, ErrNetworkUnreachable) {
			return hinted
		}
		return fmt.Errorf("sync transport: %w", err)
	}
	defer res.Body.Close()
	// ⭐ v21.218：同 transport_ctx.go —— 读取错误不再丢弃（弱网截断会伪装成
	// `unexpected end of JSON input`，误导排障方向）。
	body, rerr := io.ReadAll(res.Body)
	if rerr != nil {
		log.Printf("[sync] response body read error: %v", rerr)
		return fmt.Errorf("%w: 响应体读取中断（网络不稳）: %v", ErrNetworkUnreachable, rerr)
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		if res.StatusCode == 401 && cl.badCredsOn401 {
			return fmt.Errorf("%w: %s", ErrBadCredentials, strings.TrimSpace(string(body)))
		}
		return ErrAuthRevoked
	}
	if res.StatusCode < 200 || res.StatusCode > 299 { // 202 Accepted（快照创建）亦属成功
		return fmt.Errorf("sync http %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if len(bytes.TrimSpace(body)) == 0 {
			return fmt.Errorf("%w: 服务端返回空响应（同步未完成）", ErrNetworkUnreachable)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("响应解析失败（服务端返回非预期内容）: %w", err)
		}
	}
	return nil
}

type StateResp struct {
	ProtocolVersion       int    `json:"protocol_version"`
	ServerInstanceID      string `json:"server_instance_id"`
	ServerEpoch           int64  `json:"server_epoch"`
	CurrentChangeSeq      int64  `json:"current_change_seq"`
	MinAvailableChangeSeq int64  `json:"min_available_change_seq"`
}

func (cl *Client) State() (StateResp, error) { return cl.StateCtx(context.Background()) }

// ⭐ v21.211（210 实机：关机 72 秒不退出）：State 走可取消通道。
// 此前无 ctx，弱网下关机只能等 `http.Client.Timeout`（state 15s / bulk 10min）自然超时。
func (cl *Client) StateCtx(ctx context.Context) (StateResp, error) {
	var s StateResp
	err := cl.doCtx(ctx, "GET", "/api/sync/state", nil, &s)
	return s, err
}

type PullResp struct {
	Changes               []syncspec.ChangeDTO `json:"changes"`
	NextCursor            int64                `json:"next_cursor"`
	Watermark             int64                `json:"watermark"`
	HasMore               bool                 `json:"has_more"`
	ServerEpoch           int64                `json:"server_epoch"`
	MinAvailableChangeSeq int64                `json:"min_available_change_seq"`
}

func (cl *Client) Pull(cursor, watermark int64, limit int) (PullResp, error) {
	return cl.PullCtx(context.Background(), cursor, watermark, limit)
}

// ⭐ v21.211：Pull 走可取消通道（分页循环里每页一次 HTTP 往返，取消才是秒级的）。
func (cl *Client) PullCtx(ctx context.Context, cursor, watermark int64, limit int) (PullResp, error) {
	var p PullResp
	path := fmt.Sprintf("/api/sync/pull?cursor=%d&watermark=%d&limit=%d", cursor, watermark, limit)
	err := cl.doCtx(ctx, "GET", path, nil, &p)
	return p, err
}

func (cl *Client) Push(ops []syncspec.PushOp) ([]syncspec.OpResult, error) {
	return cl.PushCtx(context.Background(), ops)
}

// ⭐ v21.211：Push 走可取消通道（附件往返在大库上是分钟级，不带 ctx 则关机必然长时间挂住）。
func (cl *Client) PushCtx(ctx context.Context, ops []syncspec.PushOp) ([]syncspec.OpResult, error) {
	var out struct {
		Results []syncspec.OpResult `json:"results"`
	}
	err := cl.doCtx(ctx, "POST", "/api/sync/push", syncspec.PushRequest{DeviceID: cl.Device, Ops: ops}, &out)
	return out.Results, err
}

// TOTPRequiredError 携带 2 分钟临时票（v21.151）：errors.As 取 Token 供第二步 VerifyTOTP。
type TOTPRequiredError struct{ Token string }

func (e *TOTPRequiredError) Error() string { return "TOTP_REQUIRED" }

func (e *TOTPRequiredError) Is(target error) bool { return target == ErrTOTPRequired }

// friendlyHTTPError 把 "sync http 400: {"error":"验证码错误"}" 提炼为「验证码错误」——
// 登录/二因子通道的服务端文案是中文、可直接上屏，不该让用户看到传输层噪声。
func friendlyHTTPError(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, "{"); i >= 0 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(msg[i:]), &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
	}
	return err
}

// ---- v21.202网络错误分流（P0-1）----
//
// 定罪（用户实机 2026-10-10 10:20:05 日志）：
//
//	[sched] 后台同步轮次失败: Post "https://note.example.com/api/auth/refresh":
//	        net/http: TLS handshake timeout
//
// 该机器上 DNS 把域名解析到 **198.51.100.8**（本机代理软件的 Fake-IP 段，
// `Fake-IP 模式`），且规则里
// 有 `域名分流规则` ⇒ 便签流量绕境外节点再回来，
// 隧道建立不起来就一直握手到超时。
//
// 为什么必须分流：此前 `net/http: TLS handshake timeout` 这类传输层噪声
// **原样上屏**，用户看到的是一串英文传输错误，完全不知道"该检查网络/代理"。
// 现在把它翻译成可执行的中文。
var (
	// ErrNetworkUnreachable 语义：请求根本没到服务端（DNS/隧道/TLS/连接层）。
	ErrNetworkUnreachable = errors.New("NETWORK_UNREACHABLE")
)

// netErrHint 把传输层错误翻成面向用户的中文，并保留 ErrNetworkUnreachable 语义。
//
// 分流依据（不是猜）：用 errors.As 匹配 net.Error 的 Timeout()，
// 并识别 TLS handshake / no such host / connection refused 三类典型字样。
// **不猜业务语义** —— 账密错误、验证码错误等一律走服务端中文文案原样上屏。
func netErrHint(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	isTimeout := false
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		isTimeout = true
	}

	var hint string
	switch {
	// ---- 代理判据必须排在所有连接判据**之前** ----
	//
	// 定罪（测试抓出）：真实错误 `proxyconnect tcp: dial tcp: connection refused`
	// 里含 "connection refused" 子串。若连接判据在前，就会被判成
	// 「服务器拒绝连接，请确认服务已启动」—— 而实际是**代理配置问题**，
	// 两者给用户的排查方向完全相反（重启服务 vs 改代理规则），误导性极强。
	case strings.Contains(lower, "proxyconnect"),
		strings.Contains(lower, "http proxy"),
		strings.Contains(lower, "socks"),
		strings.Contains(lower, "proxy error"),
		strings.Contains(lower, "unable to connect to proxy"):
		hint = "无法通过代理连接服务器。请检查代理软件设置，或把该服务器设为直连"

	// ---- 其余按"连接层"（请求根本没出去）→"传输层"（出去了但断了）排序 ----
	case strings.Contains(lower, "tls handshake timeout"),
		strings.Contains(lower, "tls handshake"):
		// 定罪场景：代理隧道建不起来（Fake-IP + 走境外节点）最典型的表现。
		hint = "无法与服务器建立安全连接（TLS 握手超时）。" +
			"若你使用代理软件，请把该服务器域名设为**直连**，或换用可直连的地址后重试"
	case strings.Contains(lower, "no such host"):
		hint = "找不到该服务器地址。请检查地址拼写，或确认 DNS/代理未拦截该域名"
	case strings.Contains(lower, "connection refused"):
		hint = "服务器拒绝连接。请确认服务已启动、端口与协议（http/https）正确"

	// ---- v21.203 补充分流（原实现漏判，实机只看到裸英文）----
	case strings.Contains(lower, "eof"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "broken pipe"),
		strings.Contains(lower, "server closed"):
		// 连接被对端/代理静默掐断。部分代理软件在长连接上重置是高频场景，
		// 用户看到的却是 "EOF" 三个字母，完全无从判断。
		hint = "与服务器的连接被中断（连接被重置或服务端关闭）。" +
			"若你使用代理软件，请把该服务器域名设为**直连**后重试"
	case strings.Contains(lower, "x509"),
		strings.Contains(lower, "certificate"),
		strings.Contains(lower, "tls:"):
		// 证书问题必须与"网络不通"分开：这是**地址配错了**（自签/域名不符），
		// 让用户去查代理是浪费时间。
		hint = "服务器证书校验失败。请确认地址与证书匹配（自签名证书需用 http:// 或改用有效证书）"
	case strings.Contains(lower, "http2"),
		strings.Contains(lower, "stream error"),
		strings.Contains(lower, "goaway"):
		hint = "HTTP/2 连接被中断。请重试；若持续失败，可尝试刷新页面或检查代理设置"
	case strings.Contains(lower, "no route to host"),
		strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "host is unreachable"):
		hint = "网络不可达。请确认设备已联网，且该服务器所在网段可达"
	case strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "timeout"):
		hint = "连接服务器超时。若你使用代理软件，请把该服务器域名设为**直连**后重试"
	case isTimeout:
		hint = "连接服务器超时。若你使用代理软件，请把该服务器域名设为**直连**后重试"
	}
	if hint == "" {
		return err // 非网络类错误：原样上屏（服务端业务文案走 friendlyHTTPError）
	}
	return fmt.Errorf("%w: %s", ErrNetworkUnreachable, hint)
}

// newHTTP 统一构造带**独立 TLS 握手超时**的 client。
//
// v21.202：此前只有 `http.Client{Timeout: N}`（整请求超时），TLS 握手被
// 裹在里面 —— 实测 30s 握手超时会让用户对着白屏干等。现在把握手单独收敛到
// 10s：**握手失败几乎一定是网络/代理问题，10s 足够区分"慢"与"不通"**。
func newHTTP(totalTimeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSHandshakeTimeout = tlsHandshakeTimeout
	tr.DialContext = (&net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	//代理软件（TUN/Fake-IP）常在长连接上被静默掐断；短 IdleConnTimeout
	// 让连接不会在"看似还活着"时复用坏连接（会表现为随机的 TLS/EOF 错）。
	tr.IdleConnTimeout = 60 * time.Second
	tr.ExpectContinueTimeout = 1 * time.Second
	return &http.Client{Timeout: totalTimeout, Transport: tr}
}

const (
	// tlsHandshakeTimeout TLS 握手独立上限。10s：实测正常握手 0.5s，
	// 留 20倍余量吸收抖动，同时把"代理不通"的最坏等待从 30s 压到 10s。
	tlsHandshakeTimeout = 10 * time.Second
	// dialTimeout TCP 拨号（含代理接管）上限。
	dialTimeout = 10 * time.Second
)

// Login 真实账号密码换 JWT（正式 LocalAuth/DPAPI 设备会话属 P3）。
// v21.151：识别 200 {totp_required}（2FA 账号）→ 显式 ErrTOTPRequired（带临时票）；
// 200 但缺 access_token 属异常响应 → 显式报错（旧实现两者都静默空票，下游 401 被误报吊销）。
func Login(baseURL, username, password string) (accessToken, refreshToken string, err error) {
	cl := &Client{BaseURL: strings.TrimRight(baseURL, "/"), http: newHTTP(15 * time.Second),
		badCredsOn401: true}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TOTPRequired bool   `json:"totp_required"`
		TOTPToken    string `json:"totp_token"`
	}
	if err := cl.do("POST", "/api/auth/login", map[string]any{
		"username": username, "password": password, "remember": true,
	}, &out); err != nil {
		return "", "", err
	}
	if out.TOTPRequired {
		return "", "", &TOTPRequiredError{Token: out.TOTPToken}
	}
	if out.AccessToken == "" {
		return "", "", errors.New("LOGIN_INCOMPLETE: 服务器未返回登录凭证（响应异常，请检查服务端版本）")
	}
	return out.AccessToken, out.RefreshToken, nil
}

// VerifyTOTP 两步验证第二步（v21.151）：临时票 + 动态码（或恢复码）换正式 token 对。
// 服务端契约见 handler/totp.go VerifyTOTP：400=验证码错误、400=验证已过期（临时票超时）、429=防爆破锁定。
func VerifyTOTP(baseURL, totpToken, code string) (accessToken, refreshToken string, err error) {
	cl := &Client{BaseURL: strings.TrimRight(baseURL, "/"), http: newHTTP(15 * time.Second),
		badCredsOn401: true}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := cl.do("POST", "/api/auth/verify-totp", map[string]any{
		"totp_token": totpToken, "code": code, "remember": true,
	}, &out); err != nil {
		return "", "", friendlyHTTPError(err)
	}
	if out.AccessToken == "" {
		return "", "", errors.New("LOGIN_INCOMPLETE: 服务器未返回登录凭证（响应异常，请检查服务端版本）")
	}
	return out.AccessToken, out.RefreshToken, nil
}

// Refresh 用落盘 refresh 换发新 access（批次 4 刀 2：冷启动恢复/重验共用通道）。
// 服务端 v21.90 起一次一换（旧票 family 内吊销）——调用方拿到新 refresh 必须**立即**
// SaveRefresh 覆盖封存件，否则下次启动手里只有废票。401 → ErrAuthRevoked（需重新登录）。
func Refresh(baseURL, refreshToken string) (accessToken, newRefresh string, err error) {
	return RefreshCtx(context.Background(), baseURL, refreshToken)
}

// RefreshCtx ctx 版换票（v21.211）。
//
// 为什么必须带 ctx：换票是 `ensureAccess` 的第一步，**每个**同步轮和每次
// `BootstrapNow` 都会走它。原先裸调 `http.Client{Timeout:15s}`，
// 关机时 `cancelShutdown()` 广播到不了它（且它本身不认 ctx）⇒ 关机要白等 15s。
// 现在 ctx 一取消立即返回，换票进入秒级收敛。
func RefreshCtx(ctx context.Context, baseURL, refreshToken string) (accessToken, newRefresh string, err error) {
	cl := &Client{BaseURL: strings.TrimRight(baseURL, "/"), http: newHTTP(15 * time.Second)}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := cl.doWithContext(ctx, "POST", "/api/auth/refresh", map[string]any{"refresh_token": refreshToken}, &out); err != nil {
		return "", "", err
	}
	return out.AccessToken, out.RefreshToken, nil
}

// ---- 批次 5 · P5：Bootstrap 快照通道（服务端契约见 server/sync/snapshot_api.go）----

// SnapshotStatusResp 轮询载荷（字段与服务端 SnapshotStatus handler 对齐）。
type SnapshotStatusResp struct {
	SnapshotID  string `json:"snapshot_id"`
	Status      string `json:"status"`
	ServerEpoch int64  `json:"server_epoch"`
	Watermark   int64  `json:"watermark"`
	InstanceID  string `json:"instance_id"`
	Sha256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
	FileCount   int    `json:"file_count"`
	Error       string `json:"error"`
}

// RequestSnapshot POST /api/sync/snapshot → snapshot_id（202；在途复用同 id）。
// 429/503 以错误透出（调用侧退避）。
func (cl *Client) RequestSnapshot(ctx context.Context) (string, error) {
	var out struct {
		SnapshotID string `json:"snapshot_id"`
		Status     string `json:"status"`
	}
	// ⭐ v21.218（212 实机深度核查）：原实现调 `do()`（**无 ctx**，http.NewRequest），
	// 而同文件 `transport_ctx.go:doWithContext` 已有正确实现却从未被这里使用。
	// 后果：关机/换代取消信号在这两个请求上**完全失效**，只能等满
	// `newHTTP(30s)` 的 Client.Timeout —— 实机日志里「16:15:23 → 16:15:53」
	// 那 30 秒空窗正是它（该区间内 bootstrap 已决定中止换代，却仍空等 30s）。
	if err := cl.doWithContext(ctx, "POST", "/api/sync/snapshot", map[string]any{}, &out); err != nil {
		return "", err
	}
	return out.SnapshotID, nil
}

// SnapshotStatus GET /api/sync/snapshot/:id。
//
// ⭐ v21.218：同上，改走 doWithContext。轮询期间关机/换代时能立即返回，
// 不再每次都撞满 30s 客户端超时。
func (cl *Client) SnapshotStatus(ctx context.Context, id string) (*SnapshotStatusResp, error) {
	var out SnapshotStatusResp
	if err := cl.doWithContext(ctx, "GET", "/api/sync/snapshot/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DownloadSnapshot GET .../download 流式落盘（批次 5；sha256 由调用侧对整文件校验）。
// 先写 dst+".part" 再原子改名——磁盘上永不出现"看似完整实则半截"的 zip。
func (cl *Client) DownloadSnapshot(id, dst string) error {
	req, err := http.NewRequest("GET", cl.BaseURL+"/api/sync/snapshot/"+id+"/download", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cl.AccessToken())
	res, err := cl.http.Do(req)
	if err != nil {
		return fmt.Errorf("snapshot transport: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrAuthRevoked
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("snapshot http %d", res.StatusCode)
	}
	f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, res.Body)
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dst + ".part")
		return fmt.Errorf("snapshot write: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst + ".part")
		return closeErr
	}
	if err := os.Rename(dst+".part", dst); err != nil {
		_ = os.Remove(dst + ".part")
		return err
	}
	return nil
}

// UploadAttachment POST /api/sync/upload（multipart：file + filename + sha256 申报）。
// 服务端流式重算为准（ADR-003 R2）；200 即成功（幂等重传同内容亦 200）。
func (cl *Client) UploadAttachment(filename, declaredSHA string, data []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("filename", filename)
	_ = mw.WriteField("sha256", declaredSHA)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	mw.Close()
	req, err := http.NewRequest("POST", cl.BaseURL+"/api/sync/upload", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+cl.AccessToken())
	res, err := cl.http.Do(req)
	if err != nil {
		// v21.202：传输层失败先分流成可执行中文（网络/代理不可达 vs 业务错误）。
		// 日志保留原始英文（排障要原文），上屏用中文。
		hinted := netErrHint(err)
		log.Printf("[sync] transport error: %v", err)
		if errors.Is(hinted, ErrNetworkUnreachable) {
			return hinted
		}
		return fmt.Errorf("sync transport: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrAuthRevoked
	}
	if res.StatusCode == 200 {
		return nil
	}
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &out)
	code := out.Code
	if code == "" {
		code = fmt.Sprintf("http_%d", res.StatusCode)
	}
	return &SyncUploadError{Code: code}
}

// DownloadAttachment GET /uploads/<name>（Bearer 头认证同 §v20.7 受保护路由）。
func (cl *Client) DownloadAttachment(name string) ([]byte, error) {
	req, err := http.NewRequest("GET", cl.BaseURL+"/uploads/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cl.AccessToken())
	res, err := cl.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sync transport: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return nil, ErrAttachmentMissing
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, ErrAuthRevoked
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("attachment http %d", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}
