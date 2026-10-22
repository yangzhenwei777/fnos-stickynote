package handler

import (
	"testing"
	"time"

	"stickynote/database"
)

// ============================================================================
// loginGuard DB 持久化单测（v21.93 P1：初始化时序回归点）
//
// 缺陷：loadFromDB() 原先挂在包级 init() 内，而 init() 早于 main()/StartServer，
// 此时 database.DB 尚未初始化 → 首行 nil 检查直接 return → 正常启动时
// 「从 DB 恢复未过期失败记录」从未生效（全库仅此一个调用点）。
// 修复后由 StartServer 在 database.Init 之后显式调用 handler.InitLoginGuard()。
// ============================================================================

// TestInitLoginGuard_LoadsFromDB 显式调用应把 DB 中未过期的失败记录载入内存。
func TestInitLoginGuard_LoadsFromDB(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()

	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	now := time.Now().Unix()
	const key = "victim|10.0.0.1"
	if _, err := database.DB.Exec(
		"INSERT OR REPLACE INTO login_fails (key, count, window_start, locked_until) VALUES (?, ?, ?, ?)",
		key, 5, now, now+600); err != nil {
		t.Fatalf("insert login_fails: %v", err)
	}

	// 关键：必须是 DB 就绪之后调用（模拟 StartServer 中 database.Init 之后的调用点）
	InitLoginGuard()

	guard.mu.Lock()
	rec, ok := guard.fails[key]
	guard.mu.Unlock()
	if !ok {
		t.Fatalf("InitLoginGuard 应从 DB 恢复记录 %q（内存 keys=%d）", key, len(guard.fails))
	}
	if rec.count != 5 {
		t.Errorf("恢复的失败计数应为 5, got %d", rec.count)
	}
	if !time.Now().Before(rec.lockedUntil) {
		t.Errorf("恢复的 lockedUntil 应在未来, got %v", rec.lockedUntil)
	}
}

// TestInitLoginGuard_IgnoresExpiredRows 已过期记录不应被载入（避免重启后误锁）。
func TestInitLoginGuard_IgnoresExpiredRows(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()

	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	now := time.Now().Unix()
	// 锁定已过期 且 窗口也已过期 → 不满足 loadFromDB 的 WHERE 条件
	if _, err := database.DB.Exec(
		"INSERT OR REPLACE INTO login_fails (key, count, window_start, locked_until) VALUES (?, ?, ?, ?)",
		"stale|10.0.0.2", 9, now-10000, now-9000); err != nil {
		t.Fatalf("insert stale login_fails: %v", err)
	}

	InitLoginGuard()

	guard.mu.Lock()
	_, ok := guard.fails["stale|10.0.0.2"]
	guard.mu.Unlock()
	if ok {
		t.Error("已过期记录不应被载入内存")
	}
}

// ============================================================================
// 持久化串行队列单测（v21.93 P1：乱序/复活竞态回归点）
//
// 缺陷：persistFail/persistDelete 各自起 goroutine，落库顺序无保证。
// 「失败 UPSERT」与「成功 DELETE」可能以 DELETE→UPSERT 提交，
// 刚成功登录的账号在 DB 中被旧失败记录复活，重启后误锁定。
// ============================================================================

// TestPersistQueue_UpsertLands 失败记录应能落库（队列链路本身可用）。
func TestPersistQueue_UpsertLands(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	InitLoginGuard() // 幂等启动 worker
	resetGuard(t)
	t.Setenv("LOGIN_MAX_FAILS", "10")

	guard.fail("bob", "10.8.8.8")
	persistFlush()

	var n int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM login_fails WHERE key = ?", "bob|10.8.8.8").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("失败记录应已落库")
	}
}

// TestPersistQueue_SuccessAfterFailNoResurrect 核心回归点：失败→成功之后，
// DB 中不得残留该账号的失败记录（异步乱序曾导致记录被复活）。
func TestPersistQueue_SuccessAfterFailNoResurrect(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	InitLoginGuard()
	resetGuard(t)
	t.Setenv("LOGIN_MAX_FAILS", "10") // 避免中途锁定干扰本例意图

	const ip = "10.9.9.9"
	const key = "alice|10.9.9.9"

	for i := 0; i < 3; i++ {
		guard.fail("alice", ip)
	}
	guard.success("alice", ip) // 成功后清除该 (用户名,IP) 记录
	persistFlush()

	var n int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM login_fails WHERE key = ?", key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("成功后 DB 不应残留 %s 的失败记录（乱序复活），got %d", key, n)
	}
}

// TestPersistQueue_NoDBSafe 队列已启动但 DB 被置空时，投递不得 panic。
func TestPersistQueue_NoDBSafe(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	InitLoginGuard()
	resetGuard(t)

	old := database.DB
	database.DB = nil
	guard.fail("carol", "10.7.7.7")
	guard.success("carol", "10.7.7.7")
	persistFlush()
	database.DB = old
}

// TestInitLoginGuard_NoDBSafe DB 未就绪时调用不得 panic（启动顺序异常/测试场景）。
func TestInitLoginGuard_NoDBSafe(t *testing.T) {
	old := database.DB
	database.DB = nil
	defer func() { database.DB = old }()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DB 为 nil 时 InitLoginGuard 不应 panic: %v", r)
		}
	}()
	InitLoginGuard()
}

// TestPersistQueueDropCounter（v21.97 审计 L-02）：队列满丢弃时原子计数递增，
// persistDropCount() 可查询——供运维确认丢弃规模（登录风暴/DB 不可用信号）。
func TestPersistQueueDropCounter(t *testing.T) {
	oldCh := persistCh
	oldCnt := persistDropTotal.Load()
	defer func() {
		persistCh = oldCh
		persistDropTotal.Store(oldCnt)
	}()

	// 容量 1 的小队列：第一个任务占位，后续投递全部触发丢弃分支
	persistCh = make(chan persistTask, 1)
	persistDropTotal.Store(0)

	enqueue(persistTask{kind: persistUpsert, key: "k0"}) // 入队成功
	enqueue(persistTask{kind: persistUpsert, key: "k1"}) // 满 → 丢弃 + 计数
	enqueue(persistTask{kind: persistUpsert, key: "k2"}) // 满 → 丢弃 + 计数

	if got := persistDropCount(); got != 2 {
		t.Errorf("队列满丢弃应计数 2，实际 %d", got)
	}
}
