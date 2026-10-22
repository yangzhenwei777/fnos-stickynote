package portable

// redline26_outbox_test.go —— 红线 26「Portable 恢复 DB 后缺必要同步状态」的两条缺口断言。
//
// 台账原缺口（§九 之前）：刀 1/刀 3 收窄了数据面（ListConflicts / OpenOps / allowed_resolutions），
// 但仍缺两条：
//   ① **"A 未推 op 随包到达 B"** —— 即「未推送的本地 CREATE 必须随portable 包落到接收方」
//      （Import 走 carryOverOutbox，bootstrap.go:360-434）；
//   ② **"同包二次导入不翻倍"** —— 同一个 .snbak 连续导入两次，笔记数与 outbox 行数不得翻倍。
//
// 风险面：
//   - 缺 ① ⇒ 用户「把笔记从 A 带到 B」，A 上那条还没上传的笔记**内容与账一起消失**
//     （用户以为存上了，实际丢了，且服务端也没有 —— 数据静默丢失，最坏一档）；
//   - 缺 ② ⇒ 重复导入（同包重试／换机重复操作）会让笔记与待推op **成倍复制**，
//     同sync_id 多行还会撞 UNIQUE 或产生重复实体。
//
// 幂等依据（读码，2026-10-08）：落库语句是 `INSERT OR IGNORE ... sync_outbox`
// （bootstrap.go:427-429）+ `op_id` 有 UNIQUE 约束（synclocal/store.go DDL）⇒ 同 op_id 天然幂等；
// 实体行搬运 `carryEntityRow` 先查 `COUNT(*) WHERE sync_id=?`，>0 即return（bootstrap.go:456-460）。
// 本用例把这两条机制连同「账在物在」不变式一起钉住。

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"stickynote-desktop/localstore"
)

// mustOpenActive 打开 profile 当前激活代（调用方自行 Close）。
func mustOpenActive(t *testing.T, prof string) *sql.DB {
	t.Helper()
	lay := localstore.Layout{ProfileDir: prof}
	active, err := lay.Active()
	if err != nil || active == "" {
		t.Fatalf("读激活代：active=%q err=%v", active, err)
	}
	db, err := localstore.OpenGenDB(lay.GenDB(active))
	if err != nil {
		t.Fatalf("开代库：%v", err)
	}
	return db
}

// TestRedline26PortableOutboxCarry —— 红线 26 两条缺口。
func TestRedline26PortableOutboxCarry(t *testing.T) {
	const pw = "correct horse battery"

	t.Run("①A的未推CREATE随包到达B_账在物在", func(t *testing.T) {
		root := t.TempDir()
		// A：自己就有一条**未推送**的 CREATE（用户以为存上了，其实只在 A 本机）
		profA := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", instanceID: "inst-A",
			cursor: 10, epoch: 7, noteTitle: "A已同步的笔记", noteSyncID: "srv-a1",
			hasProfile: true, pendingOp: true})
		out := filepath.Join(root, "carry.snbak")
		exportRes, err := Export(ExportOptions{ProfileDir: profA, UploadDir: filepath.Join(profA, "uploads"),
			Password: pw, OutPath: out, AppVersion: "test"})
		if err != nil {
			t.Fatalf("导出: %v", err)
		}
		// 导出侧计数必须已含那条未推 op（OutboxPending ≥ 1）—— 导出漏了后面无从谈起
		if exportRes.OutboxPending < 1 {
			t.Fatalf("导出元数据应含未推 op 计数，实得 %+v", exportRes)
		}

		// B：全新机器（无档案）接包
		profB := filepath.Join(root, "B")
		if err := os.MkdirAll(filepath.Join(profB, "staging"), 0700); err != nil {
			t.Fatal(err)
		}
		res, err := Import(ImportOptions{ProfileDir: profB, UploadDir: filepath.Join(profB, "uploads"),
			InPath: out, Password: pw})
		if err != nil {
			t.Fatalf("导入: %v", err)
		}
		// 注：res.Carried 计的是「**本机旧账**搬运条数」（import.go:170 只在 oldDB != nil 时累加），
		// 全新装机本机无旧代 ⇒ Carried=0 是正确的，不是缺陷。真正要钉的是**数据面**：
		// A 的未推 op 必须随整库激活落到新代（下面逐条断言），否则「跨机搬笔记」会把
		// A 上那条还没上传的笔记变成**只读存在、永不上传**（服务端也没有 → 静默丢失）。
		if res.HadProfile {
			t.Fatal("前置：全新机不应有旧代（若为 true 则本用例失去「跨机」意义）")
		}
		if res.NoteCount != 2 {
			t.Fatalf("全新机导入后应有 2 条笔记（A 已同步 + A 未推），实得 %d", res.NoteCount)
		}

		db := mustOpenActive(t, profB)
		defer db.Close()

		// 账在物在：op 行 + 实体行都必须在新代
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE op_id='op-local-1' AND status='PENDING'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("红线26：未推 op 未落到 B，n=%d", n)
		}
		var title string
		if err := db.QueryRow(`SELECT title FROM notes WHERE sync_id='srv-new'`).Scan(&title); err != nil {
			t.Fatalf("红线26：未推实体行未随 op 搬入（账在物在不变式破坏）: %v", err)
		}
		if title != "未推送新建" {
			t.Fatalf("实体行内容错误：%q", title)
		}
		// A 已同步的笔记也必须在（常规内容）
		if err := db.QueryRow(`SELECT title FROM notes WHERE sync_id='srv-a1'`).Scan(&title); err != nil {
			t.Fatalf("A 已同步笔记缺失: %v", err)
		}
	})

	t.Run("②同包二次导入不翻倍", func(t *testing.T) {
		root := t.TempDir()
		profA := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", instanceID: "inst-A",
			cursor: 10, epoch: 7, noteTitle: "A笔记", noteSyncID: "srv-a1",
			hasProfile: true, pendingOp: true})
		out := filepath.Join(root, "twice.snbak")
		if _, err := Export(ExportOptions{ProfileDir: profA, UploadDir: filepath.Join(profA, "uploads"),
			Password: pw, OutPath: out, AppVersion: "test"}); err != nil {
			t.Fatal(err)
		}

		profB := filepath.Join(root, "B")
		if err := os.MkdirAll(filepath.Join(profB, "staging"), 0700); err != nil {
			t.Fatal(err)
		}
		imp := func() *ImportResult {
			t.Helper()
			res, err := Import(ImportOptions{ProfileDir: profB, UploadDir: filepath.Join(profB, "uploads"),
				InPath: out, Password: pw})
			if err != nil {
				t.Fatalf("导入: %v", err)
			}
			return res
		}
		imp() // 第一次

		db := mustOpenActive(t, profB)
		defer db.Close()
		snap := func() (notes, ops int) {
			var n1, n2 int
			if err := db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n1); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&n2); err != nil {
				t.Fatal(err)
			}
			return n1, n2
		}
		n1, o1 := snap()

		imp() // 第二次：同包再导一次
		n2, o2 := snap()

		if n2 != n1 {
			t.Fatalf("红线26：同包二次导入后笔记数翻倍 %d → %d", n1, n2)
		}
		if o2 != o1 {
			t.Fatalf("红线26：同包二次导入后 outbox 行数翻倍 %d → %d", o1, o2)
		}
		// 明细断言：op-local-1 仍只有一条，且实体不重复
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE op_id='op-local-1'`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Fatalf("同一 op_id 应只存一条（INSERT OR IGNORE + UNIQUE 幂等），实得 %d", c)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id='srv-a1'`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Fatalf("同一 sync_id 实体应只存一条（carryEntityRow 先查后插），实得 %d", c)
		}
	})

	t.Run("③跨机语义_包内未推op随整库激活到全新机", func(t *testing.T) {
		// 承①的加强对照：确认「未推op 随包到达」走的是**整库激活**这条路径，
		// 而不是依赖本机旧账搬运（后者在全新机上恒为 0 条）。
		root := t.TempDir()
		profA := seedProfile(t, root, seedOpts{profileDir: "A", deviceID: "devA", instanceID: "inst-A",
			cursor: 10, epoch: 7, noteTitle: "n", noteSyncID: "srv-a1", hasProfile: true, pendingOp: true})
		out := filepath.Join(root, "x.snbak")
		if _, err := Export(ExportOptions{ProfileDir: profA, UploadDir: filepath.Join(profA, "uploads"),
			Password: pw, OutPath: out, AppVersion: "test"}); err != nil {
			t.Fatal(err)
		}
		profB := filepath.Join(root, "B")
		if err := os.MkdirAll(filepath.Join(profB, "staging"), 0700); err != nil {
			t.Fatal(err)
		}
		res, err := Import(ImportOptions{ProfileDir: profB, UploadDir: filepath.Join(profB, "uploads"),
			InPath: out, Password: pw})
		if err != nil {
			t.Fatal(err)
		}
		if res.HadProfile {
			t.Fatal("前置：全新机不应有旧代")
		}
		if res.OutboxPending != 1 {
			t.Fatalf("红线26：全新机导入后未推 op 计数应为 1（随整库激活而来），实得 %+v", res.OutboxPending)
		}
		db := mustOpenActive(t, profB)
		defer db.Close()
		var st string
		var ent int
		if err := db.QueryRow(`SELECT status FROM sync_outbox WHERE op_id='op-local-1'`).Scan(&st); err != nil {
			t.Fatalf("红线26：包内未推 op 未随整库激活落到新机: %v", err)
		}
		if st != "PENDING" {
			t.Fatalf("未推 op 应为 PENDING（否则永不上传），实得 %q", st)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id='srv-new'`).Scan(&ent); err != nil {
			t.Fatal(err)
		}
		if ent != 1 {
			t.Fatalf("账在物在：实体行应随包到达，实得 %d 行", ent)
		}
	})
}
