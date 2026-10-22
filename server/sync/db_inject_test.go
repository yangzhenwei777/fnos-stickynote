package sync

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite" // 与生产同驱动：注入测试必须用真驱动，否则又回到"假面"老问题

	"stickynote/database"
)

// v21.166 计划项5（X3 销账）· 包级 DB 注入回归钉。
//
// 目的：证明 `SetDB` 注入真的生效、`SetDB(nil)` 真的回落全局 ——
// **反向断言**：若注入被忽略，本用例会因"读到注入库的数据而非全局库"而失败，
// 防止退化成恒绿的假护栏（纪律：护栏本身要先自测）。

func openInjectedDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	h, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open injected db: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestSetDB_InjectionTakesEffectAndFallsBack(t *testing.T) {
	// 全局句柄（生产默认路径）
	globalDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	defer globalDB.Close()
	if _, err := globalDB.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('global');`); err != nil {
		t.Fatalf("seed global: %v", err)
	}
	old := database.SetDB(globalDB)
	defer database.SetDB(old)

	// 注入句柄（内容不同，可据此区分到底用了谁）
	injected := openInjectedDB(t, ":memory:")
	if _, err := injected.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('injected');`); err != nil {
		t.Fatalf("seed injected: %v", err)
	}

	read := func(d *sql.DB) string {
		var v string
		if err := d.QueryRow(`SELECT v FROM t LIMIT 1`).Scan(&v); err != nil {
			t.Fatalf("query: %v", err)
		}
		return v
	}

	// ① 未注入 → 走全局
	if got := read(db()); got != "global" {
		t.Fatalf("未注入时应读全局库，实际读到 %q", got)
	}

	// ② 注入 → 走注入库（核心断言：注入生效）
	SetDB(injected)
	if got := read(db()); got != "injected" {
		t.Fatalf("注入后应读注入库，实际读到 %q", got)
	}

	// ③ 解除注入 → 回落到全局（反向断言）
	SetDB(nil)
	if got := read(db()); got != "global" {
		t.Fatalf("解除注入后应回落全局库，实际读到 %q", got)
	}
}

// TestSetDB_NilSafe 反复注入/解除不得 panic（atomic.Pointer 语义）。
func TestSetDB_NilSafe(t *testing.T) {
	for i := 0; i < 3; i++ {
		SetDB(nil)
		if db() == nil {
			t.Fatalf("未注入且无全局库时 db() 返回 nil（i=%d）", i)
		}
	}
	SetDB(nil)
}

// TestSetDB_ConcurrentRead 并发注入下 db() 不得竞态（-race 门禁会抓）。
func TestSetDB_ConcurrentRead(t *testing.T) {
	globalDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	defer globalDB.Close()
	old := database.SetDB(globalDB)
	defer database.SetDB(old)

	h := openInjectedDB(t, ":memory:")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			SetDB(h)
			SetDB(nil)
		}
	}()
	for i := 0; i < 200; i++ {
		_ = db() // 只读不 panic 即达目的（nil 判断在调用侧）
	}
	<-done
	SetDB(nil)
}
