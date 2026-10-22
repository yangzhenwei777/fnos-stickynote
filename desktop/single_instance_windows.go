//go:build windows

// single_instance_windows.go —— v21.169 单实例互斥（Windows）。
//
// 定罪（用户实机2026-10-12 证据，日志 app-2026-10-12.log）：
//
//	18:00:53  进程启动（file logging enabled）
//	18:01:08  又一次启动 ← 前一进程尚未退出
//	...
//	18:02:37  [sched] 后台同步轮次失败: AUTH_REVOKED
//	          → session demoted to re-exchange ⇒ 掉登录 + 笔记不可见
//
// 机制：服务端的 refresh 是「**一次一换 + 族重放吊销**」——同一张封存票被用第二次，
// 服务端即撤销整个 family（server/handler/auth.go 的重放检测与 RotateRefreshSessionTx
// 的 CAS 未命中分支）。而桌面的换票单飞锁 `exchangeMu` **只在进程内有效**：
// 两个进程并存时各自换票，必然有一方花掉另一方正在用的那张票 ⇒ 整族被吊销。
// 「反复开关几次后掉登录」正是这个竞态的稳定复现面。
//
// 修法：进程级单实例互斥。第二个实例发现已存在即**直接退出**（不弹窗、不抢票）。
// 命名按 profile 隔离：绿色版（portable.mode）与安装版数据目录不同，本就该并存，
// 各自的 localDataDir 不同 ⇒ 互斥体名字也不同，互不误伤。

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// acquireSingleInstance 尝试获取本档案目录的单实例互斥体。
// 返回的 release 必须在进程退出前调用；acquired=false 表示已有实例在跑，应立即退出。
func acquireSingleInstance(marker string) (release func(), acquired bool) {
	name := "Local\\StickyNote-single-" + sanitizeMutexName(marker)
	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(name))
	if err != nil {
		// x/sys/windows 的 CreateMutex 在**对象已存在**时直接返回 ERROR_ALREADY_EXISTS
		// （实测：错误类型为 syscall.Errno(183)，**不是** windows.Errno）⇒「已有实例」。
		// 直接退出，不弹窗、不抢票（本进程不得参与换票，否则族重放吊销）。
		//
		// ⚠ 两个已实测踩过的坑：①类型断言 `err.(windows.Errno)` **永远不成立**
		// （syscall.Errno 与 windows.Errno 属不同类型，断言静默失效 ⇒ 当成未知错误
		//   放行了第二个进程，正是本缺陷本身）；②`errors.Is` 对裸 errno 同样不成立。
		// 因此这里**按数值比较**，这是唯一在这两种类型下都可靠的判据。
		var errno syscall.Errno
		if errors.As(err, &errno) && errno == syscall.Errno(windows.ERROR_ALREADY_EXISTS) {
			// ⚠ 必须关掉 CreateMutex 已打开的 handle：内核对象在**所有 handle 关闭**前
			// 不会销毁。不关的话，上一实例退出后对象仍存活 ⇒ 新实例永远拿不到互斥体
			// ⇒ 用户重开桌面被永久拦死（已实测踩过：release 后仍取不到）。
			if h != 0 {
				_ = windows.CloseHandle(h)
			}
			return func() {}, false
		}
		// 其他错误（命名空间不可用等，极少见）：**不阻断启动**——
		// 单实例是加固，不是功能前提，误拒启动的代价远大于并发。
		logMutexWarn(err)
		return func() {}, true
	}
	return func() { _ = windows.CloseHandle(h) }, true
}

// sanitizeMutexName 把任意路径压成可用作互斥体名的短串（SHA-256 前 16 hex）。
func sanitizeMutexName(s string) string {
	// 避免引入 crypto 依赖到主包：直接 FNV-1a，与项目内既有哈希口径一致。
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	const hexd = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 0; i < 8; i++ {
		out[i] = hexd[(h>>(uint32(i*8)))&0xf]
		out[8+i] = hexd[(h>>(uint32(i*8+4)))&0xf]
	}
	return string(out)
}

func logMutexWarn(err error) {
	fmt.Fprintf(os.Stderr, "[single-instance] 互斥体创建失败，本轮不阻断启动: %v\n", err)
}
