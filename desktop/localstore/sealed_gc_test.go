package localstore

// sealed_gc_test.go —— v21.203 批次 1 回归：封相（SEALED）崩溃收敛 + 回退清理 + GC。
//
// 背景（审计定罪，v21.200~202 实机事故机制）：
//   Activate 内部把 journal 清在 COMMITTED 就 return，而换代之后调用方还有 5~8 个
//   可失败步骤（锚/游标/outbox 搬运/身份迁移/自检）。进程在这些步骤之间被 kill 时
//   defer 不执行 ⇒ 下次启动 Recover 看到「无 journal + 指针指向**内容残缺**的新代」
//   ⇒ 误判 clean ⇒ 用户撞上「已同步笔记全没了 + 账号与设备显示未登录」。
//
// v21.203 的结构性修法：Activate 停在 SEALED，调用方确认后置步骤成功才 SealCommitted；
// SEALED/SWITCHING 崩溃态一律回退 prev + 删残缺代（首装无 prev 才保留）。
// 本文件把这条链钉成可执行断言。

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestUnsealedCrashRevertsToPrev —— 核心用例：换代后置步骤期间被 kill（journal 停 SEALED）。
//
// 关键区别（也是旧实现最重的一处错）：新代**内容可开、quick_check 也过**（它是服务端
// 快照的完整副本），唯独缺「outbox 搬运 / client_profile 迁移 / 锚设置」这些**只由
// 后置步骤写入**的东西。所以任何「库能不能打开」为判据的检查都会放过它 —— 唯一的
// 判据是 journal 相位本身。旧实现正是在这里做了「完成化」。
func TestUnsealedCrashRevertsToPrev(t *testing.T) {
	dir := t.TempDir()
	a := NewActivator(dir)
	l := a.L
	l.EnsureDirs()

	// 代 1：完整封相（正常换代）
	g1 := mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "gen1.db"), dir, "s1"))
	if g1 != "g-000001" {
		t.Fatalf("g1=%s", g1)
	}

	// 代 2：Activate 成功（指针已切、journal 停 SEALED），随后**进程被 kill**
	// —— 用 failAt 注入 + 不封相来忠实模拟（defer 不执行）。
	g2, err := a.Activate(sealStaging(t, newLiveWALDB(t, dir, "gen2.db"), dir, "s2"), []byte(`{}`))
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if g2 != "g-000002" {
		t.Fatalf("g2=%s", g2)
	}

	// 崩溃现场：指针已指向 g-000002，journal 停 SEALED。
	if act, _ := l.Active(); act != g2 {
		t.Fatalf("崩溃现场 active 应为 %s，实际 %s", g2, act)
	}
	j, _ := loadJournal(l)
	if j == nil || j.Phase != PhaseSealed {
		t.Fatalf("崩溃现场 journal 应停 SEALED，实得 %+v", j)
	}
	// ⭐ 事实核：新代库**本身完全可开**（证明「可开性」不是有效判据）。
	if db, oerr := OpenGenDB(l.GenDB(g2)); oerr != nil {
		t.Fatalf("残缺新代应仍可开（它只是缺后置步骤写入物），实得 %v", oerr)
	} else {
		db.Close()
	}

	// 重启 → Recover。
	summary, rerr := Recover(dir)
	if rerr != nil {
		t.Fatalf("Recover: %v", rerr)
	}
	if summary != "reverted:"+g2 {
		t.Fatalf("Recover=%q，SEALED 崩溃态必须回退而非完成化", summary)
	}
	// ① 指针复位到 prev
	if act, _ := l.Active(); act != g1 {
		t.Fatalf("回退后 active 应为 %s，实际 %s", g1, act)
	}
	// ② 残缺新代被删（不白占盘、不被人工误用）
	if genExists(l.GenerationsDir(), g2) {
		t.Fatal("残缺新代目录必须删除")
	}
	// ③ lkg 与 active 自洽（回退后 lkg 应等于 prev）
	if lkg, _ := l.LastKnownGood(); lkg != g1 {
		t.Fatalf("回退后 lkg=%s，应等于 prev=%s", lkg, g1)
	}
	// ④ journal 清理
	if j, _ := loadJournal(l); j != nil {
		t.Fatal("回退后 journal 应清空")
	}
	// ⑤ prev 代数据完好
	if v := readGenValue(t, l.GenDB(g1), "gen1.db"); v != "val-gen1.db" {
		t.Fatalf("prev 代数据应完好，实际 %q", v)
	}
	// ⑥ 回退后仍能继续正常换代（不被脏状态卡住）。
	// 注意：残缺代 g-000002 已被删除 ⇒ nextGenerationID 取「盘上最大号+1」= 3 代被
	// 复用的新代仍叫 g-000002。这是预期行为（删除残缺代的收益之一就是不再顶高代号）。
	g3 := mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "gen3.db"), dir, "s3"))
	if g3 == "" || g3 == g1 {
		t.Fatalf("回退后应能激活新代，得 %q", g3)
	}
	if readGenValue(t, l.GenDB(g3), "gen3.db") != "val-gen3.db" {
		t.Fatalf("新代数据应可读，实际 %q", readGenValue(t, l.GenDB(g3), "gen3.db"))
	}
}

// TestSealedCommittedKeepsNewGen —— 反向断言：封相后崩溃**不得**回退。
// 若这条挂了，说明修法矫枉过正（每次换代后崩溃都退到上一代 = 同步永远不落盘）。
func TestSealedCommittedKeepsNewGen(t *testing.T) {
	dir := t.TempDir()
	a := NewActivator(dir)
	l := a.L
	l.EnsureDirs()
	g1 := mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "gen1.db"), dir, "s1"))

	g2, err := a.Activate(sealStaging(t, newLiveWALDB(t, dir, "gen2.db"), dir, "s2"), []byte(`{}`))
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := a.SealCommitted(g2); err != nil {
		t.Fatalf("SealCommitted: %v", err)
	}
	// 封相后 journal 已清 ⇒ 崩溃现场就是普通「无 journal」态，Recover 判 clean。
	if j, _ := loadJournal(l); j != nil {
		t.Fatal("封相后 journal 应清空")
	}
	if s, err := Recover(dir); err != nil || s != "clean" {
		t.Fatalf("Recover=%q err=%v，封相后应为 clean", s, err)
	}
	if act, _ := l.Active(); act != g2 {
		t.Fatalf("封相后 active 应保持 %s，实际 %s", g2, act)
	}
	if !genExists(l.GenerationsDir(), g2) {
		t.Fatal("封相后的新代不得被删")
	}
	if lkg, _ := l.LastKnownGood(); lkg != g1 {
		t.Fatalf("lkg 应为 %s，实际 %s", g1, lkg)
	}
}

// TestSealCommittedDiscipline —— 封相 API 的守卫：空代、非在途代、错相位都必须报错；
// 重复封相幂等（不能因已清 journal 就把一次成功换代报成失败）。
func TestSealCommittedDiscipline(t *testing.T) {
	dir := t.TempDir()
	a := NewActivator(dir)
	l := a.L
	l.EnsureDirs()
	mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "gen1.db"), dir, "s1"))

	if err := a.SealCommitted(""); err == nil {
		t.Fatal("空代必须报错")
	}
	g2, err := a.Activate(sealStaging(t, newLiveWALDB(t, dir, "gen2.db"), dir, "s2"), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SealCommitted("g-999999"); err == nil {
		t.Fatal("非在途代必须报错（否则会封掉别人的代）")
	}
	if err := a.SealCommitted(g2); err != nil {
		t.Fatalf("正常封相失败: %v", err)
	}
	if err := a.SealCommitted(g2); err != nil {
		t.Fatalf("重复封相必须幂等，实得 %v", err)
	}
}

// TestGCGenerationsProtectsActiveLKGAndRecent —— GC 保护集：active + lkg 必留，
// keep 个最近代必留，其余删；非本包命名目录不碰。
func TestGCGenerationsProtectsActiveLKGAndRecent(t *testing.T) {
	dir := t.TempDir()
	a := NewActivator(dir)
	l := a.L
	l.EnsureDirs()
	// 6 代全封相
	var gens []string
	for i := 1; i <= 6; i++ {
		db := newLiveWALDB(t, dir, "src"+string(rune('0'+i))+".db")
		gens = append(gens, mustActivate(t, a, sealStaging(t, db, dir, "s"+string(rune('0'+i)))))
	}
	if act, _ := l.Active(); act != gens[5] {
		t.Fatalf("active 应为 %s", gens[5])
	}
	// 人为把 lkg 钉到第 2 代（模拟长跑后 lkg 落后）
	if err := l.writePointer(l.LKGFile(), gens[1]); err != nil {
		t.Fatal(err)
	}
	// 塞一个非本包命名目录，GC 不得碰
	manual := filepath.Join(l.GenerationsDir(), "my-backup")
	if err := os.MkdirAll(manual, 0700); err != nil {
		t.Fatal(err)
	}

	removed, err := l.GCGenerations(2)
	if err != nil {
		t.Fatalf("GCGenerations: %v", err)
	}
	// keep=2 的语义：保护集 = {active, lkg} ∪ 「最近 2 个**不在保护集里**的代」。
	// 这里 active=g-000006、lkg=g-000002，最近两个非保护代 = g-000005、g-000004。
	// 故 g-000001 与 g-000003 可删；g-000002(lkg)/g-000004/g-000005/g-000006 必留。
	for _, g := range []string{gens[0], gens[2]} {
		if !containsStr(removed, g) {
			t.Fatalf("%s 应被 GC 删除，removed=%v", g, removed)
		}
		if genExists(l.GenerationsDir(), g) {
			t.Fatalf("%s 仍在盘", g)
		}
	}
	for _, g := range []string{gens[1], gens[3], gens[4], gens[5]} {
		if !genExists(l.GenerationsDir(), g) {
			t.Fatalf("%s 必须保留（active/lkg/最近代）", g)
		}
	}
	if _, serr := os.Stat(manual); serr != nil {
		t.Fatal("GC 不得删除非 g-NNNNNN 命名的用户目录")
	}
}

// TestGCKeepsInFlightJournalGens —— 在途 journal 的 NextGen/PrevGen 必须进保护集：
// GC 抢先删 = 把 Recover 的回退目标删掉，把「可回退」变成「丢数据」。
func TestGCKeepsInFlightJournalGens(t *testing.T) {
	dir := t.TempDir()
	a := NewActivator(dir)
	l := a.L
	l.EnsureDirs()
	g1 := mustActivate(t, a, sealStaging(t, newLiveWALDB(t, dir, "gen1.db"), dir, "s1"))
	g2, err := a.Activate(sealStaging(t, newLiveWALDB(t, dir, "gen2.db"), dir, "s2"), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	// journal 停 SEALED（PrevGen=g1 / NextGen=g2）。把 keep 设 0 逼 GC 走到最狠。
	if _, err := l.GCGenerations(0); err != nil {
		t.Fatal(err)
	}
	if !genExists(l.GenerationsDir(), g2) {
		t.Fatal("在途 NextGen 不得被 GC")
	}
	if !genExists(l.GenerationsDir(), g1) {
		t.Fatal("在途 PrevGen（回退目标）不得被 GC")
	}
	// 封相后再 GC（keep=0）：active=g2 必留，lkg=g1 必留
	if err := a.SealCommitted(g2); err != nil {
		t.Fatal(err)
	}
	if _, err := l.GCGenerations(0); err != nil {
		t.Fatal(err)
	}
	if !genExists(l.GenerationsDir(), g2) {
		t.Fatal("active 不得被 GC")
	}
	if !genExists(l.GenerationsDir(), g1) {
		t.Fatal("lkg 不得被 GC")
	}
	_ = g1
}

// TestGCStagingRespectsRetention —— staging 清理：只删超过保留期的；近期文件必留
// （正在进行的换代正持有这些文件，无保留期会自伤）。
func TestGCStagingRespectsRetention(t *testing.T) {
	dir := t.TempDir()
	l := Layout{ProfileDir: dir}
	if err := l.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(l.StagingDir(), "snap-fresh.zip")
	old := filepath.Join(l.StagingDir(), "snap-old.zip")
	oldDir := filepath.Join(l.StagingDir(), "portable-in-old")
	for _, p := range []string{fresh, old} {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	// 把 old 拨到 48 小时前
	past := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{old, oldDir} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	n, err := l.GCStaging(24 * time.Hour)
	if err != nil {
		t.Fatalf("GCStaging: %v", err)
	}
	if n < 2 {
		t.Fatalf("应至少删 2 项，实得 %d", n)
	}
	if _, serr := os.Stat(fresh); serr != nil {
		t.Fatal("近期 staging 文件不得被删")
	}
	if _, serr := os.Stat(old); !os.IsNotExist(serr) {
		t.Fatal("过期 staging 文件应被删")
	}
	if _, serr := os.Stat(oldDir); !os.IsNotExist(serr) {
		t.Fatal("过期 staging 目录应被删")
	}
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
