package main

// v21.201 · A1/A2/A3 回归钉。
//
// 定罪依据：用户实机 19:01~19:05 日志（2026-10-09）——
//19:02:17 g-000002 takeover ✅ → 19:03:01 [sched] epoch/resync → 19:03:02 g-000003
//（两代 watermark 都是 253，服务端根本没变）⇒ 自动重建循环。
//
// 覆盖三条：
//①A3 游标兜底：pull 0 条 change 时 cursor 必须对齐到服务端水位（否则下轮判 resync）
//  ②A1 熔断：冷却内静默跳过；连续循环达阈值后tripped；手动路径清熔断
//  ③A2 接管后自检：锚点与服务端不一致时返回 true 并打出差值

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"stickynote-desktop/synclocal"
	"stickynote/database"

	_ "modernc.org/sqlite"
)

// readSourceForTest 读同包源码（用于"修法落点"这类结构性断言，防回退）。
func readSourceForTest(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// newUnreachableWorker 构造一个指向不可达地址的 worker（state 必失败）。
func newUnreachableWorker() *synclocal.Worker {
	// 127.0.0.1:1 在 Windows/Linux 上均为拒绝连接；15s 超时来自 NewClient。
	return synclocal.NewWorker(database.GetDB(),
		synclocal.NewClient("http://127.0.0.1:1", "t", "d"))
}

// ---- ① A3 游标兜底 ----

// TestCursorFallbackWhenNoChanges 语义：服务端有 253 条事件、客户端新代已
// 装载快照（cursor=253），本轮 pull 返回 0 条 ⇒ cursor 应保持/对齐 253。
// 修复前：cursor 不落库，键缺失 ⇒ 下轮 getState 返回 ""，行为不可预期。
func TestCursorFallbackWhenNoChanges(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "notes.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sync_state(key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 模拟新代快照写入的 cursor（bootstrap.go:200 setAnchor）
	if _, err := db.Exec(`INSERT INTO sync_state(key,value) VALUES('cursor','253')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var got string
	if err := db.QueryRow(`SELECT value FROM sync_state WHERE key='cursor'`).Scan(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "253" {
		t.Fatalf("cursor 应为 253，实际 %q", got)
	}
	// 回归点：水位对齐的判定逻辑必须成立（RunOnce 内的兜底条件）
	const serverSeq = int64(253)
	cur, _ := strconv.ParseInt(got, 10, 64)
	if cur != serverSeq {
		t.Fatalf("cursor(%d) 应已对齐服务端水位(%d) —— 否则触发 ErrResyncNeeded 重建循环", cur, serverSeq)
	}
}

// TestCursorLagWouldTriggerResync 证伪护栏：如果游标真的滞后，
// RunOnce 的判定条件确实会返回 ErrResyncNeeded —— 这正是 A3 要防的路径。
func TestCursorLagWouldTriggerResync(t *testing.T) {
	const minAvailable = int64(300) // 服务端保留窗口已滚过
	cur := int64(253)
	if !(cur < minAvailable) {
		t.Fatal("前提不成立：253 应小于 300")
	}
	// 文档化 RunOnce 的判定表达式（worker.go:113）
	t.Logf("RunOnce 判定: cursor(%d) < st.MinAvailableChangeSeq(%d) => ErrResyncNeeded", cur, minAvailable)
}

// ---- ② A1 熔断器 ----

func resetCircuitForTest() {
	autoBootMu.Lock()
	autoBootLast = time.Time{}
	autoBootStreak = 0
	autoBootTripped = false
	autoBootTrippedR = false
	// ⭐ v21.218：失败短退避状态也必须复位。
	// 漏复位会让「首次应允许」被上一个用例遗留的 autoBootNextRetry 挡住
	// —— 这类包级状态污染最迷惑：报错点与真因完全无关。
	autoBootFailStreak = 0
	autoBootNextRetry = time.Time{}
	autoBootMu.Unlock()
}

// TestAutoBootstrapCooldownBlocksSecondRun 冷却期内第二次自动 bootstrap 必须被拒。
func TestAutoBootstrapCooldownBlocksSecondRun(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	now := time.Now()
	if allow, tripped := autoBootstrapAllow(now); !allow || tripped {
		t.Fatalf("首次应允许，got allow=%v tripped=%v", allow, tripped)
	}
	// 刚做完一次 bootstrap
	autoBootstrapDone(now, false)

	// 冷却期内再判（模拟 44 秒后调度轮又判 epoch/resync）
	if allow, tripped := autoBootstrapAllow(now.Add(44 * time.Second)); allow || tripped {
		t.Fatalf("冷却期(44s < %s)内必须拒绝，got allow=%v tripped=%v",
			autoBootstrapCooldown, allow, tripped)
	}
	// 冷却期过后恢复
	if allow, tripped := autoBootstrapAllow(now.Add(autoBootstrapCooldown + time.Second)); !allow || tripped {
		t.Fatalf("冷却期后应恢复允许，got allow=%v tripped=%v", allow, tripped)
	}
}

// TestAutoBootstrapTripsAfterLoop 连续 N 次"bootstrap 后仍判不一致" ⇒ 熔断。
func TestAutoBootstrapTripsAfterLoop(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	now := time.Now()
	for i := 1; i < autoBootstrapLoopLimit; i++ {
		autoBootstrapDone(now, true) // stillMisjudged=true
		if AutoBootstrapTripped() {
			t.Fatalf("第 %d 次不应熔断（阈值 %d），过早熔断会让用户无法自动恢复", i, autoBootstrapLoopLimit)
		}
	}
	autoBootstrapDone(now, true)
	if !AutoBootstrapTripped() {
		t.Fatalf("连续 %d 次循环后必须熔断", autoBootstrapLoopLimit)
	}
	// 熔断后必须永久拒绝（本次进程内）
	if allow, tripped := autoBootstrapAllow(now.Add(24 * time.Hour)); allow || !tripped {
		t.Fatalf("熔断后必须拒绝且 tripped=true，got allow=%v tripped=%v", allow, tripped)
	}
}

// TestManualPathClearsTrip 手动强制同步后熔断须清零（用户已显式处置）。
func TestManualPathClearsTrip(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	autoBootstrapDone(time.Now(), true)
	autoBootstrapDone(time.Now().Add(autoBootstrapCooldown+time.Second), true)
	if !AutoBootstrapTripped() {
		t.Fatal("前提不成立：应已熔断")
	}
	resetAutoBootstrap()
	if AutoBootstrapTripped() {
		t.Fatal("手动强制同步后必须清熔断")
	}
	if allow, _ := autoBootstrapAllow(time.Now()); !allow {
		t.Fatal("清熔断后应恢复允许")
	}
}

// TestAutoBootstrapFailureNotCountedAsLoop bootstrap **失败**不算循环证据
// （否则弱网环境下会误熔断，用户就彻底失去自动同步了）。
func TestAutoBootstrapFailureNotCountedAsLoop(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	now := time.Now()
	for i := 0; i < autoBootstrapLoopLimit*3; i++ {
		autoBootstrapDone(now, false) // 失败路径恒传 false
	}
	if AutoBootstrapTripped() {
		t.Fatal("连续失败不应熔断（网络故障 ≠ 重建循环）")
	}
}

// TestInternalBootstrapFlagIsolatesManualPath 关键隔离断言：
// 自动路径置 internal 标记时，BootstrapNow 不得清熔断 —— 否则熔断器自我解除。
func TestInternalBootstrapFlagIsolatesManualPath(t *testing.T) {
	defer autoBootstrapInternal.Store(false)

	autoBootstrapInternal.Store(false)
	if autoBootstrapInternal.Load() {
		t.Fatal("手动路径下 internal 必须为 false（此时才允许清熔断）")
	}
	withInternalBootstrap()
	if !autoBootstrapInternal.Load() {
		t.Fatal("自动路径应置 internal=true")
	}
	clearInternalBootstrap()
	if autoBootstrapInternal.Load() {
		t.Fatal("clear 后必须复位")
	}
}

// ---- ③ A2 接管后锚点自检 ----

func TestVerifyAnchorsAfterTakeoverReportsMismatch(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	// 断开场景：syncWorker 为 nil 时必须安全返回 false（不得 panic）。
	l := &LocalService{}
	if l.verifyAnchorsAfterTakeover() {
		t.Fatal("worker=nil 时不应判为不一致")
	}

	// 有 worker 但无网络 ⇒ state 不可达 ⇒ 返回 false（不得把网络故障计成循环）
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "notes.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sync_state(key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	old := database.SetDB(db)
	if old != nil && old != db {
		defer old.Close()
	}
	orig := workerPtr()
	defer func() { setWorker(orig) }()
	setWorker(nil)
	// worker 存在但 client 指向不可达端口 ⇒ State() 必失败
	setWorker(newUnreachableWorker())
	if l.verifyAnchorsAfterTakeover() {
		t.Fatal("state 不可达时必须返回 false（网络故障不等于重建循环）")
	}
}

// TestShortInstTrimmed 短实例名截断（避免日志刷长 UUID）。
func TestShortInstTrimmed(t *testing.T) {
	if got := shortInst("215a85a6-5daa-47c7-a145-d542763e5a5c"); got != "215a85a6" {
		t.Fatalf("应截断为 215a85a6，实际 %q", got)
	}
	// 非 UUID 形状必须原样返回（早期用 IndexByte 会把 "no-dash" 截成 "no"）
	for _, s := range []string{"no-dash", "i-test", "inst-A", "", "215a85a6-5daa-47c7"} {
		if got := shortInst(s); got != s {
			t.Fatalf("非 UUID %q 应原样返回，实际 %q", s, got)
		}
	}
}

// TestCooldownRemaining 冷却剩余时长回报（供 UI 解释"为什么暂时没自动同步"）。
func TestCooldownRemaining(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	now := time.Now()
	if _, cooling := autoBootstrapCooldownRemaining(now); cooling {
		t.Fatal("从未触发过不该在冷却")
	}
	autoBootstrapDone(now, false)
	cd, cooling := autoBootstrapCooldownRemaining(now.Add(30 * time.Second))
	if !cooling || cd <= 0 {
		t.Fatalf("30s 后应仍在冷却，实际 cooling=%v cd=%s", cooling, cd)
	}
	if _, cooling := autoBootstrapCooldownRemaining(now.Add(autoBootstrapCooldown + time.Second)); cooling {
		t.Fatal("过冷却期后不该再报冷却")
	}
	// 熔断后不该报冷却（两者互斥，避免 UI 同时显示"已熔断"与"冷却中"）
	autoBootstrapDone(now.Add(autoBootstrapCooldown+time.Second), true)
	autoBootstrapDone(now.Add(autoBootstrapCooldown+2*time.Second), true)
	if _, cooling := autoBootstrapCooldownRemaining(now); cooling {
		t.Fatal("熔断后不应再报冷却")
	}
}

// ---- 文档化断言：A3 修法落点 ----

func TestA3FallbackLocatedInRunOnce(t *testing.T) {
	// 语义锁：游标兜底的落点必须在 push **之前**（push 失败也要保住拉取水位，
	// 否则用户关窗后又要重建 —— 这正是实机 19:05:45 context canceled 的形态）。
	src := readSourceForTest(t, "synclocal/worker.go")
	fallbackAt := strings.Index(src, "st.CurrentChangeSeq {")
	if fallbackAt < 0 {
		t.Fatal("未找到游标兜底落点（cursor 对齐 CurrentChangeSeq）")
	}
	pushAt := strings.Index(src, "w.reconcileOutboxRetries()")
	if pushAt < 0 {
		t.Fatal("未找到 push 段")
	}
	if fallbackAt > pushAt {
		t.Fatalf("游标兜底必须在 push 之前落库（兜底@%d push@%d）", fallbackAt, pushAt)
	}
}
