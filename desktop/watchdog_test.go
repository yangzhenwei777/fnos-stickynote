package main

// watchdog_test.go —— v21.177：看门狗转储通道的形状钉。
// 216 死锁定案的教训是「静默瘫痪不可接受」；看门狗是它的取证通道，必须有测试守住。
import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stickynote/database"
)

// TestWriteGoroutineDump 转储文件可写、落位正确、含池统计头与 goroutine 栈。
func TestWriteGoroutineDump(t *testing.T) {
	dir := t.TempDir()
	var st sql.DBStats
	if db := database.GetDB(); db != nil {
		st = db.Stats() // 有全局库则用真实统计；无则零值（两态均应可写）
	}
	p, err := writeGoroutineDump(dir, st)
	if err != nil {
		t.Fatalf("writeGoroutineDump: %v", err)
	}
	if filepath.Dir(p) != filepath.Join(dir, "logs") {
		t.Fatalf("转储应落到 logs/ 子目录, got %s", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "DB.STATS: MaxOpen=") {
		t.Fatal("转储缺少 DB.STATS 统计头")
	}
	if !strings.Contains(s, "goroutine ") {
		t.Fatal("转储缺少 goroutine 栈")
	}
}
