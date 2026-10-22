package model

import (
	"path/filepath"
	"testing"

	"stickynote/database"
)

// ---- NextRestoreEpoch 纯函数（v20 §85/必测 §144 的算术核） ----

func TestNextRestoreEpoch(t *testing.T) {
	cases := []struct {
		name             string
		pre, restored, w int64
	}{
		{"恢复更旧代不回退（42←17→43）", 42, 17, 43},
		{"legacy 归档无字段（0 口径）", 2, 0, 3},
		{"同代恢复仍前进", 1, 1, 2},
		{"空库首次（0←0→1）", 0, 0, 1},
		{"恢复更新代（罕见：镜像漂移）", 3, 9, 10},
	}
	for _, tc := range cases {
		if got := NextRestoreEpoch(tc.pre, tc.restored); got != tc.w {
			t.Errorf("%s: NextRestoreEpoch(%d,%d)=%d want %d", tc.name, tc.pre, tc.restored, got, tc.w)
		}
	}
}

// ---- sync_meta 读写（真库，迁移 seed 生效） ----

func newMetaDB(t *testing.T) {
	t.Helper()
	if err := database.Init(filepath.Join(tempInstanceDir(t), "test.db"), "99.0.0"); err != nil {
		t.Fatalf("database.Init: %v", err)
	}
	t.Cleanup(func() {
		if database.DB != nil {
			database.DB.Close()
			database.DB = nil
		}
	})
}

func TestSyncMetaSeedAndRoundtrip(t *testing.T) {
	newMetaDB(t)
	// 迁移 seed：新库首次读取即代际 1（R0 约定）。
	if e, err := GetServerEpoch(); err != nil || e != 1 {
		t.Fatalf("seed epoch=%d err=%v want 1", e, err)
	}
	// Set→Get 往返 + 单例行（重复 Set 不产生第二行）。
	if err := SetServerEpoch(7); err != nil {
		t.Fatal(err)
	}
	if e, err := GetServerEpoch(); err != nil || e != 7 {
		t.Fatalf("after set epoch=%d err=%v want 7", e, err)
	}
	if err := SetServerEpoch(8); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM sync_meta").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sync_meta 必须保持单例，行数=%d", n)
	}
	// 行被外部清空（模拟半途迁移的 legacy 库）：Set 须自愈补行后写入目标值。
	if _, err := database.DB.Exec("DELETE FROM sync_meta"); err != nil {
		t.Fatal(err)
	}
	if e, err := GetServerEpoch(); err != nil || e != 0 {
		t.Fatalf("无行按 legacy=0 口径，得 epoch=%d err=%v", e, err)
	}
	if err := SetServerEpoch(9); err != nil {
		t.Fatal(err)
	}
	if e, err := GetServerEpoch(); err != nil || e != 9 {
		t.Fatalf("自愈后 epoch=%d err=%v want 9", e, err)
	}
}
