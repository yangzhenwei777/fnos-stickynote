//go:build !windows

package backup

import "syscall"

// availableBytes 用 statfs 探测 dir 所在文件系统剩余可用字节（真目标 fnOS/Linux）。
// Bavail * Frsize = 非特权可用空间。
func availableBytes(dir string) (int64, bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false, err
	}
	bsize := int64(st.Bsize)
	// 某些平台 Frsize 与 Bsize 不同；用 Bavail*Bsize 足够（保守）。
	free := int64(st.Bavail) * bsize
	return free, true, nil
}
