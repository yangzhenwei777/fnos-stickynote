package webdav

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// SSRF 安全传输层：WebDAV 客户端是"管理员提供 URL → 服务端主动出网"的典型 SSRF 面。
// 防护（v4.3-final §0.3 GPT/Grok）：
//   - 自定义 DialContext **解析后逐个校验 IP**（A 和 AAAA 全部），直接 dial 已校验的 IP，
//     从根上防 DNS rebinding（校验与拨号用同一次解析结果，不给二次解析机会）；
//   - 任一解析出的 IP 命中禁用类别 → 拒绝整个主机的连接（fail-closed）；
//   - 重定向：http.Client 每一跳都会经本 Transport，故重定向到新主机同样被重校验；
//   - 默认禁环回 127/8、::1（allowLoopback 显式开）；链路本地/云元数据/multicast/未指定始终禁；
//     RFC1918 私网放行（fnOS 局域网 WebDAV 是正当场景）。

type ssrfOptions struct {
	allowLoopback bool
	allowPrivate  bool // 默认 true（局域网）
	timeouts      bool
}

var (
	ErrBlockedAddr = errors.New("WEBDAV_SSRF_BLOCKED")
	ErrBlockedURL  = errors.New("WEBDAV_URL_INVALID")
)

// guardDialContext 返回一个做 IP 策略校验的 DialContext。
func guardDialContext(opt ssrfOptions) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	resolver := &net.Resolver{}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		// host 可能已是 IP，也可能是域名；统一解析取全部 A/AAAA。
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("%w: resolve %s: %v", ErrBlockedAddr, host, err)
		}
		var chosen *net.IP
		for i := range ips {
			ip := ips[i].IP
			if ipBlocked(ip, opt) {
				return nil, fmt.Errorf("%w: %s resolves to blocked address %s", ErrBlockedAddr, host, ip)
			}
			if chosen == nil { // 用第一个已校验 IP 直连，避免 transport 二次解析（防 rebinding）
				cp := ip
				chosen = &cp
			}
		}
		if chosen == nil {
			return nil, fmt.Errorf("%w: no usable address for %s", ErrBlockedAddr, host)
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(chosen.String(), port))
	}
}

// ipBlocked 判定 IP 是否禁用。
func ipBlocked(ip net.IP, opt ssrfOptions) bool {
	if ip == nil {
		return true
	}
	switch {
	case ip.IsUnspecified(): // 0.0.0.0 / ::
		return true
	case ip.IsLoopback(): // 127/8、::1
		return !opt.allowLoopback
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(): // 169.254/16、fe80::/10
		return true
	case ip.IsInterfaceLocalMulticast(), ip.IsMulticast(): // 224/4、ff00::/8
		return true
	case isCloudMetadata(ip): // 169.254.169.254 等（含在 link-local，双保险）
		return true
	case ip.IsPrivate(): // RFC1918 + ULA fc00::/7
		return !opt.allowPrivate
	}
	return false
}

func isCloudMetadata(ip net.IP) bool {
	// 169.254.169.254（AWS/GCP/OpenStack 元数据）、fd00:ec2::254（IMDS IPv6）等；
	// 链路本地端点已另由 IsLinkLocalUnicast 覆盖，此处双保险 + 云厂商专属段。
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 169 && v4[1] == 254 && v4[2] == 169 && v4[3] == 254 {
			return true
		}
		// 100.64.0.0/10（RFC6598 运营商级 NAT / 云厂商元数据共享段，含阿里云 IMDS
		// 100.100.100.200）。该段不属于 ip.IsPrivate()（仅覆盖 RFC1918），若不显式拦
		// 会被当公网放行 → SSRF 可达元数据服务（审计 L-03）。
		if v4[0] == 100 && v4[1]&0xC0 == 0x40 { // 100.64.0.0 ~ 100.127.255.255
			return true
		}
	}
	return ip.String() == "fd00:ec2::254"
}

// newTransport 构造带 SSRF 护栏 + 自定义 CA / 超时 / 可选跳过的 http.RoundTripper。
// 供 gowebdav SetTransport 注入；重定向每跳都会经过它（http.Client 逐跳调用 RoundTrip）。
func newTransport(opt ssrfOptions, customCAPEM string, insecureSkipVerify bool) (http.RoundTripper, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if insecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true // 由上层严格门控（仅 fnOS 自签 + 二次确认 + 审计 + 坚果云禁用）
	} else if customCAPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(customCAPEM)) {
			return nil, fmt.Errorf("%w: custom CA PEM 无法解析", ErrBlockedURL)
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Transport{
		DialContext:           guardDialContext(opt),
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}, nil
}

// SchemeAllowed 仅 http/https；拒绝 file/gopher/ftp/unix 等。
func SchemeAllowed(rawURL string) bool {
	// 交由 http 层解析，这里只粗筛前缀
	for _, s := range []string{"https://", "http://"} {
		if len(rawURL) >= len(s) && rawURL[:len(s)] == s {
			return true
		}
	}
	return false
}
