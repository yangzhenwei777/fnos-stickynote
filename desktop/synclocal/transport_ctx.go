package synclocal

// transport_ctx.go —— v21.203 批次 3：传输层的 context 贯通 / 分阶段超时 / 空闲看门狗。
//
// 定罪（审计 + 用户实机 2026-10-10）：
//
//  ① **无 ctx**：`BootstrapNow → Runner.Run → poll → DownloadSnapshot` 全链没有
//     context，`poll` 里是裸 `time.Sleep`。用户点「强制全量同步」后若想取消，
//     唯一手段是杀进程 —— 而杀进程恰好落在换代窗口里（批次 1 的 SEALED 崩溃态）。
//     两件事叠在一起 = 用户为了取消一次网络等待而制造一次换代崩溃。
//
//  ② **`http.Client.Timeout` 语义错**：它覆盖「整个请求从建连到读完 body」的总时长。
//     快照 zip 动辄几十 MB + 慢上行，`awaiting headers` / `while reading body` 超时
//     正是它造成的（实机日志原文）。正确形态是**分阶段**：连接/首字节要短（快速
//     判定"通不通"），**传输本身要长**（大文件本来就慢，不该被总时限腰斩）。
//
//  ③ **`io.Copy` 无看门狗**：`Timeout` 到期后 http 会把 body 读出错中止 Copy，
//     但如果对端**保持连接不发数据**（半开连接、代理挂起），`Client.Timeout`
//     依然会兜住 —— 然而 `Worker.RunOnce` 用的 client 与下载用的 client 不同，
//     且未来任何绕过 `Client.Timeout` 的路径都会变成"永久挂起"。故对流式复制
//     额外加**空闲看门狗**（两个方向都没数据就中止），与总时限正交。
//
// 设计取舍：
//   - **不全局改 `newHTTP` 的 Timeout**（那会让所有小请求的语义跟着变），
//     而是给大文件通道单独一个 client：大总时限 + 短首字节超时 + 看门狗。
//   - ctx 优先于超时：调用方给了 ctx（关机/取消）就听 ctx 的，没给才用时限兜底。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// bulkTotalTimeout 大文件（快照 zip）传输总时限上限。
	//
	// 取 10 分钟的依据：快照 = 服务端 `VACUUM INTO` 的一致性副本，体积与用户
	// 数据量同阶（典型 5~100MB）。10MB/s 的链路上 100MB 需 10s，留 60 倍余量
	// 覆盖极慢上行（1Mbps ≈ 0.125MB/s，100MB 需 13 分钟 —— 超了，但那种链路上
	// 本来就不该做全量同步）。**关键是它不再像 30s 那样腰斩正常传输**。
	bulkTotalTimeout = 10 * time.Minute

	// bulkFirstByteTimeout 首字节/响应头时限。**短**：这一段只反映"通不通"，
	// 与文件大小无关，超了就是网络/代理问题（实机的 TLS 握手/Fake-IP 即在此段）。
	bulkFirstByteTimeout = 20 * time.Second

	// bulkIdleTimeout 流式传输的**空闲**看门狗：两个方向都没有任何字节流动
	// 超过此时长即中止。20s：正常传输的字节间隔在毫秒~秒级，20s 无进展即异常
	// （代理静默掐连接、服务端假死、下载进程被挂起）。
	bulkIdleTimeout = 20 * time.Second
)

// newBulkHTTP 构造大文件通道的 client。
//
// 与 `newHTTP` 的差别只有三处：总时限放宽、短 Dial/TLS、短首字节等待。
// 短首字节靠 `ResponseHeaderTimeout` 实现（它在收到响应头时解除），
// 这样"开始下载"之后就不再受它约束 —— 正是分阶段语义。
func newBulkHTTP() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSHandshakeTimeout = tlsHandshakeTimeout // 10s
	tr.DialContext = (&net.Dialer{
		Timeout:   dialTimeout, // 10s
		KeepAlive: 30 * time.Second,
	}).DialContext
	tr.ResponseHeaderTimeout = bulkFirstByteTimeout // 20s：只管到"开始下载"为止
	tr.ExpectContinueTimeout = 1 * time.Second
	// v21.203：`IdleConnTimeout` 从 60s 调到 90s。原值 60s **恰好等于调度周期
	// 60s**（startScheduler 里 Probe=60s），于是每一轮调度都有极高概率命中
	// "空闲连接刚被服务端/代理静默掐断"的时刻，复用出一条半开连接 →
	// 表现为随机的 TLS/EOF 错。留出 1.5 倍余量让连接自然老化而非周期撞车。
	tr.IdleConnTimeout = 90 * time.Second
	return &http.Client{Timeout: bulkTotalTimeout, Transport: tr}
}

// bulkClientOf 取（惰性建）该 Client 的大文件通道。
//
// 惰性 + sync.Once 的理由：绝大多数请求是小 JSON（state/pull/push），
// 不该为此多建一个连接池；但 `Client` 是值构造（`NewClient` 返回指针但
// `Login`/`Refresh` 走临时 Client），所以只能惰性挂在 `*Client` 上。
func (cl *Client) bulkClient() *http.Client {
	cl.bulkOnce.Do(func() {
		cl.bulk = newBulkHTTP()
	})
	return cl.bulk
}

// doWithContext 是 `do` 的 ctx 版。ctx 为 nil 时退化为 context.Background()。
func (cl *Client) doWithContext(ctx context.Context, method, path string, reqBody any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var rdr io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, cl.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cl.AccessToken())
	res, err := cl.http.Do(req)
	if err != nil {
		// ⭐ v21.203：**ctx 取消不得被当成网络故障上屏**。关机/取消是我们自己
		// 主动终止的，不是"网络不通" —— 混进 ErrNetworkUnreachable 会让用户
		// 在关机瞬间看到"检查网络/代理"的横幅。
		if ctx.Err() != nil {
			return fmt.Errorf("请求已取消: %w", ctx.Err())
		}
		hinted := netErrHint(err)
		log.Printf("[sync] transport error: %v", err)
		if errors.Is(hinted, ErrNetworkUnreachable) {
			return hinted
		}
		return fmt.Errorf("sync transport: %w", err)
	}
	defer res.Body.Close()
	// ⭐ v21.218（212 实机深度核查）：原为 `body, _ := io.ReadAll(res.Body)` ——
	// **丢弃了读取错误**。弱网下响应体常被中途截断，此时 err != nil 且 body 是半截 JSON，
	// 但错误被吞掉后流程继续走到 `json.Unmarshal` ⇒ 抛出 **`unexpected end of JSON
	// input`** —— 一个完全误导性的报错（真实成因是"响应体截断"，不是"JSON 格式错"）。
	// 实机日志 16:16:41 的 `自动快照失败: unexpected end of JSON input` 即此。
	//
	// 修法：读取错误必须显式分类。ctx 取消走取消语义，其余按网络类失败上屏，
	// 让 UI 提示"网络/代理"而不是让用户怀疑自己的数据。
	body, rerr := io.ReadAll(res.Body)
	if rerr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("请求已取消: %w", ctx.Err())
		}
		log.Printf("[sync] response body read error: %v", rerr)
		return fmt.Errorf("%w: 响应体读取中断（网络不稳）: %v", ErrNetworkUnreachable, rerr)
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		if res.StatusCode == 401 && cl.badCredsOn401 {
			return fmt.Errorf("%w: %s", ErrBadCredentials, strings.TrimSpace(string(body)))
		}
		return ErrAuthRevoked
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("sync http %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if len(bytes.TrimSpace(body)) == 0 {
			// 2xx 但响应体为空：不是 JSON 格式问题，是服务端/中间层返回了空体。
			// 明确归类为网络类，避免误导成"数据损坏"。
			return fmt.Errorf("%w: 服务端返回空响应（同步未完成）", ErrNetworkUnreachable)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("响应解析失败（服务端返回非预期内容）: %w", err)
		}
	}
	return nil
}

// doCtx 是 State/Pull/Push 的统一可取消入口（v21.211）。
//
// 与 `do` 的差异只有一处：`http.NewRequestWithContext` + ctx 取消分流。
// 取消**不得**被当成网络故障上屏（关机瞬间不该弹"检查网络/代理"）—— 该分流
// 逻辑在 doWithContext 内已实现（v21.203），故此处直接复用同一实现体，
// 避免同一套状态码/凭据/错误分类出现第二份实现而漂移。
func (cl *Client) doCtx(ctx context.Context, method, path string, reqBody any, out any) error {
	return cl.doWithContext(ctx, method, path, reqBody, out)
}

// DownloadSnapshotCtx ctx 版快照下载（分阶段超时 + 空闲看门狗 + 原子落盘）。
//
// 与 `DownloadSnapshot` 的差异只在传输策略与可取消性，落盘语义完全一致
// （`.part` → Sync → Close → Rename），**两版必须保持行为同步**（一条是
// 老调用点的薄封装，见 DownloadSnapshot 的注释）。
func (cl *Client) DownloadSnapshotCtx(ctx context.Context, id, dst string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", cl.BaseURL+"/api/sync/snapshot/"+id+"/download", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cl.AccessToken())
	res, err := cl.bulkClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("快照下载已取消: %w", ctx.Err())
		}
		hinted := netErrHint(err)
		log.Printf("[sync] snapshot transport error: %v", err)
		if errors.Is(hinted, ErrNetworkUnreachable) {
			return hinted
		}
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
	// 空闲看门狗包住 Copy：见 copyWithIdleGuard 注释。
	_, copyErr := copyWithIdleGuard(f, res.Body, bulkIdleTimeout, ctx)
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

// DownloadPayloadCtx ctx 版大载荷 blob 下载（§32/§34 P1-b：GET /api/sync/payload/:sha256）。
//
// 复用快照下载同一套传输纪律：bulk 客户端 + 空闲看门狗 + `.part` 原子落盘。
// 差异：下载路径经 io.MultiWriter 同步喂 sha256（边下边算，无回读）——返回实算
// hex，调用方必须与 ref 对拍后再用（截断/篡改件就地作废）。
// 404（引用失效/文件缺失）返回 ErrPayloadGone，调用方按重试+逃逸计数处置。
func (cl *Client) DownloadPayloadCtx(ctx context.Context, ref, dst string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", cl.BaseURL+"/api/sync/payload/"+ref, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cl.AccessToken())
	res, err := cl.bulkClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("载荷下载已取消: %w", ctx.Err())
		}
		hinted := netErrHint(err)
		log.Printf("[sync] payload transport error: %v", err)
		if errors.Is(hinted, ErrNetworkUnreachable) {
			return "", hinted
		}
		return "", fmt.Errorf("payload transport: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return "", ErrAuthRevoked
	}
	if res.StatusCode == 404 {
		return "", ErrPayloadGone
	}
	if res.StatusCode != 200 {
		return "", fmt.Errorf("payload http %d", res.StatusCode)
	}
	f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := copyWithIdleGuard(io.MultiWriter(f, h), res.Body, bulkIdleTimeout, ctx)
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dst + ".part")
		return "", fmt.Errorf("payload write: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst + ".part")
		return "", closeErr
	}
	if err := os.Rename(dst+".part", dst); err != nil {
		_ = os.Remove(dst + ".part")
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyWithIdleGuard 带空闲看门狗与 ctx 的流式复制。
//
// 语义：**双向都有进展才继续**。每读到一块就重置计时器；超过 idle 无任何
// 字节流动即返回可读错误（区分"取消"与"空闲超时"两种原因）。
//
// 为什么不能只靠 http.Client.Timeout：Timeout 是总时限，对"慢但正常"的
// 大文件误杀；而"连着但不传数据"这种半开连接在有些链路上也不触发 Read
// 错误（TCP 窗口开着、代理缓存住响应头）。看门狗补的正是这个洞。
func copyWithIdleGuard(dst io.Writer, src io.Reader, idle time.Duration, ctx context.Context) (int64, error) {
	if idle <= 0 {
		// 未配置看门狗 = 退化为纯复制（调用方显式选择，不要静默弱化）
		return io.Copy(dst, src)
	}
	buf := make([]byte, 64*1024)
	var total int64
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return total, fmt.Errorf("传输已取消: %w", ctx.Err())
			default:
			}
		}
		// 每一轮读都带 idle 上限：src.Read 卡住超过 idle 即返回超时。
		type readResult struct {
			n   int
			err error
		}
		ch := make(chan readResult, 1) // 容量 1：读超时后 goroutine 也能安全退出
		go func() {
			n, err := src.Read(buf)
			ch <- readResult{n, err}
		}()
		timer := time.NewTimer(idle)
		var res readResult
		select {
		case <-ctx.Done():
			timer.Stop()
			return total, fmt.Errorf("传输已取消: %w", ctx.Err())
		case res = <-ch:
			timer.Stop()
		case <-timer.C:
			return total, fmt.Errorf("传输停滞：连续 %s 无数据到达（连接可能被代理或服务端静默掐断）", idle)
		}
		if res.n > 0 {
			if werr := writeAll(dst, buf[:res.n]); werr != nil {
				return total, werr
			}
			total += int64(res.n)
		}
		if res.err != nil {
			if res.err == io.EOF {
				return total, nil
			}
			return total, res.err
		}
	}
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
