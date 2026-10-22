package handler

// refresh_grace_test.go —— v21.217 rotation 重放宽限期的行为钉与旧钉适配。
//
// 生产实证（211b~211f 五轮桌面实机日志）：弱网下「服务端已轮转成功、响应未达
// 客户端」高频发生；客户端重试/重开即用已吊销旧票 → 原实现撤族+401 → 用户被迫
// 反复重登（死循环）。修法对齐 RFC 9700 / Okta / Auth0：**重放宽限期**——
// 已吊销 jti 在窗内重现且 family 仍活跃 ⇒ 续签同族新票；窗外或族已死 ⇒ 维持撤族。
//
// ⚠ 既有 rotation 钉（refresh_rotation_test.go）钉的是「一次性 token」契约。
// 宽限期把该契约从「轮转后旧票恒 401」收窄为「宽限期外旧票 401」。
// 为不弱化既有钉：旧测试经 setGraceZero 置 0 恢复严格语义；宽限行为由本文件钉住。

import (
	"stickynote/database"
	"stickynote/model"

	"net/http"
	"testing"
)

// setGraceZero 临时把宽限期置 0（恢复 v21.216 前的严格一次性语义）。
// 测试结束自动恢复，杜绝跨测试污染。
func setGraceZero(t *testing.T) {
	t.Helper()
	old := refreshReuseGrace
	refreshReuseGrace = 0
	t.Cleanup(func() { refreshReuseGrace = old })
}

// revokeFamilyOf 吊销该用户当前唯一的活跃 family（模拟登出/改密的全局吊销）。
func revokeFamilyOf(t *testing.T, userID int64) error {
	t.Helper()
	var fam string
	if err := database.DB.QueryRow(
		"SELECT family_id FROM refresh_sessions WHERE user_id = ? AND revoked_at IS NULL LIMIT 1",
		userID).Scan(&fam); err != nil {
		return err
	}
	return model.RevokeRefreshFamily(fam)
}

// TestRefreshGrace_ReplayWithinGraceReissues 新语义核心钉：
// 宽限期内重放已轮转的旧票 ⇒ **200 + 同族新票**（而非撤族 401）。
// 这正是弱网桌面端「响应丢失 → 重试」的救命路径。
func TestRefreshGrace_ReplayWithinGraceReissues(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	_, tok1 := loginAndGetRefresh(t, "grace1", "Passw0rd!2026")

	code, _ := doRefresh(tok1)
	if code != http.StatusOK {
		t.Fatalf("首次换票应 200, got %d", code)
	}

	// 宽限期内旧票再现（模拟：服务端轮转成功但 tok2 未送达客户端，客户端重试）
	code2, tok3 := doRefresh(tok1)
	if code2 != http.StatusOK {
		t.Fatalf("宽限期内重放旧票应 200 续签（弱网自愈路径），got %d", code2)
	}
	if tok3 == "" || tok3 == tok1 {
		t.Fatalf("宽限续签应签发新 refresh, got %q", tok3)
	}

	// 续签出的新票必须真实可用（下一轮轮转 200）
	code3, _ := doRefresh(tok3)
	if code3 != http.StatusOK {
		t.Fatalf("宽限续签的新票应可用, got %d", code3)
	}
}

// TestRefreshGrace_FamilyDeadStillRejected 安全底线钉：
// family 已全撤（真重放攻击前族已被其它路径吊销 / 登出 / 改密）时，
// 即便在宽限窗口内也必须 401 —— 宽限只救「轮转成功但响应丢失」，不救死族。
func TestRefreshGrace_FamilyDeadStillRejected(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	u, tok1 := loginAndGetRefresh(t, "grace2", "Passw0rd!2026")

	// 直接吊销整族（模拟登出/改密触发的全局吊销）
	if err := revokeFamilyOf(t, u.ID); err != nil {
		t.Fatalf("revoke family: %v", err)
	}

	code, _ := doRefresh(tok1)
	if code != http.StatusUnauthorized {
		t.Fatalf("family 已死时宽限不得续命，应 401, got %d", code)
	}
}
