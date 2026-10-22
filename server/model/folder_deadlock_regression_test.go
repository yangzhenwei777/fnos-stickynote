package model

// folder_deadlock_regression_test.go —— v21.177 回归钉（216 实机 P0：连接池嵌套查询死锁）。
//
// 案件：ListFolders 原实现「先开 rows（持连接 1、defer 到函数尾才释放）再发 countRows
// （要连接 2）」，池上限 MaxOpenConns(2)；两个并发调用 ⇒ 各持 1 条、各等 1 条 ⇒ 永久
// 互锁（database/sql 等连接无超时），进程内全链路静默瘫痪。goroutine 转储 + 最小复现
// 双重坐实（报告/216桌面端深度核查-连接池死锁定案）。
//
// 双保险：
//  ① TestListFoldersConcurrentNoPoolDeadlock —— 行为压测：高并发调用必须全部完成
//    （旧实现在此形态下会互锁至超时）。
//  ② TestListFoldersCountQueryBeforeRows —— 源码顺序断言：countRows 查询必须先于
//    rows 查询出现，直接禁止重新引入「持 Rows 再取连接」的嵌套模式。

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestListFoldersConcurrentNoPoolDeadlock 高并发压测：16 worker × 50 轮必须限时完成。
func TestListFoldersConcurrentNoPoolDeadlock(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()
	uid := createUserForTest(t, "deadlock_user")
	for i := 0; i < 5; i++ {
		if _, err := CreateFolder(uid, fmt.Sprintf("f%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	const workers = 16
	const iters = 50
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if _, err := ListFolders(uid); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("并发 ListFolders 20s 未完成——疑似池死锁复现（是否重新引入「持 Rows 再取连接」？见 v21.177 死锁定案）")
	}
	close(errCh)
	for err := range errCh {
		t.Fatalf("ListFolders 失败: %v", err)
	}
}

// TestListFoldersCountQueryBeforeRows 源码顺序断言（防重构回退嵌套模式）。
func TestListFoldersCountQueryBeforeRows(t *testing.T) {
	src, err := os.ReadFile("folder.go")
	if err != nil {
		t.Skipf("读 folder.go 失败，跳过：%v", err)
	}
	text := string(src)
	countIdx := strings.Index(text, "countRows, err := database.DB.Query(")
	rowsIdx := strings.Index(text, "rows, err := database.DB.Query(")
	if countIdx < 0 || rowsIdx < 0 {
		t.Fatalf("未找到预期查询锚点（count=%d rows=%d）——ListFolders 结构可能被大改，请同步审视本钉", countIdx, rowsIdx)
	}
	if countIdx > rowsIdx {
		t.Fatal("ListFolders 中 countRows 查询出现在 rows 之后——重新引入了「持 Rows 再取连接」嵌套模式" +
			"（216 实机 P0 连接池死锁的成因，池上限 2 下两并发调用即永久互锁）")
	}
}
