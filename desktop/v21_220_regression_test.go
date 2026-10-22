// v21_220_regression_test.go —— 213 实机复测定罪「账号卡片永久显示未登录」的 Go 侧回归钉。
//
// 定罪背景（213 实机三方交叉取证，非推测）：
//
//	· 日志：换代成功（`generation g-000002 sealed+committed`、`gen_epoch=1` 持久化、
//	  `接管后锚点自检 OK（epoch=2 cursor=290）`）；
//	· 数据库 g-000002：notes 48 / note_folders 9 / sync_changes 290，
//	  client_profile 的 refresh_sealed 515B 完好；
//	· WebView2 Local Storage：`user = {"id":1,"username":"testuser"}` **已正确写入**。
//
//	⇒ Go 侧一切正常，**但账号卡片恒显示「未登录：处于离线单机状态」**。
//
// 机制（本文件只钉 Go 侧那一半）：
// 首屏恰逢换代窗口（DB 句柄切换中）⇒ `AuthStatus` 内 `EnsureTables`/`Status`
// 抛 `sql: database is closed` ⇒ Wails 绑定 reject ⇒ 前端 `refresh()` 进 catch
// ⇒ `st` 停在 `{}` ⇒ 显示"未登录"。
//
// 核心不变式（v21.220 钉死）：**「读不到状态」不得被表达成「用户未登录」。**
// AuthStatus 在内部读表失败时必须**返回结构完整的降级 map 而非 error**，
// 否则前端把「我还没问出结果」误报成「你没登录」⇒ 用户反复重登
// ⇒ 每次重登又制造新的换代窗口 ⇒ 恶性循环（213 实机即此）。
package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"stickynote/database"
)

// closeDBForTest 造一个"已关闭的 DB 句柄"——**这正是 213 实机换代窗口的物理形态**
// （bootstrap 换代期间 `database.SetDB(newDB)` 已切走，而前端此刻来读旧句柄）。
//
// 实现要点：sql.Open 后立即 Close，再把它设为全局句柄。
// 此时任何 Query/Exec 都返回 `sql: database is closed`，与实机日志逐字一致。
func closeDBForTest(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	closed, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	old := database.SetDB(closed)
	t.Cleanup(func() {
		database.SetDB(old)
		if old != nil && old != closed {
			old.Close()
		}
	})
	return closed, old
}

// TestAuthStatusNeverThrowsOnInternalFailure 钉住降级契约的**结构**。
//
// 213 实机的具体触发：首屏 authStatus 撞上换代窗口（DB 句柄已切走）。
// 此钉直接用「已关闭句柄」复现该条件，断言 AuthStatus 仍返回可用的 map。
func TestAuthStatusNeverThrowsOnInternalFailure(t *testing.T) {
	closeDBForTest(t)

	l := &LocalService{}
	// ⚠ 关键：第二个返回值必须是 nil error —— 前端拿到 error 就进 catch 卡死空态。
	st, err := l.AuthStatus()
	if err != nil {
		t.Fatalf("AuthStatus 不得因内部读表失败而抛错（213 事故根因之一）: %v", err)
	}
	if st == nil {
		t.Fatal("AuthStatus 必须返回非 nil map（前端会读 st.logged_in 等字段）")
	}

	// 必填字段齐全性：前端 stateLine/computed 依赖这些键存在。
	for _, k := range []string{
		"status_degraded", "status_error", "profile",
		"online", "accounting", "logged_in", "relogin_required",
	} {
		if _, ok := st[k]; !ok {
			t.Errorf("降级返回缺少必填字段 %q（前端依赖它判断状态）", k)
		}
	}

	// ⭐ 降级时 status_degraded 必须为 true：前端据此显示「正在读取」而非「未登录」
	if st["status_degraded"] != true {
		t.Errorf("status_degraded 应为 true，实际 %v", st["status_degraded"])
	}
	// ⭐ 降级时**绝不能**断言 relogin_required=true —— 那会让用户去重新登录，
	// 而凭据其实完好（213 实机 refresh_sealed 515B 在位）。
	if st["relogin_required"] == true {
		t.Error("降级时不得断言 relogin_required=true（凭据可能完好，误报会让用户无谓重登）")
	}
	// status_error 需带真实原因，供 UI 展示与排障
	msg, _ := st["status_error"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Error("status_error 不得为空（降级原因必须可观测，否则又变静默失败）")
	}
}

// TestAuthStatusSessionRecoverableOnlyWhenActuallyReadable 钉住一条易被"顺手优化"破坏的语义。
//
// v21.203 曾经的教训：`recoverable` 只看 server_url 会造出"僵尸已登录"。
// v21.220 的教训：**降级时更不能猜** —— 读不到封存件就断言 false，
// 但必须靠 status_degraded 让前端知道「这是未知，不是确认」。
func TestAuthStatusSessionRecoverableOnlyWhenActuallyReadable(t *testing.T) {
	closeDBForTest(t)

	l := &LocalService{}
	st, err := l.AuthStatus()
	if err != nil {
		t.Fatalf("降级路径不得抛错: %v", err)
	}
	if st["session_recoverable"] == true {
		t.Error("降级时 session_recoverable 必须为 false/缺省（读不到封存件就是不知道，不能猜）")
	}
}

// TestAuthStatusHealthyPathNoDegradedFlag 反向验证：健康路径不得被误标降级。
//
// 防「为了让测试过而把降级标记无条件置 true」这类反向劣化。
func TestAuthStatusHealthyPathNoDegradedFlag(t *testing.T) {
	// 造一个真库：建最小 schema 后设全局句柄
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	old := database.SetDB(db)
	t.Cleanup(func() {
		database.SetDB(old)
		if old != nil && old != db {
			old.Close()
		}
	})

	l := &LocalService{}
	st, err := l.AuthStatus()
	if err != nil {
		t.Fatalf("健康路径不得抛错: %v", err)
	}
	// 首次运行（无档案）是正常态：profile=false 但**不是**降级
	if st["status_degraded"] == true {
		t.Errorf("健康路径不得标记 status_degraded（会把正常首显误报成异常）")
	}
	if st["profile"] != false {
		t.Errorf("空库时 profile 应为 false，实际 %v", st["profile"])
	}
	// ⭐ 从未登录 ≠ 需要重新登录（v21.152 语义，防「一启动就催你重登」）
	if st["relogin_required"] == true {
		t.Error("从未登录（无档案）时 relogin_required 应为 false")
	}
}
