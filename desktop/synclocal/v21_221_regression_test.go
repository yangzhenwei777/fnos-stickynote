// v21_221_regression_test.go —— 214 实机复测定罪的回归钉（package 内，直接用内部函数）。
//
// ══════════════════════════════════════════════════════════════════════════
// 214 实机症状（三方交叉取证）
// ══════════════════════════════════════════════════════════════════════════
//
//	① 桌面端首次登录同步正常，操作全部正常；
//	② 第一次关闭后再次打开 → 设置显示「恢复中」，**很久都不恢复**，笔记与文件夹为空；
//	③ 此后再打开多少次都是「恢复中」，**永久无法使用**。
//
// 取证坐实：
//
//	· 日志：`[sched] 后台同步调度已启动` 之后**再无任何轮次日志**（我实测启动观察 45s 亦然）；
//	· g-000002 的 sync_outbox 积压 **10 条全是 DELETE/PURGE**（目标实体在 Web 端早删了）；
//	· 内网服务器直连 **2ms** 响应、登录正常 ⇒ **网络与服务端均无问题**；
//	· client_profile 的 refresh_sealed 515B 完好 ⇒ 不是凭据丢失。
//
// 两个独立缺陷（互相叠加）：
//
//	【缺陷 1】`planWait` 里 `if s.st.NeedAttention { return 0, true }`
//	         ⇒ hold=true ⇒ loop 中 `tick = nil 通道` ⇒ **定时器永不就绪**
//	         ⇒ 自动轮次**永久停手**，只有用户手动点按钮才 Wake
//	         ⇒ UI 永远停在 `online=false` =「恢复中」永不变化。**这是"无法使用"的直接原因。**
//
//	【缺陷 2】DELETE/PURGE 遇到服务端 `CodeEntityGone`（"目标态已达成"）
//	         被客户端当成 `PUSH_CONFLICT` 塞进 sync_conflicts 要求用户处置
//	         ⇒ 10 条僵尸操作**永久**躺在 outbox，每轮重试每轮失败。
//
// 设计反思（写进注释，防止将来"优化"回去）：
//
//	「需要用户注意」是**提示义务**；「停止自动尝试」是**可用性自杀**。
//	凡 hold=true 的设计，必须回答：「用户什么都不做时，多久能自愈？」答案是"永远"吗？
package synclocal

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"stickynote/syncspec"
)

// ---- 缺陷 1：终态错误不得永久停手 ----

// TestTerminalErrorDoesNotStopSchedulerForever 钉住 214 的直接成因。
func TestTerminalErrorDoesNotStopSchedulerForever(t *testing.T) {
	s, _ := NewScheduler(SchedOptions{
		Idle:       time.Minute,
		FirstDelay: 2 * time.Second,
		Round:      func(context.Context) error { return errors.New("boom") },
	})
	s.mu.Lock()
	s.st.NeedAttention = true
	s.st.ConsecutiveFailures = 1
	s.st.Rounds = 1 // 已跑过一轮，避开「启动首轮」分支
	s.mu.Unlock()

	d, hold := s.planWait()
	// ⭐ 核心断言：**不得 hold**。hold 会让定时器变成 nil 通道，
	// 自动轮次永久停手 ⇒ 214 实机「永久恢复中、无法使用」。
	if hold {
		t.Fatal("NeedAttention 不得导致 hold（hold ⇒ tick 为 nil 通道 ⇒ 自动轮次永久停手，214 实机症状）")
	}
	if d <= 0 {
		t.Fatalf("必须排下一次自动重试，得 d=%s", d)
	}
	if s.State().NextAt.IsZero() {
		t.Fatal("必须设置 NextAt，否则 UI 无法告知用户下次自动重试时间")
	}
}

// TestTerminalBackoffLadder 钉住长退避阶梯：5 → 15 → 30min（封顶）。
func TestTerminalBackoffLadder(t *testing.T) {
	s, _ := NewScheduler(SchedOptions{
		Idle:  time.Minute,
		Round: func(context.Context) error { return nil },
	})
	cases := []struct {
		fail int
		want time.Duration
	}{{1, 5 * time.Minute}, {2, 15 * time.Minute}, {3, 30 * time.Minute}, {50, 30 * time.Minute}}
	for _, tc := range cases {
		s.mu.Lock()
		s.st.NeedAttention = true
		s.st.ConsecutiveFailures = tc.fail
		got := s.nextDelayLocked()
		s.mu.Unlock()
		if got != tc.want {
			t.Fatalf("failures=%d 退避应为 %s，得 %s", tc.fail, tc.want, got)
		}
	}
}

// ---- 缺陷 1 附：AUTH_REVOKED 独立成类 ----

// TestAuthRevokedClassifiedSeparately 钉住「票被作废」与「代际不一致」分家。
func TestAuthRevokedClassifiedSeparately(t *testing.T) {
	if got := classifyRoundErr(ErrAuthRevoked); got != outcomeAuthRevoked {
		t.Fatalf("AUTH_REVOKED 应独立成类（须重登），得 %d", got)
	}
	if got := classifyRoundErr(ErrEpochChanged); got != outcomeTerminal {
		t.Fatalf("ErrEpochChanged 应为终态（退避可自愈），得 %d", got)
	}
	if got := classifyRoundErr(ErrResyncNeeded); got != outcomeTerminal {
		t.Fatalf("ErrResyncNeeded 应为终态（退避可自愈），得 %d", got)
	}
	// 包一层也要能识别（errors.Is 穿透）
	if got := classifyRoundErr(errors.Join(errors.New("runonce"), ErrAuthRevoked)); got != outcomeAuthRevoked {
		t.Fatalf("被包装的 AUTH_REVOKED 也须独立成类，得 %d", got)
	}
}

// ---- 缺陷 2：僵尸 outbox 自动消解 ----

// newOutboxHarness 建最小可用库。
//
// ⚠ 用**真实的 `EnsureTables`** 而不是手抄 DDL —— 手抄必然漂移
// （本次初版就因 `base_version` 字段名对不上而失败）。生产与测试共用同一份 DDL，
// 才谈得上"测试覆盖的是真表结构"。
func newOutboxHarness(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v221.db"))
	if err != nil {
		t.Skipf("sqlite 不可用，跳过：%v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := EnsureTables(db); err != nil {
		t.Skipf("EnsureTables 失败，跳过：%v", err)
	}
	return db
}

// seedOutboxOp 插一条 PENDING 待推操作，返回 (op_id, seq)。
func seedOutboxOp(t *testing.T, db *sql.DB, oper, entity string) (string, int64) {
	t.Helper()
	opID := "op-" + oper + "-" + entity
	res, err := db.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, payload)
		VALUES (?, 'NOTE', ?, ?, '{"sync_id":"`+entity+`"}')`, opID, entity, oper)
	if err != nil {
		t.Fatalf("插 outbox 失败: %v", err)
	}
	seq, _ := res.LastInsertId()
	return opID, seq
}

// TestZombieDeleteOutboxAutoDissolved 钉住 214 实机的 10 条僵尸 PURGE/DELETE 处置。
//
// 语义依据（服务端 push.go:339 原文）：「行不存在（已删过/幂等重放）
// ⇒ IDEMPOTENT_REPLAY（**目标态"不存在"已达成**）」。
// 既然目标态已达成，客户端再留着这条删除操作纯属浪费，
// 而留着 = 阻塞 outbox = 同步不推进 = 用户看到的「笔记一直是空的」。
func TestZombieDeleteOutboxAutoDissolved(t *testing.T) {
	for _, oper := range []string{"DELETE", "PURGE"} {
		db := newOutboxHarness(t)
		opID, seq := seedOutboxOp(t, db, oper, "deadbeef")
		w := NewWorker(db, NewClient("http://127.0.0.1:1", "tok", "dev"))

		b := batch{ops: []syncspec.PushOp{{OpID: opID}}, ids: []int64{seq}, byOp: map[string]int64{opID: seq}}
		acked, err := w.applyResult(syncspec.OpResult{
			OpID: opID, Code: syncspec.CodeEntityGone, Error: "实体已不存在",
		}, b)
		if err != nil {
			t.Fatalf("%s: applyResult 报错: %v", oper, err)
		}
		if !acked {
			t.Fatalf("%s: 目标实体已不存在应视为幂等成功（acked=true），却报未确认", oper)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE seq = ?`, seq).Scan(&n); err != nil {
			t.Fatalf("%s: %v", oper, err)
		}
		if n != 0 {
			t.Fatalf("%s + ENTITY_GONE 必须从 outbox 消解（214 实机 10 条僵尸操作每轮重试每轮失败），仍有 %d 条", oper, n)
		}
		// 关键：不得进冲突面（那要求用户逐条手动处置）
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts`).Scan(&c); err == nil && c != 0 {
			t.Fatalf("%s + ENTITY_GONE 不应产生冲突记录（幂等成功，无需人工介入），却有 %d 条", oper, c)
		}
	}
}

// TestNonDeleteEntityGoneStillConflicts 反向验证：非删除类 ENTITY_GONE **仍需用户决策**。
//
// 本地改动（UPDATE/CREATE）无处安放，必须让用户选「另存副本/以本机为准重投/放弃」，
// 不能自动消解（那等于静默丢用户的编辑）。此钉防"顺手把非删除类也消解"的过度修复。
func TestNonDeleteEntityGoneStillConflicts(t *testing.T) {
	db := newOutboxHarness(t)
	opID, seq := seedOutboxOp(t, db, "UPDATE", "livecafe")
	w := NewWorker(db, NewClient("http://127.0.0.1:1", "tok", "dev"))

	b := batch{ops: []syncspec.PushOp{{OpID: opID}}, ids: []int64{seq}, byOp: map[string]int64{opID: seq}}
	if _, err := w.applyResult(syncspec.OpResult{
		OpID: opID, Code: syncspec.CodeEntityGone, Error: "实体已不存在",
	}, b); err != nil {
		t.Fatalf("applyResult 报错: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE seq = ?`, seq).Scan(&n)
	if n == 0 {
		t.Fatal("UPDATE + ENTITY_GONE 不得消解 outbox（本地编辑无处安放，必须让用户决策）")
	}
	var c int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts`).Scan(&c)
	if c == 0 {
		t.Fatal("UPDATE + ENTITY_GONE 应登记冲突（让用户选另存副本/重投/放弃），却没有冲突记录")
	}
}
