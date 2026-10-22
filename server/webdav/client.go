package webdav

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/studio-b12/gowebdav"
)

// Options 客户端构造参数。
type Options struct {
	URL             string
	User            string
	Pass            string
	AllowLoopback   bool // "允许本机 WebDAV"（allow_loopback），默认关
	AllowPrivate    bool // RFC1918/ULA；默认 true（fnOS 局域网）
	InsecureSkipTLS bool // 跳证书校验（上层严格门控 + 坚果云禁用）
	CustomCAPEM     string
	DialTimeout     time.Duration // 0 → 15s（护栏内固定）
	RequestTimeout  time.Duration // 单请求超时（0→60s）
	MaxObjectSize   int64         // ProviderCapability.MaxObjectSize（0→用调用侧默认）
}

// Client 是 gowebdav 之上的薄封装：只暴露备份需要的原语，协议细节屏蔽在包内。
// 业务代码不直接依赖 WebDAV 语义。
type Client struct {
	wd  *gowebdav.Client
	opt Options
}

// New 构造客户端。仅允许 http/https；注入 SSRF 护栏 Transport。
func New(opt Options) (*Client, error) {
	if !SchemeAllowed(strings.TrimSpace(opt.URL)) {
		return nil, fmt.Errorf("%w: 仅支持 http/https", ErrBlockedURL)
	}
	wd := gowebdav.NewClient(opt.URL, opt.User, opt.Pass)
	rt, err := newTransport(ssrfOptions{
		allowLoopback: opt.AllowLoopback,
		allowPrivate:  true, // 一期恒允许私网（fnOS 局域网）；如需更严可后移
	}, opt.CustomCAPEM, opt.InsecureSkipTLS)
	if err != nil {
		return nil, err
	}
	wd.SetTransport(rt)
	rc := reqTimeout(opt.RequestTimeout)
	wd.SetTimeout(rc)
	return &Client{wd: wd, opt: opt}, nil
}

func reqTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 60 * time.Second
	}
	return d
}

// Test 做一次真实连通/鉴权探测（PROPFIND 根）。
func (c *Client) Test() error {
	if err := c.wd.Connect(); err != nil {
		return classify(err)
	}
	return nil
}

// MkdirAll 递归建远端目录（已存在幂等）。
func (c *Client) MkdirAll(path string) error { return classify(c.wd.MkdirAll(path, os.ModePerm)) }

// PutStream 以 Content-Length 流式上传（避免 chunked，部分 WebDAV 对无长度 PUT 行为异常）。
func (c *Client) PutStream(path string, r io.Reader, size int64) error {
	return classify(c.wd.WriteStreamWithLength(path, r, size, os.FileMode(0644)))
}

// PutBytes 上传小对象（如 index JSON）。
func (c *Client) PutBytes(path string, data []byte) error {
	return classify(c.wd.Write(path, data, os.FileMode(0644)))
}

// Get 全量读取（恢复/验证/歧义内容核对用）。
func (c *Client) Get(path string) ([]byte, error) {
	b, err := c.wd.Read(path)
	return b, classify(err)
}

// Head 返回对象是否存在与大小（不读内容，省下行配额）。
func (c *Client) Head(path string) (size int64, exists bool, err error) {
	fi, e := c.wd.Stat(path)
	if e != nil {
		if gowebdav.IsErrNotFound(e) {
			return 0, false, nil
		}
		return 0, false, classify(e)
	}
	return fi.Size(), true, nil
}

// Delete 删除对象。
func (c *Client) Delete(path string) error { return classify(c.wd.Remove(path)) }

// ListNames 列目录（相对名），供索引发现/轮转。
func (c *Client) ListNames(path string) ([]string, error) {
	entries, err := c.wd.ReadDir(path)
	if err != nil {
		// 空目录部分实现 404 → 视为空
		if gowebdav.IsErrNotFound(err) {
			return nil, nil
		}
		return nil, classify(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out, nil
}

// Move 尝试 MOVE（overwrite=true）；失败由上层据 HEAD 判定是否走 fallback PUT。
func (c *Client) Move(from, to string) error { return classify(c.wd.Rename(from, to, true)) }

// classify 把底层错误映射为稳定错误码语义（不改原始 err，供上层判断）。
func classify(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case gowebdav.IsErrCode(err, http.StatusUnauthorized):
		return fmt.Errorf("%w (401)", ErrAuth)
	case gowebdav.IsErrCode(err, http.StatusForbidden):
		return fmt.Errorf("%w (403)", ErrAuth)
	}
	return err
}

// IsNotFound 报告 err 是否为"对象不存在(404)"。手动删除备份时用于把 404 视作
// 幂等成功（远端对象本就不在），避免对已轮转/已删的归档重复报错。
func IsNotFound(err error) bool { return err != nil && gowebdav.IsErrNotFound(err) }
