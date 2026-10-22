//go:build windows

// v21.196 O4/O3：首启解压提示与 runtime 降级告警的原生弹窗。
// 用 user32.MessageBoxW 直接弹（不经 Wails —— 此时尚未起窗），无新增依赖
// （golang.org/x/sys 已是 desktop 模块直依赖）。
// 非 Windows 构建走 notify_other.go 的空实现，保证 `go vet ./...` 跨平台可过。

package main

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	mbIconInformation = 0x00000040
	mbIconWarning     = 0x00000030
	mbOk              = 0x00000000
)

// notifyNative 弹出模态提示框；title/text 为空时静默返回（不打扰）。
//
// v21.196 修订（**自动化挂起事故**）：模态 MessageBox 会阻塞主线程，而本 exe 会被
// **自动化链路短暂运行**——`wails build` 的 bindings 生成、桌面走查/探针、发布 gate 的
// `-version` 断言等。首启解压提示曾在 bindings 生成阶段把构建**锁死 19 分钟**。
// 处置：凡设置了 `STICKYNOTE_PROFILE_DIR`（工装/沙箱/自动化的统一标志）或
// `STICKYNOTE_NO_DIALOG=1` 的运行，一律**不弹**——真实用户两者都不设，仍必弹。
func notifyNative(icon int, title, text string) {
	if text == "" {
		return
	}
	if os.Getenv("STICKYNOTE_NO_DIALOG") == "1" || os.Getenv("STICKYNOTE_PROFILE_DIR") != "" {
		return
	}
	if title == "" {
		title = "Sticky note"
	}
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	m, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	_, _, _ = proc.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), uintptr(icon|mbOk))
}
