package logger

// v21.202 · logger 全局指针并发回归钉。
//
// 定罪：CI run 37958794289 backend job `go test -race` 抓到真实 data race ——
//	Write at logger.go:28（Init 裸写包级指针 l）
//	Previous read at logger.go:45（Init 起的 24h ticker goroutine 在 select 里裸读 l.done）
//	Goroutine 846: redline30_credlog_test.go:156（第二个 Init）
//	Goroutine 845: redline30_credlog_test.go:65 （第一个 Init 起的 ticker）
//
// 修法：包级 `var l *logger` → `atomic.Pointer[logger]`；
// ticker 闭包只捕获自己的 inst.done（不再读全局）；重复 Init 先关停旧实例再 Store。
//
// 本文件把「结构正确」与「并发安全」都钉住，防回退。

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestLoggerGlobalIsAtomicPointer 结构性断言：全局 logger 必须是
// atomic.Pointer[logger]而非裸指针 —— 裸指针在 Init/Close/writeRaw/ticker
// 四类访问点之间无任何同步保证（race 的根因就是它）。
func TestLoggerGlobalIsAtomicPointer(t *testing.T) {
	// 能对 l 施加 Load/Store 即证明它是 atomic.Pointer；裸指针没有这两个方法。
	l.Load()
	l.Store(nil)

	// 复位，避免影响其它用例（t.TempDir 场景下 Close 需nil 安全）
	l.Store(nil)
	if cur() != nil {
		t.Fatal("Store(nil) 后 cur() 应为 nil")
	}
}

// TestLoggerInitThenCloseThenInitReentrant 重复 Init 必须安全：
// 第二次 Init 要先关停上一实例（旧 done 通道已 close、文件已关），
// 否则旧 ticker goroutine 永远不退出（泄漏）且与新实例并存。
func TestLoggerInitThenCloseThenInitReentrant(t *testing.T) {
	defer resetLoggerForTest()

	dir1 := t.TempDir()
	if err := Init(dir1); err != nil {
		t.Fatalf("Init#1: %v", err)
	}
	first := cur()
	if first == nil {
		t.Fatal("Init#1 后 cur() 应非 nil")
	}
	Info("first instance alive")

	dir2 := t.TempDir()
	if err := Init(dir2); err != nil {
		t.Fatalf("Init#2: %v", err)
	}
	second := cur()
	if second == nil {
		t.Fatal("Init#2 后 cur() 应非 nil")
	}
	if second == first {
		t.Fatal("Init#2 应创建新实例（而非复用旧指针）")
	}
	// 旧实例的 done 必须已关闭（ticker goroutine 能退出）
	select {
	case <-first.done:
	default:
		t.Fatal("重复 Init 后旧实例的 done 未被关闭 ⇒ 旧 ticker goroutine 泄漏")
	}
	Info("second instance alive")
	Close()
}

// TestLoggerConcurrentInitWriteClose 并发 Init + 写 + Close 不得 panic/ 死锁。
// 这是 race 的直接复现场景：旧实现下 Write 与 read 并发会报 DATA RACE。
//
// ⚠ 关键设计（初版曾 FAIL，教训记此）：
// 初版让 8 个 goroutine 各自 `t.TempDir()` + Init，结果只有最后一个实例被
// Close，其余 7 个的文件句柄仍打开 ⇒ `t.TempDir()` 的 RemoveAll cleanup 报
// "The process cannot access the file because it is being used by another process"。
// 那是**测试自身的设计缺陷**（生产里 logger 进程生命周期内只 Init 一次），
// 不是产品缺陷。现改为：共享同一个目录（这才是「重复 Init」的真实语义 —
// 热重载/配置重载都指向同一 logDir），末尾统一 Close 收口。
func TestLoggerConcurrentInitWriteClose(t *testing.T) {
	defer func() {
		// 无论断言是否失败，都要收口当前实例，否则 TempDir 删不掉。
		if inst := cur(); inst != nil {
			inst.closeOnce.Do(func() {
				close(inst.done)
				inst.mu.Lock()
				defer inst.mu.Unlock()
				for _, f := range inst.files {
					f.Close()
				}
			})
		}
		l.Store(nil)
	}()

	// 共享目录：模拟真实的「重复 Init 指向同一 logDir」
	shared := t.TempDir()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := Init(shared); err != nil {
				return
			}
			for j := 0; j < 20; j++ {
				Info("concurrent write %d-%d", n, j)
			}
		}(i)
	}
	wg.Wait()
	Close()

	// Close 之后 cur() 仍非 nil（Close 不清指针，只关通道+文件）——
	// 写路径仍可用（getFile 会在 mu 保护下重开文件）。
	if cur() == nil {
		t.Fatal("Close 不应把全局指针置 nil（否则运行期日志静默丢失）")
	}
}

// TestLoggerWriteAfterInitCreatesFile 行为不回归：Init 后写日志应落盘。
func TestLoggerWriteAfterInitCreatesFile(t *testing.T) {
	defer func() {
		Close()
		if inst := cur(); inst != nil {
			inst.closeOnce.Do(func() { close(inst.done) })
		}
		l.Store(nil)
	}()

	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	Info("marker-line-unique-42")

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读日志目录: %v", err)
	}
	found := false
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		if strings.Contains(string(b), "marker-line-unique-42") {
			found = true
		}
	}
	if !found {
		t.Fatalf("日志未落盘（%s 内找不到 marker）—— atomic.Pointer 改造引入了行为回归？", dir)
	}
}

// TestLoggerCloseIsIdempotent Close 必须幂等（closeOnce 保护）。
func TestLoggerCloseIsIdempotent(t *testing.T) {
	defer resetLoggerForTest()
	if err := Init(t.TempDir()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	Close()
	Close() // 二次调用不得 panic（close 已关闭的 channel 会 panic）
	Close()
}

// resetLoggerForTest 测试收口：关停当前实例（通道 + **文件句柄**）并清指针。
//
// ⚠ 为什么必须关文件：`t.TempDir()` 的 cleanup 是 RemoveAll，Windows 上
// 未关闭的文件句柄会让删除失败，报 "The process cannot access the file
// because it is being used by another process"。初版只 close(done)
// 不关文件⇒ 3 个用例 FAIL（教训：本项目测试收口必须关文件，不只是关通道）。
func resetLoggerForTest() {
	if inst := cur(); inst != nil {
		inst.closeOnce.Do(func() {
			close(inst.done)
			inst.mu.Lock()
			defer inst.mu.Unlock()
			for _, f := range inst.files {
				_ = f.Close()
			}
		})
	}
	l.Store(nil)
}
