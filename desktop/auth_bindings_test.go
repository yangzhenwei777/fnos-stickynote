package main

// 批次 4 · 刀 2 绑定层冒烟：restoreDeviceSession/AuthStatus 在"无档案/非 Windows"下的
// 明确降级形态（不 panic、不静默假绿）。真实登录/轮转闭环归真机走查线（批次 4 验证门）。

import (
	"path/filepath"
	"runtime"
	"testing"

	"stickynote/database"

	"stickynote-desktop/localauth"
)

func TestAuthStatusShapeNoProfile(t *testing.T) {
	dir := t.TempDir()
	if _, err := bootLocalstore(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.GetDB().Close() })

	ls := NewLocalService(dir)
	if err := ls.restoreDeviceSession(dir); err != nil {
		t.Fatalf("restore must be non-fatal: %v", err)
	}
	st, err := ls.AuthStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st["logged_in"] != false || st["profile"] != false || st["session_restored"] != false {
		t.Fatalf("fresh profile must report logged_in=false profile=false restored=false: %+v", st)
	}
}

func TestRestoreOnWindowsTamperAndLinuxDegrade(t *testing.T) {
	dir := t.TempDir()
	if _, err := bootLocalstore(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.GetDB().Close() })
	ls := NewLocalService(dir)

	// 伪造档案行（secret 封存件为垃圾）：Windows 必须走"不可解→需重登"降级，禁恢复成功。
	if err := localauth.EnsureTables(database.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO client_profile (id, device_id, server_url, username, secret_sealed)
		VALUES (1,'forged','http://x','u', X'0102030405060708') ON CONFLICT(id) DO UPDATE SET secret_sealed=excluded.secret_sealed`); err != nil {
		t.Fatal(err)
	}
	if err := ls.restoreDeviceSession(dir); err != nil {
		t.Fatalf("restore must swallow unsealable profile: %v", err)
	}
	st, err := ls.AuthStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st["profile"] != true {
		t.Fatalf("forged row must be visible as profile=true: %+v", st)
	}
	if runtime.GOOS == "windows" {
		if st["profile_openable"] != false {
			t.Fatalf("windows: forged sealed must report profile_openable=false, got %+v", st["profile_openable"])
		}
		if ls.sessionRestored {
			t.Fatal("windows: unsealable profile must NOT set sessionRestored")
		}
	}
	_ = filepath.Separator
}
