//go:build !windows

// v21.196：非 Windows 平台的空实现（保持 `go vet ./...` / `go test ./...` 跨平台可编译）。

package main

func notifyNative(icon int, title, text string) {}
