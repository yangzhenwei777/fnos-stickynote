package model

import (
	"testing"
	"time"

	"stickynote/database"
)

// ============================================================================
// refresh_session.go 单测（v21.97 审计 P-03 整改补充）
// 覆盖：CleanupExpiredRefreshSessions 新语义——删除**所有**过期行（含未吊销），
// 未过期的活跃会话必须保留（轮转仍可能引用）。
// ============================================================================

func TestCleanupExpiredRefreshSessions_DeletesAllExpired(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	now := time.Now().Unix()
	u, err := RegisterUser("cleanup1", "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatal(err)
	}
	_ = now // 时间基准；行创建用相对时间（下方 time.Now().Add）

	// 三种行：
	// 1) 过期且已吊销（旧语义也删）
	// 2) 过期但未吊销（P-03 新语义：必须删——旧条件漏删的正是这种）
	// 3) 未过期（活跃，必须保留）
	expiredRevoked := "jti-expired-revoked"
	expiredActive := "jti-expired-active"
	active := "jti-active"

	mustCreate := func(jti string, expiresAt time.Time, revoked bool) {
		if err := CreateRefreshSession(u.ID, "fam-"+jti, jti, "", expiresAt); err != nil {
			t.Fatalf("create session %s: %v", jti, err)
		}
		if revoked {
			if err := RevokeRefreshFamily("fam-" + jti); err != nil {
				t.Fatalf("revoke family %s: %v", jti, err)
			}
		}
	}
	mustCreate(expiredRevoked, time.Now().Add(-time.Hour), true)
	mustCreate(expiredActive, time.Now().Add(-time.Minute), false)
	mustCreate(active, time.Now().Add(24*time.Hour), false)

	n, err := CleanupExpiredRefreshSessions()
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n != 2 {
		t.Errorf("应删除 2 条过期行（含未吊销的 jti-expired-active，P-03 新语义），实际删 %d", n)
	}

	// 活跃行必须仍在
	var cnt int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM refresh_sessions WHERE jti = ?", active).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("未过期活跃会话应保留，实际 jti-active 剩余 %d 条", cnt)
	}
	// 两条过期行应已删净
	for _, jti := range []string{expiredRevoked, expiredActive} {
		if err := database.DB.QueryRow("SELECT COUNT(*) FROM refresh_sessions WHERE jti = ?", jti).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 0 {
			t.Errorf("过期行 %s 应被删除，实际剩余 %d", jti, cnt)
		}
	}
}

func TestCleanupExpiredRefreshSessions_NoRows(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("cleanup2", "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatal(err)
	}
	// 只有未过期活跃会话
	if err := CreateRefreshSession(u.ID, "fam-keep", "jti-keep", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := CleanupExpiredRefreshSessions()
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n != 0 {
		t.Errorf("无过期行时应删 0 条，实际 %d", n)
	}
}
