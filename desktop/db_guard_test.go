package main

// db_guard_test.go — P1-1 回归钉（2026-10-15）。
//
// 验证三层：
//  1. 正常路径透传（护栏不得改变成功/失败语义）；
//  2. 超时路径返回 ErrDBBusy（"永久卡"降为"可报错可重试"的验收语义）；
//  3. 池饱和真实场景：连接被占满时 ListTags 在超时窗内返回而非永久阻塞，
//     且连接释放后恢复可用（回到健康态，护栏不留下永久损伤）。
//
// 反向对照：若无护栏（改动前行为），场景 3 会永久阻塞——这正是 217 死锁受害面
// （ListNotes/ListFolders 等）的观感；本钉守住"调用侧必须有界"。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"stickynote/database"
)

func TestDBReadGuardReturnsResult(t *testing.T) {
	val, err := dbReadGuard(func() (string, error) { return "ok", nil })
	if err != nil || val != "ok" {
		t.Fatalf("正常路径应透传结果: val=%q err=%v", val, err)
	}
}

func TestDBReadGuardPreservesFnError(t *testing.T) {
	sentinel := errors.New("业务错误")
	_, err := dbReadGuard(func() (string, error) { return "", sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("fn 的业务错误必须原样透传，不得被护栏吞掉: %v", err)
	}
}

func TestDBReadGuardTimeout(t *testing.T) {
	old := dbReadTimeout
	dbReadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dbReadTimeout = old })

	started := make(chan struct{})
	release := make(chan struct{})
	val, err := dbReadGuard(func() (string, error) {
		close(started)
		<-release // 模拟长时间持连接（健康场景下不可达；此处仅验超时分支）
		return "late", nil
	})
	close(release) // 让后台 goroutine 退场，不泄漏到下一用例
	if !errors.Is(err, ErrDBBusy) {
		t.Fatalf("超时后应返回 ErrDBBusy，实得 val=%q err=%v", val, err)
	}
	<-started // 确保 fn 确实已进入执行（守卫 select 公平性，防假绿）
}

// TestDBReadGuardPoolSaturated 真实池饱和场景（P1-1 验收钉）：
// MaxOpenConns=2 的池被两个持连接占满 → ListTags（经 dbReadGuard）必须在超时窗内
// 返回 ErrDBBusy，而非像 217 之前那样永久阻塞；连接释放后同一调用恢复成功。
func TestDBReadGuardPoolSaturated(t *testing.T) {
	dir := t.TempDir()
	if err := database.Init(filepath.Join(dir, "notepad.db"), localstoreAppVersion); err != nil {
		t.Skipf("database.Init 不可用，跳过池饱和钉: %v", err)
	}
	t.Cleanup(func() { database.GetDB().Close() })

	old := dbReadTimeout
	dbReadTimeout = 800 * time.Millisecond
	t.Cleanup(func() { dbReadTimeout = old })

	ctx := context.Background()
	c1, err := database.GetDB().Conn(ctx)
	if err != nil {
		t.Skipf("取连接失败，跳过: %v", err)
	}
	c2, err := database.GetDB().Conn(ctx)
	if err != nil {
		c1.Close()
		t.Skipf("取第二连接失败，跳过: %v", err)
	}

	ls := &LocalService{userID: 1}
	done := make(chan error, 1)
	go func() {
		_, err := ls.ListTags()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDBBusy) {
			t.Fatalf("池饱和时应在超时窗内返回 ErrDBBusy，实得: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("池饱和时 ListTags 永久阻塞——P1-1 护栏失效（回归到'永久卡'）")
	}

	// 恢复能力：释放连接后，同一调用应成功返回（空 tags，无错误）。
	c1.Close()
	c2.Close()
	if _, err := ls.ListTags(); err != nil {
		t.Fatalf("连接释放后 ListTags 应恢复可用: %v", err)
	}
}
