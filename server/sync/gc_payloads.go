// gc_payloads.go —— §36 Payload Blob GC（P1-b，2026-10-15）。
//
// 只删「无任何有效 sync_changes 引用」的 blob（fail-closed：引用查询失败即整体放弃，
// 绝不误删）。孤儿来源：appendChangeBlob INSERT 成功但事务最终回滚（写盘在事务外，
// 无法随事务撤销）——INSERT 失败路径已在当场清理，此处兜底残余。
// V1 服务端 sync_changes 无保留期清理（事件流只增不减），本 GC 不自动挂载；
// 服务端将来引入 ChangeLog 保留策略时接线（届时引用查询须与保留期条件同口径，
// 且须注意"pull 在途客户端"窗口：客户端拿到 blob_ref 后应尽快下载）。
package sync

import (
	"os"
	"path/filepath"

	"stickynote/changelog"
)

// GCOrphanPayloads 目录扫描 vs 引用集合差集，返回删除数。
func GCOrphanPayloads() (int, error) {
	dir := changelog.PayloadsDir()
	if dir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	refs, err := changelog.ListPayloadRefs(db())
	if err != nil {
		return 0, err // fail-closed：拿不到权威引用集合就一个都不删
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !changelog.ValidPayloadRef(name) {
			continue // 临时件（.payload-*）与非 blob 形状不碰
		}
		if !refs[name] {
			if rmErr := os.Remove(filepath.Join(dir, name)); rmErr == nil {
				removed++
			}
		}
	}
	return removed, nil
}
