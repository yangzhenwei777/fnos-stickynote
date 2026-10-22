package middleware

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tempInstanceDir 与 handler 包 tempInstanceDir 同一根治模式（v1.0.175，CI run 35313835610：
// TestDynamicLimit_ConfigZero 在 -race 下 TempDir RemoveAll ENOTEMPTY）——
// modernc sqlite 连接池 finalizer 释放/WAL 附属文件回收与 t.TempDir 一次性强删存在
// 毫秒级竞态；有界重试（≤3s）吸收瞬态，重试耗尽仍残留才报错，不掩盖真泄漏。
// 两处 database.Init(t.TempDir()) 均经本助手；注册顺序要求：先本函数、后关库 cleanup。
func tempInstanceDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mwdtest-"+strings.ReplaceAll(t.Name(), "/", "_")+"-")
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

func tempDBPath(t *testing.T) string { return filepath.Join(tempInstanceDir(t), "test.db") }
