//go:build windows

package restore

// fsyncDir 在 Windows 上是 no-op：对目录 Open+Sync 被拒绝，且 Windows 不承诺目录级
// crash-consistency。开发环境（本机 Windows）用此实现；真正的崩溃一致性以 fnOS/Linux 为准。
func fsyncDir(string) error { return nil }
