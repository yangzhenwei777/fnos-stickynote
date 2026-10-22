package handler

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"stickynote/auth"
	"stickynote/database"
	"stickynote/model"
)

// ============================================================================
// Refresh Token 一次性轮转单测（v21.93 P1：R-01 / H-04 审计缺口回归点）
//
// 缺陷：v21.90 声称「refresh token 轮换」，实现只是每次换票时签发一张新票，
// 既未记录也未校验 jti；注释所称「旧 token 由 token_version 全局吊销兜底」
// 并不成立——Refresh 路径从不 bump token_version。结果是旧 refresh 在过期前
// （7 天 / 记住我 30 天）可反复换发，泄露窗口并未缩短（replacement，非 rotation）。
//
// 修复：登录/换票时登记当前有效 jti（users.refresh_jti），换票时比对，
// 不符即判定重放并全局吊销。存量用户（jti 为空）首次换票放行并登记。
// ============================================================================

// loginAndGetRefresh 走完整登录流程，返回用户与响应体中的 refresh token。
// 登录路径会登记 jti（issueTokenPair），是与生产一致的状态起点。
func loginAndGetRefresh(t *testing.T, username, password string) (*model.User, string) {
	t.Helper()
	auth.Init("unit-test-jwt-secret-for-refresh")
	u, err := model.RegisterUser(username, password, "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	c, w := newNoteContext("POST", "/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	Login(c)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, got %d body=%s", w.Code, w.Body.String())
	}
	return u, parseRefreshToken(t, w.Body.String())
}

func parseRefreshToken(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("parse login response: %v (%s)", err, body)
	}
	if resp.RefreshToken == "" {
		t.Fatalf("响应体缺少 refresh_token: %s", body)
	}
	return resp.RefreshToken
}

// storedJTI 读取库中当前登记的 jti。
func storedJTI(t *testing.T, userID int64) string {
	t.Helper()
	var jti string
	if err := database.DB.QueryRow(
		"SELECT refresh_jti FROM users WHERE id = ?", userID).Scan(&jti); err != nil {
		t.Fatalf("query refresh_jti: %v", err)
	}
	return jti
}

// doRefresh 用给定 token 发起一次换票，返回状态码与新的 refresh token。
func doRefresh(token string) (int, string) {
	c, w := newNoteContext("POST", "/api/auth/refresh",
		`{"refresh_token":"`+token+`"}`)
	Refresh(c)
	if w.Code != http.StatusOK {
		return w.Code, ""
	}
	return w.Code, parseRefreshTokenSilently(w.Body.String())
}

func parseRefreshTokenSilently(body string) string {
	var resp struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.Unmarshal([]byte(body), &resp)
	return resp.RefreshToken
}

// TestRefreshRotation_LoginRegistersJTI 登录后应登记当前 refresh 的 jti，
// 否则旧票会因「库中 jti 为空」被当成存量首次换票而多活一轮。
func TestRefreshRotation_LoginRegistersJTI(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)
	setGraceZero(t) // v21.217：既有钉恢复严格一次性语义（宽限行为另钉于 refresh_grace_test.go）

	u, tok := loginAndGetRefresh(t, "rr1", "Passw0rd!2026")

	claims, err := auth.ParseToken(tok)
	if err != nil {
		t.Fatalf("parse refresh token: %v", err)
	}
	if got := storedJTI(t, u.ID); got != claims.ID {
		t.Errorf("登录后应登记 jti：库=%q token=%q", got, claims.ID)
	}
}

// TestRefreshRotation_OldTokenRejectedAfterRotation 轮转后旧票必须失效——
// 这是「rotation」与「replacement」的分水岭。
func TestRefreshRotation_OldTokenRejectedAfterRotation(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)
	setGraceZero(t) // v21.217：既有钉恢复严格一次性语义（宽限行为另钉于 refresh_grace_test.go）

	u, tok1 := loginAndGetRefresh(t, "rr2", "Passw0rd!2026")

	code, tok2 := doRefresh(tok1)
	if code != http.StatusOK {
		t.Fatalf("首次换票应 200, got %d", code)
	}
	if tok2 == "" || tok2 == tok1 {
		t.Fatalf("应签发不同的新 refresh token, got %q", tok2)
	}

	// 库中 jti 应跟进到新票
	claims2, err := auth.ParseToken(tok2)
	if err != nil {
		t.Fatalf("parse new refresh token: %v", err)
	}
	if got := storedJTI(t, u.ID); got != claims2.ID {
		t.Errorf("换票后应更新 jti：库=%q 新票=%q", got, claims2.ID)
	}

	// 旧票再换 → 401（v21.90 行为是继续签发新票，此处即为回归点）
	code2, _ := doRefresh(tok1)
	if code2 != http.StatusUnauthorized {
		t.Errorf("轮转后旧 refresh 应 401, got %d", code2)
	}
}

// TestRefreshRotation_ReplayRevokesFamily 重放检测应撤销整个 family：
// 该设备全部会话（含新签发的票）一并失效，401 后即使拿轮转后的新票也换不出。
// v21.94 P3-7 语义：只撤该设备，不 bump token_version（不误伤其他设备）。
func TestRefreshRotation_ReplayRevokesFamily(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)
	setGraceZero(t) // v21.217：既有钉恢复严格一次性语义（宽限行为另钉于 refresh_grace_test.go）

	u, tok1 := loginAndGetRefresh(t, "rr3", "Passw0rd!2026")
	before, err := model.GetUserByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}

	code, tok2 := doRefresh(tok1)
	if code != http.StatusOK {
		t.Fatalf("首次换票应 200, got %d", code)
	}
	// 重放旧票：应 401 且撤销整个 family
	if code, _ := doRefresh(tok1); code != http.StatusUnauthorized {
		t.Fatalf("重放旧票应 401, got %d", code)
	}
	// family 撤销后，轮转时签发的新票（tok2）也一并作废——重放即整个设备下线
	if code, _ := doRefresh(tok2); code != http.StatusUnauthorized {
		t.Errorf("family 撤销后新票也应 401, got %d", code)
	}
	// 其他设备不受影响：token_version 不应递增（全局吊销只用于登出/改密/重置）
	after, err := model.GetUserByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TokenVersion != before.TokenVersion {
		t.Errorf("重放不应 bump token_version（只撤 family）：before=%d after=%d",
			before.TokenVersion, after.TokenVersion)
	}
	// 库中断言：该 family 全部会话 revoked_at 非空
	claims1, _ := auth.ParseToken(tok1)
	sess, err := model.FindRefreshSessionByJTI(claims1.ID)
	if err == nil && sess.RevokedAt == nil {
		t.Error("重放的旧 jti 会话应已被吊销")
	}
}

// TestRefreshRotation_LegacyJTIMigration 存量 v21.93 单 jti 用户：
// users.refresh_jti 与票 jti 匹配 → 迁移进会话表后放行；空 jti 的裸票 → 拒绝。
func TestRefreshRotation_LegacyJTIMigration(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)
	setGraceZero(t) // v21.217：既有钉恢复严格一次性语义（宽限行为另钉于 refresh_grace_test.go）
	auth.Init("unit-test-jwt-secret-for-refresh")

	u, err := model.RegisterUser("rr4", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// 场景 A：空 jti 的裸票（无会话、无登记）→ 拒绝（无法验证，安全优先）
	bareTok, _, err := auth.GenerateRefreshToken(u.ID, u.TokenVersion, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := doRefresh(bareTok); code != http.StatusUnauthorized {
		t.Errorf("空 jti 裸票应 401, got %d", code)
	}

	// 场景 B：v21.93 存量登录（users.refresh_jti 已登记）→ 迁移放行
	legacyTok, legacyJTI, err := auth.GenerateRefreshToken(u.ID, u.TokenVersion, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := model.UpdateRefreshJTI(u.ID, legacyJTI); err != nil { // 模拟 v21.93 登录登记
		t.Fatal(err)
	}
	code, newTok := doRefresh(legacyTok)
	if code != http.StatusOK {
		t.Fatalf("存量已登记 jti 的首次换票应迁移放行, got %d", code)
	}
	// 迁移后应建了会话（family = 原 jti），新票可继续轮转
	claims, err := auth.ParseToken(newTok)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := model.FindRefreshSessionByJTI(claims.ID)
	if err != nil || sess == nil {
		t.Fatalf("迁移后新票应有会话记录: %v", err)
	}
	// 迁移用的旧票再换 → 401（已被吊销）
	if code2, _ := doRefresh(legacyTok); code2 != http.StatusUnauthorized {
		t.Errorf("迁移后旧票应 401, got %d", code2)
	}
}

// TestRefreshRotation_PasswordChangeInvalidatesRefresh 改密（全局吊销）后
// 旧 refresh 失效——同时验证 BumpTokenVersion 会清空 jti、撤销全部会话，
// 避免残留 jti 让下一轮换票被误判为重放。
func TestRefreshRotation_PasswordChangeInvalidatesRefresh(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)
	setGraceZero(t) // v21.217：既有钉恢复严格一次性语义（宽限行为另钉于 refresh_grace_test.go）

	u, tok := loginAndGetRefresh(t, "rr5", "Passw0rd!2026")

	if err := model.BumpTokenVersion(u.ID); err != nil {
		t.Fatal(err)
	}
	if got := storedJTI(t, u.ID); got != "" {
		t.Errorf("全局吊销应清空 jti, got %q", got)
	}
	if code, _ := doRefresh(tok); code != http.StatusUnauthorized {
		t.Errorf("改密后旧 refresh 应 401, got %d", code)
	}
}

// loginOnly 仅登录（用户已存在），返回响应体中的 refresh token。
func loginOnly(t *testing.T, username, password string) string {
	t.Helper()
	auth.Init("unit-test-jwt-secret-for-refresh")
	c, w := newNoteContext("POST", "/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	Login(c)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200, got %d body=%s", w.Code, w.Body.String())
	}
	return parseRefreshToken(t, w.Body.String())
}

// TestRefreshRotation_MultiDeviceIndependent 多设备核心语义：
// 同账号多设备各持独立 family，各自轮转互不影响；
// 某设备重放只撤销其自身 family，不误伤其他设备（v21.94 P3-7）。
func TestRefreshRotation_MultiDeviceIndependent(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)
	setGraceZero(t) // v21.217：既有钉恢复严格一次性语义（宽限行为另钉于 refresh_grace_test.go）

	// 注册并首登（设备 A）；再两次登录 = 设备 B、C（各建独立 family）
	_, tokA1 := loginAndGetRefresh(t, "rrmulti", "Passw0rd!2026")
	tokB1 := loginOnly(t, "rrmulti", "Passw0rd!2026")
	tokC1 := loginOnly(t, "rrmulti", "Passw0rd!2026")

	// 三个设备各自轮转都成功
	codeA, tokA2 := doRefresh(tokA1)
	if codeA != http.StatusOK {
		t.Fatalf("设备A轮转应 200, got %d", codeA)
	}
	codeB, tokB2 := doRefresh(tokB1)
	if codeB != http.StatusOK {
		t.Fatalf("设备B轮转应 200, got %d", codeB)
	}
	codeC, _ := doRefresh(tokC1)
	if codeC != http.StatusOK {
		t.Fatalf("设备C轮转应 200, got %d", codeC)
	}

	// 设备 B 重放旧票 → 仅 B 的 family 被撤
	if code, _ := doRefresh(tokB1); code != http.StatusUnauthorized {
		t.Fatalf("设备B重放应 401, got %d", code)
	}
	// B 的新票也一并作废（family 级撤销）
	if code, _ := doRefresh(tokB2); code != http.StatusUnauthorized {
		t.Errorf("B family 撤销后 B 新票应 401, got %d", code)
	}
	// A 完全不受影响：A 的新票仍可继续轮转
	codeA3, _ := doRefresh(tokA2)
	if codeA3 != http.StatusOK {
		t.Errorf("设备A 不应受设备B 重放影响（可继续轮转）, got %d", codeA3)
	}
}

// TestRefreshRotation_ConcurrentSameToken 并发安全回归（v1.0.134 审计 R-1 修复核心）：
// 多个 goroutine 同时持「同一张旧 refresh token」换票，一次性语义要求**恰好 1 次成功**，
// 其余全部 401。修复前 handler 用 5 次独立 DB 调用、且吊销错误 `_ =` 丢弃，
// 并发可全部 200（双花）。现 RotateRefreshSessionTx 以「单事务 + CAS」保证原子性。
// 配合 -race 运行可同时捕获数据竞争。
//
// v21.97（审计上线前建议）：并发量 20 → 50，覆盖更激进的抢用压力。
func TestRefreshRotation_ConcurrentSameToken(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)
	setGraceZero(t) // v21.217：既有钉恢复严格一次性语义（宽限行为另钉于 refresh_grace_test.go）

	_, tok1 := loginAndGetRefresh(t, "rrconc", "Passw0rd!2026")

	const N = 50
	codes := make([]int, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := doRefresh(tok1)
			codes[i] = code
		}(i)
	}
	wg.Wait()

	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("并发持同一旧 token 换票，应仅 1 次成功（一次性语义），实际 %d 次 200: %v", ok, codes)
	}
}
