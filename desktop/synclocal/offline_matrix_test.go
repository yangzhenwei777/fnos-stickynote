// offline_matrix_test.go —— 断网矩阵端到端用例（批次 8-Alt 刀 4）。
//
// 与 scheduler_test.go 的分工：那边用假 Round 钉"节奏与状态机"，这边把同一套调度器放到
// **真 HTTP + 进程外独立服务端 + 真本地库**上跑（批次 5 实锤：同进程 httptest 会让
// syncsrv 与桌面共用 database.DB 全局，句柄乒乓产生假通过路径 —— 本文件一律走 bootptest 子进程）。
//
// 三条用例各钉一刀 3 的一个承诺：
//  1. 断网期挂账一条不丢，服务端回归后由**探测节奏自动收敛**（不需要用户点同步）；
//  2. 协议终态（换代）自动停手，不狂刷服务端，只有用户动作能再试一次；
//  3. 真链路上产生的冲突必须被 ListConflicts 完整呈现（kind/实体/操作/远端载荷/允许处置），
//     且 keep-remote 处置后本机与服务端一致。
package synclocal

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"stickynote-desktop/internal/bootptest"
)

// startRealScheduler 把真 worker.RunOnce 接到调度器上（毫秒级节奏，只为让 CI 跑得起）。
// idle 与 probe 分开给值：断网用例要"空闲长 + 探测短"（真实默认形状），
// 终态用例要两者都短（否则健康期根本没机会再撞第二次）。
func startRealScheduler(t *testing.T, w *Worker, idle, probe time.Duration) *SchedulerHolder {
	t.Helper()
	h := &SchedulerHolder{}
	var runs int32
	s, err := NewScheduler(SchedOptions{
		Idle: idle, Probe: probe, MaxBackoff: time.Hour, FirstDelay: 20 * time.Millisecond,
		Round: func(ctx context.Context) error {
			atomic.AddInt32(&runs, 1)
			_, err := w.RunOnce(ctx)
			return err
		},
		Allow: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.s, h.runs = s, &runs
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); s.Stop() })
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

type SchedulerHolder struct {
	s    *Scheduler
	runs *int32
}

func openCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	return countRows(t, db, `SELECT COUNT(*) FROM sync_outbox
		WHERE status IN ('PENDING','SENDING','FAILED','CONFLICT')`)
}

// 1) 断网期不丢账，恢复后自动收敛（探测节奏真的在跑，无人工干预）。
func TestOfflineWritesSurviveOutageAndAutoConverge(t *testing.T) {
	srvData := t.TempDir() // 先建目录再 StartAt：清理顺序决定 Windows 能否删掉库文件
	ts := bootptest.StartAt(t, srvData, bootptest.FreeAddr(t))
	db := newStore(t)
	w := NewWorker(db, NewClient(ts.URL, ts.Token(t), "devA"))
	w.AttachDir = filepath.Join(srvData, "attach-out")

	localCreate(t, db, "s-live", "在线时同步过的一篇")
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ts.HasNote(t, "在线时同步过的一篇") != 1 {
		t.Fatal("前置：服务端应有那篇笔记")
	}

	// ---- 断网：整个服务端进程消失（不是"把 URL 改成假的"，那测不出真实传输失败路径）----
	ts.Stop()
	localCreate(t, db, "s-off1", "断网期间新建一")
	localCreate(t, db, "s-off2", "断网期间新建二")
	if n := openCount(t, db); n != 2 {
		t.Fatalf("断网期两笔挂账应在队，得 %d", n)
	}
	h := startRealScheduler(t, w, time.Hour, 30*time.Millisecond)

	// 断网期：调度器必须进离线态并持续失败，但**账不能动**
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := h.s.State(); st.Offline && st.ConsecutiveFailures >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := h.s.State(); !st.Offline {
		t.Fatalf("断网期应进离线探测态（而不是被当成普通失败去长退避）: %+v", st)
	}
	if n := openCount(t, db); n != 2 {
		t.Fatalf("红线1/6：断网期挂账不得被吞或改写，队里剩 %d 笔", n)
	}
	if st := h.s.State(); st.NeedAttention {
		t.Fatalf("传输失败不该进停手态（网络会回来）: %+v", st)
	}

	// ---- 恢复：同址同库回来，调度器应自己收敛（用户没点任何按钮）----
	ts.Restart(t)
	converged := time.Now().Add(15 * time.Second)
	for time.Now().Before(converged) {
		if ts.HasNote(t, "断网期间新建一") == 1 && ts.HasNote(t, "断网期间新建二") == 1 && openCount(t, db) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := ts.HasNote(t, "断网期间新建一"); got != 1 {
		t.Fatalf("恢复后第 1 笔未收敛（服务端 count=%d，outbox 仍开 %d）", got, openCount(t, db))
	}
	if got := ts.HasNote(t, "断网期间新建二"); got != 1 {
		t.Fatalf("恢复后第 2 笔未收敛（服务端 count=%d，outbox 仍开 %d）", got, openCount(t, db))
	}
	if n := openCount(t, db); n != 0 {
		t.Fatalf("收敛后不该留开账，得 %d", n)
	}
	if st := h.s.State(); st.ConsecutiveFailures != 0 || st.Offline || st.LastOKAt.IsZero() {
		t.Fatalf("收敛后应清失败计数、退出离线态并留下成功时间: %+v", st)
	}
}

// 2) 协议终态自动停手：不狂刷服务端，用户动作才再试一次。
func TestTerminalEpochStopsAutoRoundsE2E(t *testing.T) {
	srvData := t.TempDir()
	ts := bootptest.StartAt(t, srvData, bootptest.FreeAddr(t))
	db := newStore(t)
	w := NewWorker(db, NewClient(ts.URL, ts.Token(t), "devA"))
	localCreate(t, db, "s-ep", "换代前的一篇")
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	h := startRealScheduler(t, w, 20*time.Millisecond, 20*time.Millisecond)
	time.Sleep(150 * time.Millisecond) // 先让它在健康态跑几轮

	ts.ResetEpoch(t) // 服务端整库恢复语义：换代
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := h.s.State(); st.NeedAttention {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := h.s.State()
	if !st.NeedAttention {
		t.Fatalf("换代后必须进停手态: %+v", st)
	}
	if !strings.Contains(st.LastError, "EPOCH") && !strings.Contains(st.LastError, "epoch") {
		t.Fatalf("停手原因必须写明是换代，便于 UI 引导重建: %q", st.LastError)
	}

	// 停手期间不得继续自动打服务端（这条是"狂刷"的硬闸）
	held := atomic.LoadInt32(h.runs)
	time.Sleep(400 * time.Millisecond)
	if got := atomic.LoadInt32(h.runs); got != held {
		t.Fatalf("停手后仍在自动重试：%d → %d", held, got)
	}

	// 用户动作（Wake）→ 恰好一次尝试，仍停手（此时应走 Bootstrap，不由调度器代劳）
	h.s.Wake()
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(h.runs); got != held+1 {
		t.Fatalf("Wake 应只换来一次尝试：%d → %d（期望 %d）", held, got, held+1)
	}
	if !h.s.State().NeedAttention {
		t.Fatal("终态未解除前不得恢复自动节奏")
	}
}

// 3) 真链路上的冲突必须被 Conflict Center 完整呈现，并可处置闭环。
func TestConflictSurfacesInConflictCenterE2E(t *testing.T) {
	ts := bootptest.Start(t)
	a, b := newStore(t), newStore(t)
	wa := NewWorker(a, NewClient(ts.URL, ts.Token(t), "devA"))
	wb := NewWorker(b, NewClient(ts.URL, ts.Token(t), "devB"))
	wb.AttachDir = filepath.Join(t.TempDir(), "attach")

	localCreate(t, a, "s-cf", "母版")
	if _, err := wa.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.RunOnce(context.Background()); err != nil { // B 拿到 v1
		t.Fatal(err)
	}
	var bid int64
	if err := b.QueryRow(`SELECT id FROM notes WHERE sync_id='s-cf'`).Scan(&bid); err != nil {
		t.Fatal(err)
	}
	localEdit(t, b, bid, "B 本机未同步")              // B 变脏（未推）
	localEdit(t, a, idOf(t, a, "s-cf"), "A 已上推") // A 先推上去 → 服务端 v2
	if _, err := wa.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	list, total, err := ListConflicts(b, true, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("真链路撞脏必须产出 1 条可见冲突，得 len=%d total=%d", len(list), total)
	}
	c := list[0]
	if c.Kind != KindConflictOnPull || c.EntityType != "NOTE" || c.Operation != "UPDATE" {
		t.Fatalf("冲突三要素不齐（UI 无法解释这条冲突是什么）: %+v", c)
	}
	if c.RemotePayload == "" || c.LocalPayload == "" {
		t.Fatalf("两侧载荷都必须随行走，否则并排呈现只能靠猜: %+v", c)
	}
	if c.OpenOps < 1 {
		t.Fatalf("未推送改动笔数必须可见，得 %d", c.OpenOps)
	}
	if len(c.AllowedResolutions) != 3 {
		t.Fatalf("NOTE 面撞脏三种处置都该可选，得 %v", c.AllowedResolutions)
	}

	// 处置闭环：接受远端 → 本机内容与服务端一致，待处置归零
	res, err := wb.ResolveConflict(c.ID, ResolveKeepRemote, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AppliedRemote {
		t.Fatalf("处置回执未标记已应用远端: %+v", res)
	}
	var title string
	if err := b.QueryRow(`SELECT title FROM notes WHERE sync_id='s-cf'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "A 已上推" {
		t.Fatalf("keep-remote 后本机应等于服务端内容，得 %q", title)
	}
	if _, total2, err := ListConflicts(b, true, 50, 0); err != nil || total2 != 0 {
		t.Fatalf("处置后待处置计数应归零，得 total=%d err=%v", total2, err)
	}
	if n := openCount(t, b); n != 0 {
		t.Fatalf("被放弃的那笔必须终止，队里还开着 %d 笔", n)
	}
	// 服务端不受处置动作影响（处置是本地决策，不产生新事件）
	if ts.HasNote(t, "A 已上推") != 1 {
		t.Fatal("服务端内容被处置动作改写")
	}
}

// 附带钉一条：终态错误分类在真链路上的判据（防止有人把 epoch 变化降级成普通失败）。
func TestClassifyUsesRealSentinelNotMessageHeuristic(t *testing.T) {
	if classifyRoundErr(ErrEpochChanged) != outcomeTerminal {
		t.Fatal("ErrEpochChanged 必须是终态")
	}
	if classifyRoundErr(errors.New("SERVER_EPOCH_CHANGED")) == outcomeTerminal {
		t.Fatal("仅凭文案命中不算终态——必须是哨兵错误本身，否则误判会把普通网络抖动变停手")
	}
}
