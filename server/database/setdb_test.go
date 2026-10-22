package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openRawForSwapTest 测试用小辅助：裸开一个句柄（不触全局），仅供 SetDB 换入验证。
func openRawForSwapTest(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	return db, db.Ping()
}

// TestSetDBSwapSemantics 验证受控换句柄：换入后全局=新句柄、返回旧句柄、nil 拒绝不动全局。
// （desktop localstore 启动链依赖此语义，绑定测试见 desktop/startup_localstore_test.go。）
func TestSetDBSwapSemantics(t *testing.T) {
	dir := t.TempDir()
	if err := Init(filepath.Join(dir, "first.db"), "1.0.174"); err != nil {
		t.Fatal(err)
	}
	first := GetDB()

	if old := SetDB(nil); old != nil {
		t.Fatal("SetDB(nil) must refuse")
	}
	if GetDB() != first {
		t.Fatal("SetDB(nil) must not touch global")
	}

	// 换入新句柄：返回旧句柄交调用方关闭（钉记：换库须持锁+置入，Close 由调用方负责）。
	second, err := openRawForSwapTest(filepath.Join(dir, "second.db"))
	if err != nil {
		t.Fatal(err)
	}
	if old := SetDB(second); old != first {
		t.Fatal("SetDB must return previous handle")
	}
	if GetDB() != second {
		t.Fatal("global must point to new handle")
	}
	_ = first.Close()
	_ = second.Close()
}
