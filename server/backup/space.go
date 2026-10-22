package backup

import "fmt"

// 磁盘空间动态预检（v4.3-final GPT §6，弃硬编码 2.5×）。
//
// required = snapshot_peak + archive_staging + restore_staging + rollback_reserve + safety_margin
// 备份前与恢复前用不同估算（恢复时同时存在 原始 + .pre-restore + 解包 staging + 新档 等）。

// SpaceRequest 一次操作的磁盘需求分解。
type SpaceRequest struct {
	SnapshotPeak    int64 // VACUUM INTO 快照峰值（≈ db 大小）
	ArchiveStaging  int64 // tar.gz + sealed .snbak 临时（≈ 归档字节数）
	RollbackReserve int64 // 恢复用 .pre-restore 占用（备份时为 0）
	SafetyMargin    int64 // 固定安全余量
}

func (r SpaceRequest) total() int64 {
	return r.SnapshotPeak + r.ArchiveStaging + r.RollbackReserve + r.SafetyMargin
}

// CheckDiskSpace 校验 dir 所在文件系统剩余空间 ≥ req.total()。
// 平台实现见 space_unix.go（真目标 fnOS/Linux 强制）/ space_windows.go（开发环境 no-op）。
func CheckDiskSpace(dir string, req SpaceRequest) error {
	free, ok, err := availableBytes(dir)
	if err != nil {
		// 无法探测时不阻断（保守：真目标 Linux 一定可探测；异常环境放行但日志由调用方决定）
		return nil
	}
	if !ok {
		return nil
	}
	if free < req.total() {
		return fmt.Errorf("%w: need %d bytes, free %d bytes on %s", ErrDiskInsufficient, req.total(), free, dir)
	}
	return nil
}
