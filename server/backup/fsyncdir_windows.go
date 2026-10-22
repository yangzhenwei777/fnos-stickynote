//go:build windows

package backup

// 开发环境（Windows）：对目录 Open+Sync 被系统拒绝，且 Windows 非交付目标
// （fnOS/Docker 均 Linux），不承诺同等 crash-consistency 语义（GPT §14）。
// 原子 rename 仍执行；仅目录 fsync 在此为 no-op——真目标 Linux 由 fsyncdir_unix.go 强制。
func fsyncDir(dir string) error { return nil }
