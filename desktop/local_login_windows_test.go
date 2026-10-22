//go:build windows

package main

// local_login_windows_test.go —— 展示用账号名的来源断言（v21.140）。
//
// 缺陷：桌面登录成功后前端会打 `/auth/login` 建立"本地前端会话"，该端点在桌面被映射到
// LocalLogin，而它曾经**硬编码** username="local" → 覆盖掉远端账号名 → 侧边栏
// （AppSidebar/MainLayout 显示 auth.user.username）永远显示 local，与登录的账号无关。
// 本测试钉住"有设备档案时必须用档案里的用户名，无档案才回落 local"。

import (
	"testing"

	"stickynote/database"
)

func TestLocalLoginUsesProfileUsername(t *testing.T) {
	ls, _ := bootWithLogin(t) // 已 SignIn(username="alice")
	got := ls.LocalLogin()
	user, _ := got["user"].(map[string]any)
	if user == nil {
		t.Fatalf("LocalLogin 缺少 user 字段: %+v", got)
	}
	if user["username"] != "alice" {
		t.Fatalf("username=%v want alice（应取设备档案里的远端账号名，而非桩值 local）", user["username"])
	}
	if user["role"] != "user" {
		t.Fatalf("role=%v want user（桌面壳不透出远端角色）", user["role"])
	}
}

func TestLocalLoginFallsBackToLocalWithoutProfile(t *testing.T) {
	saved := database.GetDB()
	dir := t.TempDir()
	if _, err := bootLocalstore(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		setWorker(nil)
		database.GetDB().Close()
		if saved != nil {
			database.SetDB(saved)
		}
	})
	_ = dir

	ls := NewLocalService(dir)
	got := ls.LocalLogin()
	user, _ := got["user"].(map[string]any)
	if user == nil {
		t.Fatalf("LocalLogin 缺少 user 字段: %+v", got)
	}
	// 全新库无 client_profile → 离线单机语义，回落桩值
	if user["username"] != "local" {
		t.Fatalf("username=%v want local（无档案时应回落离线单机桩值）", user["username"])
	}
}
