package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// 统一网关模式（v2.2.0）：fnOS 官方推荐形态——应用监听 unix socket，
// 飞牛网关（校验 OS 登录态后）经 socket 转发请求，App/FN Connect/浏览器三端统一入口。
// TCP :13389 双轨并存（WebDAV 第三方客户端、局域网直连、桌面端依赖），禁止停用。
// 方案依据：docs/统一网关模式演进方案-20261020.md §1.1。

// DefaultSocketPath 返回统一网关模式的默认 unix socket 路径。
// fnOS 部署环境注入 TRIM_APPDEST（应用安装目录，如 /var/apps/stickynote）；
// 未注入（本地开发 / Windows 测试 / 桌面端）返回空串 = socket 监听禁用。
func DefaultSocketPath() string {
	dest := strings.TrimSpace(os.Getenv("TRIM_APPDEST"))
	if dest == "" {
		return ""
	}
	return filepath.Join(dest, "app.sock")
}

// listenUnixSocket 创建统一网关模式的 unix socket 监听。
//
// 启动语义（方案 §1.1 #1）：
//  1. 先清理残留 socket 文件——异常退出（kill -9/断电）不会走优雅清理，遗留文件
//     会导致 bind 失败，必须先摘除（"address already in use" 的 unix socket 变体）；
//  2. net.Listen("unix", path)；
//  3. Chmod 0o770——属主与同组可读写（unix socket 连接 = 对文件节点的写权限），
//     网关进程经组权限连接，其他用户拒之门外。
//
// 任一步失败即返回错误，由调用方 fail-closed 停止启动：网关模式是 fnOS 官方
// 唯一入口，静默降级（仅 TCP）会复现本项目反复踩过的「进程活着但官方入口
// 打不开」的静默瘫痪形态（详见开发纪律：fail-safe 优于 fail-open 的例外——
// 官方入口缺失比服务不可用更难排查，宁可启动失败让 supervisor 3s 重试）。
func listenUnixSocket(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o770); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("chmod socket %s: %w", path, err)
	}
	return ln, nil
}

// cleanupUnixSocket 优雅退出时摘除 socket 文件（方案 §1.1 #4）。
// 幂等：文件不存在（已异常路径）静默通过。
func cleanupUnixSocket(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}
