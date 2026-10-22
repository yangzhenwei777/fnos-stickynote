package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"stickynote/auth"
	"stickynote/database"
	"stickynote/model"
)

// ============================================================================
// DisableTOTP 单测（v21.93 P0：R-02 / H-01 审计缺口回归点）
//
// 背景：v21.90 声称「关闭 TOTP 时已启用则再校验 TOTP 码/恢复码」，实际实现把
// 第二因子校验包在 `if req.Code != ""` 内，code 留空即整段跳过——只要当前密码
// 正确就能关掉 MFA，而 2FA 存在的意义正是防「密码已泄露」这一威胁模型。
// 恢复码途径更是从未实现（只有事后 DELETE FROM totp_recovery）。
//
// 本组用例锁定修复后的行为，任一被无意回退都会变红。
// ============================================================================

// enableTOTPForTest 直接落库启用 TOTP 并写入一枚明文恢复码，返回明文密钥与恢复码。
func enableTOTPForTest(t *testing.T, userID int64, account string) (secret, recoveryCode string) {
	t.Helper()
	auth.Init("unit-test-jwt-secret-for-totp") // 供 encryptTOTP 派生密钥（无返回值）
	key, err := totp.Generate(totp.GenerateOpts{Issuer: totpIssuer, AccountName: account})
	if err != nil {
		t.Fatalf("generate totp key: %v", err)
	}
	enc, err := encryptTOTP(key.Secret())
	if err != nil {
		t.Fatalf("encrypt totp secret: %v", err)
	}
	if _, err := database.DB.Exec(
		"UPDATE users SET totp_secret = ?, totp_enabled = 1 WHERE id = ?", enc, userID); err != nil {
		t.Fatalf("enable totp: %v", err)
	}
	recoveryCode = "rc1234567"
	hash, err := bcrypt.GenerateFromPassword([]byte(recoveryCode), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash recovery code: %v", err)
	}
	if _, err := database.DB.Exec(
		"INSERT INTO totp_recovery (user_id, code_hash) VALUES (?, ?)", userID, string(hash)); err != nil {
		t.Fatalf("insert recovery code: %v", err)
	}
	return key.Secret(), recoveryCode
}

// newDisableTOTPContext 构造带 userID / username 的 DisableTOTP 请求上下文
// （newNoteContext 默认只设 userID，而 handler 内部会取 username 做身份校验）。
func newDisableTOTPContext(body string, userID int64, username string) (*gin.Context, *httptest.ResponseRecorder) {
	c, w := newNoteContext("POST", "/api/auth/disable-totp", body)
	c.Set("userID", userID)
	c.Set("username", username)
	return c, w
}

// isTotpEnabled 读取用户当前 TOTP 启用状态。
func isTotpEnabled(t *testing.T, userID int64) bool {
	t.Helper()
	var enabled int
	if err := database.DB.QueryRow("SELECT totp_enabled FROM users WHERE id = ?", userID).Scan(&enabled); err != nil {
		t.Fatalf("query totp_enabled: %v", err)
	}
	return enabled == 1
}

// TestDisableTOTP_CodeRequired code 为空必须被拒——v21.90 缺口的核心回归点：
// 此前不带 code（或空串）会整段跳过第二因子校验，仅凭密码即可关闭 MFA。
func TestDisableTOTP_CodeRequired(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	u, err := model.RegisterUser("duser", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	enableTOTPForTest(t, u.ID, "duser")

	// 仅传当前密码：必须拒绝
	c, w := newDisableTOTPContext(`{"current_password":"Passw0rd!2026"}`, u.ID, "duser")
	DisableTOTP(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺少 code 应 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "验证码或恢复码") {
		t.Errorf("应提示输入验证码或恢复码: %s", w.Body.String())
	}
	if !isTotpEnabled(t, u.ID) {
		t.Error("被拒绝后 TOTP 必须仍处于启用状态")
	}

	// 显式传空串：同样必须拒绝（不能退化成「跳过校验」）
	c2, w2 := newDisableTOTPContext(`{"current_password":"Passw0rd!2026","code":"   "}`, u.ID, "duser")
	DisableTOTP(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("空串 code 应 400, got %d body=%s", w2.Code, w2.Body.String())
	}
}

// TestDisableTOTP_ValidTOTPCode 正确 TOTP 码 → 关闭成功，恢复码一并清理。
func TestDisableTOTP_ValidTOTPCode(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	u, err := model.RegisterUser("duser2", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := enableTOTPForTest(t, u.ID, "duser2")

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	c, w := newDisableTOTPContext(
		`{"current_password":"Passw0rd!2026","code":"`+code+`"}`, u.ID, "duser2")
	DisableTOTP(c)
	if w.Code != http.StatusOK {
		t.Fatalf("正确 TOTP 应 200, got %d body=%s", w.Code, w.Body.String())
	}
	if isTotpEnabled(t, u.ID) {
		t.Error("成功后 totp_enabled 应置 0")
	}
	// 关闭后恢复码应被清理（防止残留可继续用于登录）
	var n int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM totp_recovery WHERE user_id = ?", u.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("关闭后恢复码应清空, got %d", n)
	}
}

// TestDisableTOTP_ValidRecoveryCode 正确恢复码 → 关闭成功（changelog 承诺的
// 「恢复码」途径在 v21.90 从未实现，本例锁定其存在）。
func TestDisableTOTP_ValidRecoveryCode(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	u, err := model.RegisterUser("duser3", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, recovery := enableTOTPForTest(t, u.ID, "duser3")

	c, w := newDisableTOTPContext(
		`{"current_password":"Passw0rd!2026","code":"`+recovery+`"}`, u.ID, "duser3")
	DisableTOTP(c)
	if w.Code != http.StatusOK {
		t.Fatalf("正确恢复码应 200, got %d body=%s", w.Code, w.Body.String())
	}
	if isTotpEnabled(t, u.ID) {
		t.Error("成功后 totp_enabled 应置 0")
	}
}

// TestDisableTOTP_InvalidCodeCountsToGuard 错误码 → 400 且计入账号维度防爆破。
func TestDisableTOTP_InvalidCodeCountsToGuard(t *testing.T) {
	t.Setenv("LOGIN_MAX_FAILS", "3")
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	u, err := model.RegisterUser("duser4", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	enableTOTPForTest(t, u.ID, "duser4")

	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()

	c, w := newDisableTOTPContext(`{"current_password":"Passw0rd!2026","code":"000000"}`, u.ID, "duser4")
	DisableTOTP(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("错误码应 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !isTotpEnabled(t, u.ID) {
		t.Error("失败后 TOTP 必须仍启用")
	}

	guard.mu.Lock()
	rec, ok := guard.fails["account:duser4"]
	guard.mu.Unlock()
	if !ok {
		t.Fatal("失败应计入账号维度 guard（key=account:duser4）")
	}
	if rec == nil || rec.count != 1 {
		t.Errorf("账号维度失败计数应为 1, got %+v", rec)
	}
}

// TestDisableTOTP_LockAfterThreshold 连续错误达到阈值 → 429 锁定。
func TestDisableTOTP_LockAfterThreshold(t *testing.T) {
	t.Setenv("LOGIN_MAX_FAILS", "3")
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	u, err := model.RegisterUser("duser5", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	enableTOTPForTest(t, u.ID, "duser5")

	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()

	body := `{"current_password":"Passw0rd!2026","code":"000000"}`
	for i := 0; i < 3; i++ {
		c, w := newDisableTOTPContext(body, u.ID, "duser5")
		DisableTOTP(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次错误码应 400, got %d", i+1, w.Code)
		}
	}
	// 达到阈值后第 4 次应被锁定（429），而不是继续校验
	c, w := newDisableTOTPContext(body, u.ID, "duser5")
	DisableTOTP(c)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("达到阈值后应 429, got %d body=%s", w.Code, w.Body.String())
	}
	if !isTotpEnabled(t, u.ID) {
		t.Error("锁定期间 TOTP 必须仍启用")
	}
}

// TestDisableTOTP_NotEnabled 未启用 TOTP 的用户请求关闭 → 400。
func TestDisableTOTP_NotEnabled(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	u, err := model.RegisterUser("duser6", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, w := newDisableTOTPContext(`{"current_password":"Passw0rd!2026","code":"123456"}`, u.ID, "duser6")
	DisableTOTP(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未启用应 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "未开启") {
		t.Errorf("应提示两步验证未开启: %s", w.Body.String())
	}
}

// TestDisableTOTP_DecryptFailClosed 密钥无法解密 → fail-closed（500 拒绝），
// 不得像 v21.90 那样因 `serr == nil &&` 短路而放行关闭。
func TestDisableTOTP_DecryptFailClosed(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	resetGuard(t)

	u, err := model.RegisterUser("duser7", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	enableTOTPForTest(t, u.ID, "duser7")
	// 篡改密文，使解密必然失败
	if _, err := database.DB.Exec(
		"UPDATE users SET totp_secret = 'not-a-valid-ciphertext' WHERE id = ?", u.ID); err != nil {
		t.Fatal(err)
	}

	c, w := newDisableTOTPContext(`{"current_password":"Passw0rd!2026","code":"123456"}`, u.ID, "duser7")
	DisableTOTP(c)
	if w.Code == http.StatusOK {
		t.Fatal("密钥解密失败时必须拒绝关闭（fail-closed），不得返回 200")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("解密失败应 500, got %d body=%s", w.Code, w.Body.String())
	}
	if !isTotpEnabled(t, u.ID) {
		t.Error("解密失败后 TOTP 必须仍启用（不得放行关闭）")
	}
}
