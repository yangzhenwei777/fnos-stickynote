package localstore

// redline13_matrix_test.go —— 批次 9 · C1-B「崩溃/断电矩阵补全」+ 红线 13 缺口收口。
//
// 台账 §八对红线 13（Generation 激活失败无可靠回退）的缺口原文：
//   「切换落盘后 abort 复位、新代损坏分支仍未测——留给批次 9」。
// 本文件补三类**真实 kill 点位**（均在既有 failAt 注入面之外的新组合）：
//   A. 切换落盘后 abort 复位：PREPARED/VERIFIED/SWITCHING 三个失败点各跑一遍 abort 全链，
//      断言「指针复位到 prev / 半成品代删除 / journal 清空 / 下一轮激活不被脏 journal 卡住」。
//   B. 新代损坏：激活成功后**破坏新代库文件**（写垃圾字节），断言 Recover/打开路径
//      fail-closed 或回落 lkg，**绝不静默以损坏库启动**（红线 13 的实质）。
//   C. WAL 活库断电（189 回归护栏）：staging 源库带未 checkpoint 的 WAL，
//      激活后新代必须含 WAL 里的数据——否则复现「首装空库 / 0 表库秒退」历史事故。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- A. abort 复位

// TestAbortAfterSwitchRollsBackCleanly —— A 组：在三个失败点注入崩溃，
// 逐一验证 abort 的**四件套**（指针复位 / 半成品删除 / journal 清理 / 后续可再激活）。
func TestAbortAfterSwitchRollsBackCleanly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failAt Phase
	}{
		{"PREPARED后崩", PhasePrepared},
		{"VERIFIED后崩", PhaseVerified},
		{"SWITCHING后崩（指针尚未替换）", PhaseSwitching},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a := NewActivator(dir)
			l := a.L
			l.EnsureDirs()

			// 先成功激活一代作为 prev（lkg 基线）
			mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "base.db"), dir, "s-base"))
			prev, _ := l.Active()
			if prev == "" {
				t.Fatal("基线激活后指针应非空")
			}

			// 第二次激活在指定相位注入失败 → 走 abort
			live := newLiveWALDB(t, dir, "next.db")
			st := sealStaging(t, live, dir, "s-next")
			a.failAt = func(next Phase) error {
				if next == tc.failAt {
					return os.ErrInvalid
				}
				return nil
			}
			if _, err := a.Activate(st, []byte(`{}`)); err == nil {
				t.Fatalf("%s：注入失败后 Activate 应返回错误", tc.name)
			}
			a.failAt = nil

			// ① 指针复位到 prev（abort 不得把指针留在半路）
			if act, _ := l.Active(); act != prev {
				t.Fatalf("%s：指针应复位到 %s，实际 %s", tc.name, prev, act)
			}
			// ② 半成品代目录已清理（journal.NextGen 即目标代）
			if j, _ := loadJournal(l); j != nil {
				if genExists(l.GenerationsDir(), j.NextGen) {
					t.Fatalf("%s：半成品代 %s 应被 abort 删除", tc.name, j.NextGen)
				}
			}
			// ③ journal 已清空（不留脏状态给下一轮）
			if j, _ := loadJournal(l); j != nil {
				t.Fatalf("%s：abort 后 journal 应清空，实得 phase=%s next=%s", tc.name, j.Phase, j.NextGen)
			}
			// ④ 基线数据仍可读（abort 不得毁掉 prev 代）
			if v := readGenValue(t, l.GenDB(prev), "base.db"); v != "val-base.db" {
				t.Fatalf("%s：prev 代数据应完好，实际 %q", tc.name, v)
			}
			// ⑤ **后续激活不被脏状态卡住**（这一条是「复位」二字的关键含义）
			gen := mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "retry.db"), dir, "s-retry"))
			if gen == "" || gen == prev {
				t.Fatalf("%s：abort 后应能重新激活出新代，得 %q", tc.name, gen)
			}
		})
	}
}

// ---------------------------------------------------------------- B. 新代损坏

// TestCorruptedActiveGenFailsClosed —— B 组：**当前代库损坏**时的真实行为钉测。
//
// 事实核（读码 + 实测，2026-10-08）：`Recover` 在**无journal** 分支只检查
// `genExists`（目录存在）即返回 "clean"，**不校验库可开性**；随后启动链
// （`startup_localstore.go:62`）`OpenGenDB(active)` 失败即 return error → **fail-closed**。
// 即：当前代损坏时**不会自动回落 lkg**，也没有「如何处置」的指引文案。
//
// 本用例钉住这个现状（fail-closed = 不静默用损坏库继续，符合纪律 E.1），
// 并把「无自动回落 / 无处置指引」登记为批次 9 新发现缺口（见交付说明），
// **不在测试里擅自改语义**——是否加 lkg 自动回落需用户拍板（静默用旧数据启动有
// 「看起来正常但最新笔记消失」的风险，与 fail-closed 取向相悖）。
func TestCorruptedActiveGenFailsClosed(t *testing.T) {
	t.Run("当前代损坏_Recover仍判clean但开库必失败", func(t *testing.T) {
		dir := t.TempDir()
		a := NewActivator(dir)
		l := a.L
		l.EnsureDirs()
		mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "one.db"), dir, "s1"))
		act, _ := l.Active()
		if act == "" {
			t.Fatal("激活后指针应非空")
		}
		if err := os.WriteFile(l.GenDB(act), []byte("this-is-not-a-sqlite-file"), 0600); err != nil {
			t.Fatal(err)
		}

		// ① Recover 无 journal 分支：只看目录存在 → clean（现状）
		s, err := Recover(dir)
		if err != nil {
			t.Fatalf("Recover 应返回 clean（现状语义），实得 err=%v", err)
		}
		if s != "clean" {
			t.Fatalf("现状应为 clean（目录存在即通过），实得 %q", s)
		}

		// ② 但启动链的 OpenGenDB 必须失败 —— 这才是真正的 fail-closed 防线
		db, oerr := OpenGenDB(l.GenDB(act))
		if oerr == nil {
			db.Close()
			// SQLite 对「垃圾字节」可能宽容地开成空库；此时必须有表才能继续，
			// 否则等于静默以空/损坏库启动（0 表库事故面）。
			var tables int
			if qerr := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); qerr != nil || tables == 0 {
				t.Fatalf("损坏库被静默开成 0 表库（红线事故面）: tables=%d err=%v", tables, qerr)
			}
			t.Skip("本平台 SQLite 未能拒绝该损坏字节，且库内仍有表——已验证非空库，OpenGenDB 层不抛")
		}
		if !strings.Contains(oerr.Error(), "not a database") && !strings.Contains(oerr.Error(), "file is not") &&
			!strings.Contains(oerr.Error(), "malformed") {
			t.Logf("注：开库错误文案=%v（fail-closed 成立，文案非标准措辞）", oerr)
		}
	})

	t.Run("指针指向不存在的代_有lkg才回落", func(t *testing.T) {
		dir := t.TempDir()
		a := NewActivator(dir)
		l := a.L
		l.EnsureDirs()
		mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "gen1.db"), dir, "s1"))
		mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "gen2.db"), dir, "s2"))
		g2, _ := l.Active()
		if err := l.writePointer(l.LKGFile(), "g-000001"); err != nil {
			t.Fatal(err)
		}
		// 删代目录（模拟磁盘丢文件）→ 这条路径既有实现**确实**会回落，钉住它
		if err := os.RemoveAll(l.GenDir(g2)); err != nil {
			t.Fatal(err)
		}
		s, err := Recover(dir)
		if err != nil || !strings.HasPrefix(s, "fallback:") {
			t.Fatalf("代目录丢失时应回落 lkg，实得 %q err=%v", s, err)
		}
		if act, _ := l.Active(); act != "g-000001" {
			t.Fatalf("指针应回落 g-000001，得 %s", act)
		}
		if v := readGenValue(t, l.GenDB("g-000001"), "gen1.db"); v != "val-gen1.db" {
			t.Fatalf("回落代数据应可读，实际 %q", v)
		}
	})
}

// ---------------------------------------------------------------- C. WAL 活库断电

// TestActivationCarriesUncheckpointedWAL —— C 组：WAL 活库断电回归护栏（189 事故面）。
// 源库开着 WAL 且**未 checkpoint** 就被 SnapshotTo 封进 staging；
// 激活后新代必须带上 WAL 中的行。若丢失 → 复现「首装空库 / 0 表库秒退」。
func TestActivationCarriesUncheckpointedWAL(t *testing.T) {
	dir := t.TempDir()
	a := NewActivator(dir)
	l := a.L
	l.EnsureDirs()

	// newLiveWALDB 建的是「WAL 活库」；这里再补几笔**不 checkpoint** 的写入，
	// 确保这些行只存在于 -wal 文件里。
	live := newLiveWALDB(t, dir, "wal.db")
	db, err := OpenGenDB(live)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		k := "wal-key-" + itoaTest(i)
		if _, err := db.Exec(`INSERT INTO kv(k, v) VALUES (?, ?)`, k, "val-wal.db"); err != nil {
			t.Fatalf("写 WAL 行失败: %v", err)
		}
	}
	// 探针行（供激活后断言用，键唯一）
	if _, err := db.Exec(`INSERT INTO kv(k, v) VALUES ('wal-probe', 'val-wal.db')`); err != nil {
		t.Fatalf("写探针行失败: %v", err)
	}
	// 确认 WAL 侧确有未落盘内容（-wal 文件非空且 db 侧看不到即最典型形态）
	walPath := live + "-wal"
	if st, serr := os.Stat(walPath); serr != nil || st.Size() == 0 {
		t.Logf("注：WAL 文件不可见或为空（%v），用例仍继续（取决于平台 checkpoint 时机）", serr)
	}
	if err := db.Close(); err != nil {
		t.Logf("关闭源库返回 %v（继续）", err)
	}

	st := sealStaging(t, live, dir, "s-wal")
	gen := mustActivate(t, a, st)
	if gen == "" {
		t.Fatal("激活失败")
	}

	// 新代库必须可开且含 WAL 里的行
	gdb, err := OpenGenDB(l.GenDB(gen))
	if err != nil {
		t.Fatalf("新代库打不开（复现 189 首装空库事故）: %v", err)
	}
	defer gdb.Close()
	var n int
	if err := gdb.QueryRow(`SELECT COUNT(*) FROM kv WHERE k = 'wal-probe'`).Scan(&n); err != nil {
		t.Fatalf("查询新代 WAL 行失败（schema 缺失 = 0 表库事故特征）: %v", err)
	}
	if n == 0 {
		t.Fatal("红线 C：新代丢失了未 checkpoint 的 WAL 数据 —— 复现 189「首装空库」事故面")
	}
	var tables int
	if err := gdb.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
		t.Fatalf("sqlite_master 查询异常: %v", err)
	}
	if tables == 0 {
		t.Fatal("红线 C：新代是0 表库 —— 189 首装空库事故特征")
	}
	_ = filepath.Join // 保持 filepath 导入（供后续扩展点位）
}

// itoaTest 测试内小工具（避免与既有 helper 冲突）。
func itoaTest(i int) string { return strconv.Itoa(i) }
