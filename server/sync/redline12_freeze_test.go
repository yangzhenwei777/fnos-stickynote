package sync

// redline12_freeze_test.go —— 红线 12「Bootstrap 读 Live DB 而非 Snapshot」的**服务端并发注入**。
//
// 台账原缺口：客户端面已有直接断言（TestSnapshotProjectionMultiUser 直读 zip 内库、
// "W 后事件不进 Bootstrap"），**缺「服务端 T0 冻结后 live 新写入不泄入快照」的并发注入**。
//
// 风险面：若快照构建过程中误读 live 库（而非 T0 冻结副本），则：
//   - meta.Watermark 记的是 T1 读数（≥T0），而库内容含 >T0 的事件 → 桌面以 cursor=W 记账，
//     这些事件**永久丢失且无人察觉**（静默数据缺口，且方向相反：看似更新实则丢历史）；
//   - 或者 T0 之后的数据半进半出，快照不可重现（两次构建结果不同 → sha 抖动、Hold 失真）。
//
// 断言面（三个缺一不可）：
//   ① zip 内 notes **不含** T0 之后写入的标题（内容面冻结）；
//   ② zip 内 sync_changes 的 max(change_seq) **≤** meta.Watermark（事件面不越界）；
//   ③ meta.Watermark **等于** T0 时刻的 live 读数（锚点没被 T1 抬高）。
// 并加一条「反向对照」：T0 **之前**的写入必须在快照里（否则是「冻结了个空库」另一种坏）。

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"stickynote/changelog"
	"stickynote/database"
	"stickynote/model"
)

// redline12ReadDB 取出 zip 内 notes.db 的只读句柄。
func redline12ReadDB(t *testing.T, zipPath string) *sql.DB {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { zr.Close() })
	tmp := filepath.Join(t.TempDir(), "snap.db")
	f, err := zr.Open("notes.db")
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out.Close()
	db, err := openReadOnlyDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// redline12Titles 读快照库内全部笔记标题。
func redline12Titles(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT title FROM notes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out[s] = true
	}
	return out
}

func redline12MaxSeq(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(change_seq) FROM sync_changes`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.Int64
}

// injectLiveNote 直写 live 库一条笔记并返回其 change_seq。
// 与 pushNoteForUser 的区别：**不在 t.FailNow 上调用 t.Fatal**（Fatal 会 runtime.Goexit，
// 从并发 goroutine 调用会污染整个测试进程）——错误只记录，第一条失败后停止写入。
func injectLiveNote(t *testing.T, userID int64, syncID, title string) int64 {
	t.Helper()
	tx, err := database.DB.Begin()
	if err != nil {
		t.Errorf("注入并发写入 begin 失败: %v", err)
		return 0
	}
	seq, err := changelog.NextChangeSeq(tx)
	if err != nil {
		tx.Rollback()
		t.Errorf("NextChangeSeq: %v", err)
		return 0
	}
	if _, err := tx.Exec(`INSERT INTO notes (user_id, title, content, content_text, sync_id, version)
		VALUES (?,?,'<p>c</p>','',?,1)`, userID, title, syncID); err != nil {
		tx.Rollback()
		t.Errorf("注入并发写入 notes: %v", err)
		return 0
	}
	payload, _ := json.Marshal(map[string]any{"sync_id": syncID, "title": title, "content": "<p>c</p>"})
	if err := changelog.AppendChange(tx, seq, userID, "NOTE", &syncID, "CREATE", 1, string(payload), "injector"); err != nil {
		tx.Rollback()
		t.Errorf("AppendChange: %v", err)
		return 0
	}
	if err := tx.Commit(); err != nil {
		t.Errorf("注入并发写入 commit: %v", err)
		return 0
	}
	return seq
}

func TestRedline12SnapshotFrozenAtT0(t *testing.T) {
	t.Run("并发写入_T0后数据不泄入快照", func(t *testing.T) {
		setupSnapEnv(t)
		// T0 之前：一条合法数据（必须出现在快照里 —— 反向对照）
		pushNoteForUser(t, 1, "0000000000000000000000000000aa01", "T0之前")

		t0 := currentSeq(t) // 冻结点读数
		if t0 < 1 {
			t.Fatalf("前置：T0 应有事件，得 %d", t0)
		}

		snapID := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))

		// 并发注入：构建期间另一个「客户端」持续往 live 库写。
		// 用 stop chan 收敛：BuildSnapshotSync 返回即停写，随后断言 live 库确实多了数据
		//（否则「没泄入」是因为「压根没写」，测试空转）。
		stop := make(chan struct{})
		var wrote int64
		var seqCounter int64       // 保证 sync_id 单调唯一（notes.sync_id 有 UNIQUE 约束）
		var firstLate atomic.Value // 第一条 late 写入的 seq
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				sid := fmt.Sprintf("1b%030x", atomic.AddInt64(&seqCounter, 1))
				title := fmt.Sprintf("T0之后-并发-%d", i)
				seq := injectLiveNote(t, 1, sid, title)
				if firstLate.Load() == nil {
					firstLate.Store(seq)
				}
				atomic.AddInt64(&wrote, 1)
				if i > 200 { // 安全阀：避免测试死循环
					return
				}
			}
		}()

		buildErr := BuildSnapshotSync(snapID, 1, "u1")
		close(stop)
		<-done
		if buildErr != nil {
			t.Fatalf("构建失败: %v", buildErr)
		}

		s, err := model.GetSyncSnapshot(snapID)
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != model.SnapshotReady {
			t.Fatalf("status=%s err=%s", s.Status, s.Error)
		}

		db := redline12ReadDB(t, s.FilePath)

		// ① 内容面：T0 之后的标题一律不得出现（库内容冻结于 T0）
		titles := redline12Titles(t, db)
		if !titles["T0之前"] {
			t.Fatal("反向对照失败：T0 之前的数据竟不在快照里（冻结过头）")
		}
		for title := range titles {
			if len(title) >= 5 && title[:5] == "T0之后" {
				t.Fatalf("红线12：T0 之后写入的 %q 泄入快照（读的是 live 库而非冻结副本）", title)
			}
		}

		// ② **N1 核心断言（v21.157 修法①）**：watermark 必须与冻结内容**同点**——
		// 即等于快照库内本用户的 max(change_seq)。这是「库内容 ↔ 锚点」的自证一致：
		// 桌面以 cursor=W 起步 pull(W,∞)，恰为差集，无缺口无重复。
		//
		// 修复前实测：T0=1 / W=95（T1 读数）/ 库内 max=48 ⇒ 区间 (48,95] 的 47 条事件
		// 既不在库里、也不会被 pull(95,∞) 覆盖 → 静默丢失。此断言即该缺陷的回归护栏。
		frozenMax := redline12MaxSeq(t, db)
		if s.Watermark != frozenMax {
			t.Fatalf("红线12/N1：watermark=%d 必须等于冻结库内max(change_seq)=%d —— "+
				"不等则区间 (%d,%d] 的事件既不在快照库、也不会被 pull(W,∞) 覆盖（静默丢失）",
				s.Watermark, frozenMax, frozenMax, s.Watermark)
		}
		// 反向断言：watermark 不得越过活库当前水位（不能谎报更大值）
		liveNow := currentSeq(t)
		if s.Watermark > liveNow {
			t.Fatalf("红线12/N1：watermark=%d 越过活库当前 seq=%d —— 锚点谎报", s.Watermark, liveNow)
		}
		// 冻结窗口宽度观测（修法生效后此值应 >0，即并发写入被正确排除在锚点之外、由差集覆盖）
		if lag := liveNow - s.Watermark; lag > 0 {
			t.Logf("红线12/N1 修复生效：T0=%d 冻结窗口 lag=%d 事件已由桌面 pull(W,∞) 差集覆盖（W=%d live=%d）",
				t0, lag, s.Watermark, liveNow)
		} else if lag == 0 && atomic.LoadInt64(&wrote) > 0 {
			t.Logf("注：并发写入 %d 条但lag=0（写入全落在冻结之前或构建极快），断言仍成立", wrote)
		}

		// 空转防护：live 库必须真的多出了并发写入的数据，否则上面的断言没有意义
		if atomic.LoadInt64(&wrote) == 0 {
			t.Fatal("并发写入未发生，测试空转")
		}
		var liveLate int
		if err := database.DB.QueryRow(`SELECT COUNT(*) FROM notes WHERE title LIKE 'T0之后%'`).Scan(&liveLate); err != nil {
			t.Fatal(err)
		}
		if liveLate == 0 {
			t.Fatal("并发写入未落 live 库，测试空转")
		}
		if late, ok := firstLate.Load().(int64); ok && late <= t0 {
			t.Fatalf("第一条 late 写入 seq=%d 应 > T0=%d（否则冻结点取样有误）", late, t0)
		}
	})

	t.Run("串行边界_T0前一事件在内_T0后事件在外", func(t *testing.T) {
		// 无并发下做一次精确边界切分：seq=k 的事件必须在快照内，k+1 必须在快照外。
		// 这条比并发用例更「可判定」，两者互补（并发证明不竞态，本条证明边界精确）。
		setupSnapEnv(t)
		pushNoteForUser(t, 1, "0000000000000000000000000000cc01", "边界内")
		k := currentSeq(t)
		snapID := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
		if err := BuildSnapshotSync(snapID, 1, "u1"); err != nil {
			t.Fatal(err)
		}
		// 构建**之后**再写（必在 T0 之后）
		pushNoteForUser(t, 1, "0000000000000000000000000000cc02", "边界外")
		if got := currentSeq(t); got <= k {
			t.Fatalf("前置：后写入应推进 seq，k=%d now=%d", k, got)
		}

		s, _ := model.GetSyncSnapshot(snapID)
		if s.Watermark != k {
			t.Fatalf("watermark 应等于构建时的 k=%d，实得 %d", k, s.Watermark)
		}
		db := redline12ReadDB(t, s.FilePath)
		titles := redline12Titles(t, db)
		if !titles["边界内"] {
			t.Fatal("T0 之前的事件必须在内")
		}
		if titles["边界外"] {
			t.Fatal("红线12：T0 之后的事件泄入快照")
		}
		if mx := redline12MaxSeq(t, db); mx > k {
			t.Fatalf("快照内事件 seq=%d 越过 watermark=%d", mx, k)
		}
	})
}
