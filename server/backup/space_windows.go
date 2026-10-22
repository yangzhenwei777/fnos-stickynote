//go:build windows

package backup

// 开发环境（Windows）：不承诺与 fnOS/Linux 相同的 crash-consistency 语义（GPT §14），
// 磁盘预检在开发机上 no-op（返回 unknown → CheckDiskSpace 放行）。真机交付目标为 Linux，
// 强制由 space_unix.go 生效。

func availableBytes(dir string) (int64, bool, error) {
	return 0, false, nil // ok=false → 调用方视为"无法探测"，不阻断
}
