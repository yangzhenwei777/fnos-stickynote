package localstore

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	// 复用服务端已实装纪律原语：VACUUM INTO 一致性快照（staged sealed 生成方）。
	"stickynote/backup"
)

// ---- 工装 ----

// newLiveWALDB 建一个"WAL 活库"（有表有数据），返回 profileDir 与库路径；随后由
// backup.SnapshotTo 出封闭快照作 staging（证明复用服务端同一原语）。
func newLiveWALDB(t *testing.T, dir, name string) string {
	t.Helper()
	db, err := OpenGenDB(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kv VALUES(?, ?)`, name, "val-"+name); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return filepath.Join(dir, name)
}

func sealStaging(t *testing.T, liveDB, profileDir, id string) string {
	t.Helper()
	st := filepath.Join(profileDir, "staging", id+".db")
	if err := os.MkdirAll(filepath.Dir(st), 0700); err != nil {
		t.Fatal(err)
	}
	if err := backup.SnapshotTo(st, liveDB); err != nil {
		t.Fatalf("SnapshotTo: %v", err)
	}
	return st
}

func mustActivate(t *testing.T, a *Activator, staging string) string {
	t.Helper()
	gen, err := a.Activate(staging, []byte(`{"generation_id":"pending","schema_version":1}`))
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	// v21.203：Activate 不再自行清 journal（封相由调用方在后置步骤成功后做）。
	// 这里的测试工装扮演「调用方」：后置步骤（对测试而言）已确认完成 → 立即封相。
	if err := a.SealCommitted(gen); err != nil {
		t.Fatalf("SealCommitted: %v", err)
	}
	return gen
}

func writeJournalRaw(t *testing.T, l Layout, j Journal) {
	t.Helper()
	b, _ := json.MarshalIndent(j, "", "  ")
	if err := os.WriteFile(l.JournalFile(), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func readGenValue(t *testing.T, dbPath, key string) string {
	t.Helper()
	db, err := OpenGenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT v FROM kv WHERE k=?`, key).Scan(&v); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return v
}

// ---- 连接纪律（§93/§94） ----

func TestOpenGenDBPragmaReadback(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenGenDB(filepath.Join(dir, "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var jm string
	var sync, fk, busy int
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
		t.Fatal(err)
	}
	if jm != "wal" {
		t.Fatalf("journal_mode=%q", jm)
	}
	_ = db.QueryRow("PRAGMA synchronous").Scan(&sync)
	_ = db.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	_ = db.QueryRow("PRAGMA busy_timeout").Scan(&busy)
	if sync != 2 || fk != 1 || busy != 5000 {
		t.Fatalf("pragma 组合不符: synchronous=%d foreign_keys=%d busy=%d", sync, fk, busy)
	}
	if err := QuickCheckOK(db); err != nil {
		t.Fatal(err)
	}
}

// ---- 激活 happy path ----

func TestActivateTwoGenerations(t *testing.T) {
	dir := t.TempDir()
	a := NewActivator(dir)
	live1 := newLiveWALDB(t, dir, "src1.db")
	g1 := mustActivate(t, a, sealStaging(t, live1, dir, "s1"))
	if g1 != "g-000001" {
		t.Fatalf("gen=%s", g1)
	}
	// §84：切换前 lkg=G1、active=G2。
	live2 := newLiveWALDB(t, dir, "src2.db")
	g2 := mustActivate(t, a, sealStaging(t, live2, dir, "s2"))
	l := a.L
	if act, _ := l.Active(); act != g2 {
		t.Fatalf("active=%s want %s", act, g2)
	}
	if lkg, _ := l.LastKnownGood(); lkg != g1 {
		t.Fatalf("lkg=%s want %s", lkg, g1)
	}
	if !genExists(l.GenerationsDir(), g1) || !genExists(l.GenerationsDir(), g2) {
		t.Fatal("§16 最低保留：active 与 previous 必须同时在盘")
	}
	if j, _ := loadJournal(l); j != nil {
		t.Fatal("成功激活后 journal 应清除")
	}
	if readGenValue(t, l.GenDB(g2), "src2.db") != "val-src2.db" {
		t.Fatal("新代数据不可读")
	}
}

// ---- failAt 注入矩阵（进程内崩溃模拟 + 状态收敛断言） ----
//
// v21.203 语义更新：注入点 PhaseCommitted 已移除（Activate 不再自己写 COMMITTED），
// 改为 PhaseSealed —— 语义是「指针已切、journal 停在 SWITCHING、后置步骤未确认」，
// Recover 必须**回退并删除残缺新代**（首装场景 prev 为空 ⇒ 无退路 ⇒ 保留新代）。
func TestActivateFailAtEveryPhase(t *testing.T) {
	for _, phase := range []Phase{PhasePrepared, PhaseVerified, PhaseSwitching, PhaseSealed} {
		t.Run(string(phase), func(t *testing.T) {
			dir := t.TempDir()
			a := NewActivator(dir)
			live := newLiveWALDB(t, dir, "src.db")
			st := sealStaging(t, live, dir, "s")
			inject := phase
			a.failAt = func(next Phase) error {
				if next == inject {
					return os.ErrInvalid
				}
				return nil
			}
			_, err := a.Activate(st, []byte(`{}`))
			if err == nil {
				t.Fatal("注入点应失败")
			}
			l := a.L
			if phase == PhaseSealed {
				// 指针已切、journal 停 SWITCHING：首装无 prev ⇒ 保留新代并清 journal。
				if act, _ := l.Active(); act != "g-000001" {
					t.Fatalf("SEALED 崩溃后 active=%s", act)
				}
				summary, rerr := Recover(dir)
				if rerr != nil || summary != "reverted:g-000001" && summary != "aborted:g-000001" {
					t.Fatalf("Recover=%q err=%v", summary, rerr)
				}
				if j, _ := loadJournal(l); j != nil {
					t.Fatal("Recover 后 journal 应清除")
				}
				return
			}
			// PREPARED/VERIFIED/SWITCHING 前注入：零残留（无 journal、无代目录、无指针）。
			if act, _ := l.Active(); act != "" {
				t.Fatalf("回滚后 active=%s want 空", act)
			}
			if j, _ := loadJournal(l); j != nil {
				t.Fatal("回滚后 journal 应清除")
			}
			if genExists(l.GenerationsDir(), "g-000001") {
				t.Fatal("半成品 Generation 目录应被回滚删除")
			}
		})
	}
}

// ---- Recover 对账矩阵（手工伪造 journal×指针 任意组合 = 真实 kill 点位） ----

func TestRecoverJournalPointerMatrix(t *testing.T) {
	t.Run("PREPARED未切换_清理", func(t *testing.T) {
		dir := t.TempDir()
		l := Layout{ProfileDir: dir}
		l.EnsureDirs()
		writeJournalRaw(t, l, Journal{ActivationID: "x1", NextGen: "g-000001", BaseSHA256: "ab", Phase: PhasePrepared})
		os.MkdirAll(l.GenDir("g-000001"), 0700) // 半成品
		s, err := Recover(dir)
		if err != nil || s != "aborted:g-000001" {
			t.Fatalf("Recover=%q err=%v", s, err)
		}
		if genExists(l.GenerationsDir(), "g-000001") {
			t.Fatal("未切换的半成品应删除")
		}
	})
	t.Run("SWITCHING已切换_回退非完成化", func(t *testing.T) {
		dir := t.TempDir()
		a := NewActivator(dir)
		l := a.L
		live := newLiveWALDB(t, dir, "src.db")
		st := sealStaging(t, live, dir, "s")
		a.failAt = func(next Phase) error {
			if next == PhaseSealed {
				return os.ErrInvalid
			}
			return nil
		}
		a.Activate(st, []byte(`{}`)) // 注入后必然报错，忽略
		// v21.203：指针已切但后置未确认 ⇒ 必须回退（首装无 prev 时保留新代）。
		s, err := Recover(dir)
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if s != "reverted:g-000001" && s != "aborted:g-000001" {
			t.Fatalf("Recover=%q（首装无 prev，应为 reverted/aborted 语义）", s)
		}
		if j, _ := loadJournal(l); j != nil {
			t.Fatal("Recover 后 journal 应清除")
		}
	})
	t.Run("COMMITTED停journal未清_终清理", func(t *testing.T) {
		dir := t.TempDir()
		a := NewActivator(dir)
		l := a.L
		live := newLiveWALDB(t, dir, "src.db")
		mustActivate(t, a, sealStaging(t, live, dir, "s"))
		writeJournalRaw(t, l, Journal{ActivationID: "z", NextGen: "g-000001", BaseSHA256: "ab", Phase: PhaseCommitted})
		s, err := Recover(dir)
		if err != nil || s != "finalized:g-000001" {
			t.Fatalf("Recover=%q err=%v", s, err)
		}
	})
	t.Run("无journal指针失联_回落lkg", func(t *testing.T) {
		dir := t.TempDir()
		a := NewActivator(dir)
		l := a.L
		mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "src-a.db"), dir, "s1"))
		mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "src-b.db"), dir, "s2"))
		// 破坏当前代（模拟磁盘丢文件），保留 lkg 代。
		os.RemoveAll(l.GenDir("g-000002"))
		s, err := Recover(dir)
		if err != nil || s != "fallback:g-000001" {
			t.Fatalf("Recover=%q err=%v", s, err)
		}
		if act, _ := l.Active(); act != "g-000001" {
			t.Fatalf("指针应回落 lkg，得 %s", act)
		}
	})
	t.Run("无journal无路可回_上抛", func(t *testing.T) {
		dir := t.TempDir()
		l := Layout{ProfileDir: dir}
		l.EnsureDirs()
		atomicWriteFile(l.PointerFile(), []byte("g-999999\n"))
		if _, err := Recover(dir); err == nil || !strings.Contains(err.Error(), "manual bootstrap") {
			t.Fatalf("应上抛人工 bootstrap 错误，得 %v", err)
		}
	})
}

// ---- 空间预检（§52：不足 → 拒绝，零副作用） ----

func TestSpaceGuardRejectsBeforeAnyMutation(t *testing.T) {
	dir := t.TempDir()
	a := NewActivator(dir)
	l := a.L
	l.EnsureDirs()
	huge := filepath.Join(dir, "huge.db")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	// 稀疏巨型文件：只撑大 st.Size() 不占真实空间。注意 NTFS 单文件上限（≤16TB 档），
	// 逐级降档找到一个能设置的尺寸；need=size*2+64MB 远超任何真实卷容量即成立。
	placed := false
	for _, size := range []int64{1 << 45, 1 << 44, 1 << 40} {
		if terr := f.Truncate(size); terr == nil {
			placed = true
			break
		}
	}
	f.Close()
	if !placed {
		t.Skip("文件系统不支持稀疏大文件，空间预检用例跳过")
	}
	_, err = a.Activate(huge, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), ErrInsufficientSpace.Error()) {
		t.Fatalf("应 ErrInsufficientSpace，得 %v", err)
	}
	if j, _ := loadJournal(l); j != nil {
		t.Fatal("预检失败不得留下 journal")
	}
	if act, _ := l.Active(); act != "" {
		t.Fatal("预检失败不得动指针")
	}
}

// ---- sealed hash 纪律（F1） ----

func TestSealSHA256Basic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sealed.db")
	if err := os.WriteFile(p, []byte("sealed snapshot bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	h1, err := SealSHA256(p)
	if err != nil || len(h1) != 64 {
		t.Fatalf("hash=%s err=%v", h1, err)
	}
	if err := os.WriteFile(p, []byte("tampered  snapshot bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	h2, _ := SealSHA256(p)
	if h1 == h2 {
		t.Fatal("篡改后 hash 必须变化")
	}
}

// TestFreeBytesExported 导出面冒烟：合法路径返回正数、非法路径报错不panic（provision 预检依赖）。
func TestFreeBytesExported(t *testing.T) {
	n, err := FreeBytes(t.TempDir())
	if err != nil {
		t.Fatalf("FreeBytes(tempdir): %v", err)
	}
	if n == 0 {
		t.Fatal("FreeBytes must report >0 for a writable temp dir")
	}
	if _, err := FreeBytes(filepath.Join(t.TempDir(), "no-such-dir-xyz")); err == nil {
		t.Fatal("FreeBytes must error on non-existent path")
	}
}
