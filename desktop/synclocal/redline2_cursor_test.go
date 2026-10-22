package synclocal

// redline2_cursor_test.go —— 红线 2「Cursor 先提交于数据」的**故障注入直测**。
//
// 台账原缺口：§34「应用与 cursor 同事务」只有正例与冲突路径断言（TestPullDirtyGuardADR001），
// **缺「应用失败中途 cursor 不得推进」的负向注入**。
//
// 事实核（读码，2026-10-08）：
//   - NOTE/FOLDER 应用失败走 `applyChange` → `applyNoteChange` 返回 error → `defer tx.Rollback()`
//     （worker.go:318-336），`setCursorTx` 在其后（worker.go:330）故一并回滚；
//   - FOLDER_ORDER 的坏载荷是**另一条路**：`json.Unmarshal` 失败即 `setCursor` 照推
//     （worker.go:209-211，注释「服务端权威产」），这是**刻意设计**——集合 op 无法部分应用，
//     停在原地会永久卡住同步链。
//   故本用例把**两条语义相反的路径都钉住**，避免后人"顺手统一"掉其中一条：
//     A 组 NOTE 坏载荷 → 应用失败 → RunOnce 返回 error → **cursor 零推进**；
//     B 组 FOLDER_ORDER 坏载荷 → 跳过 → RunOnce 成功 → **cursor 照推**（否则卡死链）。
//
// 注入方式：不伪造 client（Client 是具体类型不可替换），改为**在真服务端 changelog 里
// 直接插一条坏 payload 事件**（payload_json 非 JSON / sync_id 缺失），让 pull 真实返回它。

import (
	"database/sql"
	"testing"

	"stickynote/changelog"
	"stickynote/database"
)

// injectBadChange 往服务端 changelog 直插一条指定 payload 的事件（绕过写路径的校验，
// 模拟「服务端权威产出但内容不合预期」——这正是坏载荷的真实来源）。
//
// ⚠️ 为什么 payload 必须是**合法 JSON 但类型不符**（如 JSON 字符串 "bad-payload"），
// 而不是截断的 JSON 文本：pull 响应把 payload 作为 json.RawMessage 内嵌（server/sync/pull.go:73），
// 若 RawMessage 本身非法，**gin 服务端序列化就会失败**，事件根本到不了客户端 —— 那样测的是
// 服务端 500，不是桌面侧的坏载荷语义。类型不符的合法 JSON 能穿过序列化，
// 在客户端 `json.Unmarshal(ch.Payload, &payloadIn)` 处失败（worker.go:360 / worker.go:209），
// 才是这两条分支的真实触发条件。
func injectBadChange(t *testing.T, userID int64, entityType, entitySyncID, operation, payload, deviceID string) int64 {
	t.Helper()
	tx, err := database.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	seq, err := changelog.NextChangeSeq(tx)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO sync_changes
		(change_seq, user_id, entity_type, entity_sync_id, operation, server_version, payload_kind, payload_json, device_id)
		VALUES (?,?,?,?,?,1,'json',?,?)`,
		seq, userID, entityType, entitySyncID, operation, payload, deviceID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return seq
}

// cursorOf 读当前 cursor（未建立时返回 -1，区别于合法的 0）。
func cursorOf(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	cur, err := getState(db, "cursor")
	if err != nil {
		t.Fatalf("读 cursor: %v", err)
	}
	if cur == "" {
		return -1
	}
	var v int64
	if _, err := fmtSscan(cur, &v); err != nil {
		t.Fatalf("cursor 非数字 %q: %v", cur, err)
	}
	return v
}

// TestRedline2CursorNotAdvancedOnApplyFailure —— 红线 2 主干用例。
func TestRedline2CursorNotAdvancedOnApplyFailure(t *testing.T) {
	t.Run("A_坏载荷NOTE_应用失败且cursor零推进", func(t *testing.T) {
		ts := newServer(t)
		db := newStore(t)
		localCreate(t, db, "s-r2a", "正常笔记")
		mustSync(t, db, ts, "devA")
		// 建立 cursor 基线：devZ 产一条合法事件，本机拉取后 cursor 落到该 seq
		seed := injectBadChange(t, 1, "NOTE", "s-r2a", "UPDATE",
			`{"sync_id":"s-r2a","title":"正常笔记","content":"<p>x</p>"}`, "devZ")
		if s := mustSync(t, db, ts, "devA"); s.Applied != 1 {
			t.Fatalf("前置：基线事件应被应用，%+v", s)
		}

		base := cursorOf(t, db)
		if base != seed {
			t.Fatalf("前置：cursor 应等于基线事件 seq=%d，得 %d", seed, base)
		}
		if base < 1 {
			t.Fatalf("前置：应有基线 cursor，得 %d", base)
		}

		// payload 是合法 JSON 但不是对象 → applyNoteChange 的 Unmarshal 必失败（worker.go:360）
		bad := injectBadChange(t, 1, "NOTE", "s-r2a", "UPDATE", `"bad-payload"`, "devZ")
		if bad <= base {
			t.Fatalf("注入事件 seq=%d 应大于基线 cursor=%d", bad, base)
		}

		w := NewWorker(db, NewClient(ts.URL, "tok", "devA"))
		_, err := w.RunOnce(nilCtx())
		if err == nil {
			t.Fatal("红线2：坏 payload 应用失败必须上抛，不得静默吞掉")
		}
		if !containsSubstring(err.Error(), "bad change payload") {
			t.Fatalf("错误应来自坏载荷分支，实得 %v", err)
		}

		// 决定性断言：cursor 一位都没动（与「应用同事务」等价于「同生共死」）
		if after := cursorOf(t, db); after != base {
			t.Fatalf("红线2：应用失败时 cursor 不得推进，before=%d after=%d（bad seq=%d）", base, after, bad)
		}
		// 且该坏事件未被半应用（本地内容未被空载荷覆盖）
		if title := localTitle(t, db, "s-r2a"); title != "正常笔记" {
			t.Fatalf("坏载荷不得改写本地实体，title=%q", title)
		}
		// 幂等复跑：同一坏事件仍在窗口内 → 仍失败且 cursor 依旧不动（非「推过一次就放过」）
		if _, err2 := NewWorker(db, NewClient(ts.URL, "tok", "devA")).RunOnce(nilCtx()); err2 == nil {
			t.Fatal("第二次拉取同一坏事件仍应失败")
		}
		if again := cursorOf(t, db); again != base {
			t.Fatalf("复跑后 cursor 仍不得推进，before=%d after=%d", base, again)
		}
	})

	t.Run("B_坏载荷FOLDER_ORDER_跳过但cursor照推", func(t *testing.T) {
		ts := newServer(t)
		db := newStore(t)
		mustSync(t, db, ts, "devA")
		seed := injectBadChange(t, 1, "NOTE", "s-r2b", "UPDATE",
			`{"sync_id":"s-r2b","title":"基线","content":"<p>x</p>"}`, "devZ")
		mustSync(t, db, ts, "devA")
		base := cursorOf(t, db)
		if base != seed {
			t.Fatalf("前置：cursor 应为 %d，得 %d", seed, base)
		}

		// FOLDER_ORDER 坏载荷 → setCursor 照推（worker.go:209-211，刻意设计）
		bad := injectBadChange(t, 1, "FOLDER_ORDER", "", "UPDATE", `"bad-payload"`, "devZ")
		w := NewWorker(db, NewClient(ts.URL, "tok", "devA"))
		s, err := w.RunOnce(nilCtx())
		if err != nil {
			t.Fatalf("FOLDER_ORDER 坏载荷应跳过而非中止整轮，实得 %v", err)
		}
		if s.Applied != 0 {
			t.Fatalf("坏载荷不得计入 applied，得 %d", s.Applied)
		}
		after := cursorOf(t, db)
		if after != bad {
			t.Fatalf("FOLDER_ORDER 坏载荷必须推进 cursor 到 %d（否则同步链永久卡死），实得 %d", bad, after)
		}
		if after == base {
			t.Fatal("cursor 未推进")
		}
		// 且下一轮不再重复拉取该事件（真·跳过，非每轮重试）
		s2, err := NewWorker(db, NewClient(ts.URL, "tok", "devA")).RunOnce(nilCtx())
		if err != nil {
			t.Fatal(err)
		}
		if s2.Applied != 0 || s2.Conflicts != 0 {
			t.Fatalf("复跑不应再处理该事件：%+v", s2)
		}
	})

	t.Run("C_合法载荷_应用与cursor同事务同进", func(t *testing.T) {
		ts := newServer(t)
		db := newStore(t)
		localCreate(t, db, "s-r2c", "本机")
		mustSync(t, db, ts, "devA")
		base := cursorOf(t, db)

		// 远端（devZ）对 s-r2c 的合法 UPDATE
		good := injectBadChange(t, 1, "NOTE", "s-r2c", "UPDATE",
			`{"sync_id":"s-r2c","title":"远端已改","content":"<p>x</p>"}`, "devZ")
		s, err := NewWorker(db, NewClient(ts.URL, "tok", "devA")).RunOnce(nilCtx())
		if err != nil {
			t.Fatal(err)
		}
		if s.Applied != 1 {
			t.Fatalf("合法事件应被应用，得 %+v", s)
		}
		if title := localTitle(t, db, "s-r2c"); title != "远端已改" {
			t.Fatalf("合法载荷未落地，title=%q", title)
		}
		after := cursorOf(t, db)
		if after != good {
			t.Fatalf("应用成功后 cursor 应等于事件 seq=%d，实得 %d", good, after)
		}
		if after == base {
			t.Fatal("cursor 未推进")
		}
	})
}

// containsSubstring / fmtSscan：避免为本文件引入 strings/strconv 的额外 import 噪声。
func containsSubstring(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func fmtSscan(s string, v *int64) (int, error) {
	var n int64
	var neg bool
	for i, c := range []byte(s) {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, errNotNumeric
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	*v = n
	return 1, nil
}

var errNotNumeric = &nonNumericErr{}

type nonNumericErr struct{}

func (*nonNumericErr) Error() string { return "not numeric" }
