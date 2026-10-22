//go:build windows

// single_instance_windows_test.go —— v21.169 回归钉：单实例互斥。
//
// 缺陷背景（用户实机 2026-10-12，日志 app-2026-10-12.log）：
// 18:00:53 与 18:01:08 各有一次「file logging enabled」⇒ 短窗口内两个进程并存；
// 18:02:37 即 [sched] AUTH_REVOKED → session demoted to re-exchange → 掉登录 + 笔记消失。
// 根因：服务端 refresh「一次一换 + 族重放吊销」，而 exchangeMu 只在进程内有效。

package main

import "testing"

func TestSanitizeMutexNameStableAndDistinct(t *testing.T) {
	// 同输入必同输出（否则互斥体名每轮都变，单实例形同虚设）
	if a, b := sanitizeMutexName(`C:\data\sticky-note\profile`), sanitizeMutexName(`C:\data\sticky-note\profile`); a != b {
		t.Fatalf("同名档案必须得到同名互斥体：%q != %q", a, b)
	}
	// 不同档案必不同名（绿色版与安装版要能并存，互不误伤）
	portable := sanitizeMutexName(`C:\Users\TestUser\Downloads\StickyNote\sticky-note-data\profile`)
	installed := sanitizeMutexName(`C:\Users\TestUser\AppData\Roaming\StickyNote\profile`)
	if portable == installed {
		t.Fatal("不同档案目录不得产生同名互斥体（绿色版与安装版必须能并存）")
	}
	// 名字必须是 16 位 hex（Windows 对象名里只放得下安全字符）
	if len(portable) != 16 {
		t.Fatalf("互斥体名长度应为 16，实得 %d（%q）", len(portable), portable)
	}
	for _, c := range portable {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("互斥体名只允许小写 hex，实得 %q", portable)
		}
	}
}

func TestAcquireSingleInstanceBlocksSecondHolder(t *testing.T) {
	marker := t.TempDir() // 每个用例独立档案，避免与其他用例互斥
	release1, ok1 := acquireSingleInstance(marker)
	if !ok1 {
		t.Fatal("首个实例必须取得互斥体")
	}
	// 同一进程内第二次取（Windows 上同名 mutex 会 ERROR_ALREADY_EXISTS）
	release2, ok2 := acquireSingleInstance(marker)
	if ok2 {
		release2()
		release1()
		t.Fatal("v21.169：第二个实例必须被拦下（否则并发换票会触发族重放吊销）")
	}
	// 释放后应能再次取得 —— 验证 release 真的解锁，否则用户永远开不了第二个进程
	release1()
	release3, ok3 := acquireSingleInstance(marker)
	if !ok3 {
		t.Fatal("释放后应能重新取得互斥体（否则重开桌面会被永久拦死）")
	}
	release3()
}
