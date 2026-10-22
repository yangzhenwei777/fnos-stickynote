package model

import (
	"os"
	"strings"
	"testing"
	"time"
)

// tempInstanceDir 测试专用临时实例目录：替代 t.TempDir() 的"一次性强删"。
//
// 背景（CI run 35495381838 实锤，model 面首例）：Ubuntu runner 上 t.TempDir 自动清理
// 偶发 `RemoveAll ...: directory not empty`——modernc sqlite 连接池的最后一名清理者
// （GC finalizer 路径）释放句柄与 WAL 附属文件回收同 RemoveAll 遍历存在毫秒级竞态。
// handler 包同工装（v1.0.175 根治）已验证有效；本包 setupQuotaDB 等漏配，本次补齐。
// 处置与 restore 包 renameWithRetry 同款：有界重试（≤3s）吸收瞬态 ENOTEMPTY；
// 重试耗尽仍残留则 t.Errorf——不掩盖真正的句柄泄漏。
// 注册顺序：本函数须在"关库 cleanup"之前调用（LIFO 下 remove 最后执行）。
func tempInstanceDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sntest-"+strings.ReplaceAll(t.Name(), "/", "_")+"-")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() {
		var last error
		for i := 0; i < 60; i++ {
			if last = os.RemoveAll(dir); last == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("temp dir cleanup failed after 3s (lingering handle?): %v", last)
	})
	return dir
}
