package backup

import "sync"

// GC 锁（v4.3-final A3）：备份采集期只暂停 **GC / 物理删除**，不挡上传与笔记保存。
//
// 源码依据：upload.go:92 直接写最终 UUID 文件名（无 tmp→rename，UUID 不覆盖旧件），
// staged→attached 是存笔记时 UPDATE status（非 rename），真正的一致性窗口只有
// "快照 note_uploads 还在、GC/os.Remove 把文件删了"。故只需让采集期与以下操作互斥：
//
//	CleanOrphan/Staged/PendingUploads、CleanExpiredTrash（6h ticker）、
//	DeleteOrphanUploadFiles、删用户 os.Remove(user.go:207)。
//
// 上传/新笔记保存不取此锁 → 采集几分钟内不会撞 WriteTimeout=120s。
//
// 锁序固定（GPT P1，防死锁）：Application state(restoring) → GC 锁 → DB mutex。
var gcMu sync.Mutex

// WithGCPaused 在持锁（暂停 GC/物理删除）下执行 fn（备份采集窗口）。
func WithGCPaused(fn func() error) error {
	gcMu.Lock()
	defer gcMu.Unlock()
	return fn()
}

// TryRunGC 尝试执行一次 GC/物理删除任务：若采集正持锁（TryLock 失败）则本次跳过、返回 false。
// 供 cmd/server.go 的 6h ticker 与删除路径包裹调用。
func TryRunGC(task func()) bool {
	if !gcMu.TryLock() {
		return false // 备份采集中，跳过本轮 GC（下个周期再做）
	}
	defer gcMu.Unlock()
	task()
	return true
}

// GCPause 显式获取/释放（供需要跨多步持锁的采集/恢复流程；内部一般用 WithGCPaused）。
func GCPause()  { gcMu.Lock() }
func GCResume() { gcMu.Unlock() }
