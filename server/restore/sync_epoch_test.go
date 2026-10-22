package restore

import (
	"errors"
	"strings"
	"testing"

	"stickynote/database"
	"stickynote/model"
)

// ---- R0 代际与 Ready 屏障（v20 §85/§87，必测 §144/§145） ----

func getEpoch(t *testing.T) int64 {
	t.Helper()
	e, err := model.GetServerEpoch()
	if err != nil {
		t.Fatalf("GetServerEpoch: %v", err)
	}
	return e
}

// TestRestoreEpochAdvancesMonotonically §144：
//
//	1→2→3，再恢复"更旧"归档（restored=1，pre=3）→ 4，绝不回退。
func TestRestoreEpochAdvancesMonotonically(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	if got := getEpoch(t); got != 1 {
		t.Fatalf("新库迁移后 seed epoch=%d want 1", got)
	}
	archive1, bid1 := makeArchive(t, dataDir, uploadDir) // 快照同源声明 server_epoch=1
	addNote(t, "gone-by-restore-1")

	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	if err := c.Run(Request{RestoreID: "ep-1", BackupID: bid1, ArchiveBytes: archive1}); err != nil {
		t.Fatalf("restore#1: %v", err)
	}
	if got := getEpoch(t); got != 2 {
		t.Fatalf("restore#1 后 epoch=%d want 2", got)
	}

	archive2, bid2 := makeArchive(t, dataDir, uploadDir) // 声明 =2
	addNote(t, "gone-by-restore-2")
	c2 := New(defaultOpts(dataDir, uploadDir, dbPath))
	if err := c2.Run(Request{RestoreID: "ep-2", BackupID: bid2, ArchiveBytes: archive2}); err != nil {
		t.Fatalf("restore#2: %v", err)
	}
	if got := getEpoch(t); got != 3 {
		t.Fatalf("restore#2 后 epoch=%d want 3", got)
	}

	// 关键：再恢复"更旧"的 archive1（restored=1 < pre=3）→ new=max(3,1)+1=4。
	c3 := New(defaultOpts(dataDir, uploadDir, dbPath))
	if err := c3.Run(Request{RestoreID: "ep-3", BackupID: bid1, ArchiveBytes: archive1}); err != nil {
		t.Fatalf("restore#3(旧档): %v", err)
	}
	if got := getEpoch(t); got != 4 {
		t.Fatalf("恢复旧档后代际必须前进为 4，得 %d（红线 14：绝不倒退）", got)
	}
	if n := noteCount(t); n != 1 {
		t.Fatalf("旧档内容应完整回滚到位（1 条笔记），得 %d", n)
	}
}

// TestRestoreEpochUnchangedOnRollback §85"失败 epoch 不变"：DONE 前注入失败 →
// 回滚后 live 库代际保持原值。
func TestRestoreEpochUnchangedOnRollback(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	archive, bid := makeArchive(t, dataDir, uploadDir)
	addNote(t, "must-survive")

	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	c.failAt = func(next State) error {
		if next == StateMigrated {
			return errors.New("injected pre-commit failure")
		}
		return nil
	}
	if err := c.Run(Request{RestoreID: "ep-rb", BackupID: bid, ArchiveBytes: archive}); err == nil {
		t.Fatal("应返回注入错误")
	}
	if database.IsRestoring() {
		t.Fatal("回滚后应关闭窗口")
	}
	if got := getEpoch(t); got != 1 {
		t.Fatalf("回滚后 epoch=%d want 1（失败不得消费代际）", got)
	}
	if noteCount(t) != 2 {
		t.Fatal("回滚后原数据应完好")
	}
}

// TestRestoreCommittedTailFailureKeepsWindow §145/§87 终测：
// finalize（会话吊销）失败 → 数据与代际已提交但**维护窗口保持**，Health≠200；
// 重启路径（StartupRecover + NeedsStartupFinalize + Finalize）补收尾后日志清零。
func TestRestoreCommittedTailFailureKeepsWindow(t *testing.T) {
	dataDir, uploadDir, dbPath := buildInstance(t)
	archive, bid := makeArchive(t, dataDir, uploadDir)
	addNote(t, "will-be-replaced")
	t.Cleanup(func() { database.SetRestoring(false) }) // 复位全局门标，防串测试

	before := tokenVersion(t)
	c := New(defaultOpts(dataDir, uploadDir, dbPath))
	c.failAt = func(next State) error {
		if next == StateFinalizing { // 注入在"已提交、未吊销"缝隙
			return errors.New("injected finalize failure")
		}
		return nil
	}
	err := c.Run(Request{RestoreID: "tw-1", BackupID: bid, ArchiveBytes: archive})
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("应返回注入错误，得 %v", err)
	}
	if !database.IsRestoring() {
		t.Fatal("finalize 失败必须保持维护窗口（红线 16：Health 200 早于吊销）")
	}
	if got := getEpoch(t); got != 2 {
		t.Fatalf("epoch 应已随 DONE 固化=%d want 2", got)
	}
	if noteCount(t) != 1 {
		t.Fatal("数据已提交，不得回滚")
	}
	j, jerr := loadJournal(dataDir)
	if jerr != nil || j == nil {
		t.Fatalf("journal 应保留待收尾: %v", jerr)
	}
	if j.State != StateDone || j.NewEpoch != 2 || j.PreRestoreEpoch != 1 {
		t.Fatalf("journal 收尾态不符: %+v", j)
	}
	if tokenVersion(t) != before {
		t.Fatal("注入失败路径不得提前吊销会话")
	}

	// 模拟重启补收尾：StartupRecover 保留 DONE → NeedsStartupFinalize → Finalize。
	c2 := New(defaultOpts(dataDir, uploadDir, dbPath))
	if err := c2.StartupRecover(); err != nil {
		t.Fatalf("StartupRecover: %v", err)
	}
	rid := c2.NeedsStartupFinalize()
	if rid != "tw-1" {
		t.Fatalf("应检出待收尾恢复 %q，得 %q", "tw-1", rid)
	}
	if err := c2.Finalize(rid); err != nil {
		t.Fatalf("补收尾 Finalize: %v", err)
	}
	if j, _ := loadJournal(dataDir); j != nil {
		t.Fatal("补收尾成功后日志应清除")
	}
	if tokenVersion(t) <= before {
		t.Fatal("补收尾必须完成会话吊销")
	}
}
