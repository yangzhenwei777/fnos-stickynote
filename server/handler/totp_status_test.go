package handler

// totp_status_test.go —— 两步验证状态查询端点（v21.151，生产 Bug1 回归钉）。
// 背景：个人中心此前从不加载真实启用态（页面刷新后恒显示「未启用」），
// 用户点击「开启两步验证」被 EnableTOTP 正确拒绝（已启用）却只得笼统「操作无效」。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"stickynote/database"
	"stickynote/model"
)

// TestTOTPStatusEndpoint v21.151：端点返回真实启用态（false → 落库置 1 → true）。
func TestTOTPStatusEndpoint(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("totpstat", "Passw0rd!2026", "q", "a") // userID=1

	type statusResp struct {
		TotpEnabled bool `json:"totp_enabled"`
	}
	call := func() statusResp {
		c, w := newNoteContext("GET", "/api/auth/totp-status", "")
		TOTPStatus(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200，得 %d body=%s", w.Code, w.Body.String())
		}
		var resp statusResp
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if call().TotpEnabled {
		t.Fatal("初始态应为未启用")
	}

	// 启用后（模拟 ConfirmTOTP 落库）→ 端点必须回 true（前端据此显示「已开启」）
	if _, err := database.DB.Exec("UPDATE users SET totp_enabled = 1 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if !call().TotpEnabled {
		t.Fatal("启用后必须回 true（Bug1 回归：显示层与真实态脱节）")
	}
}

// TestEnableTOTPAlreadyEnabledMessage v21.151：已启用时给出可行动文案，不再笼统「操作无效」。
func TestEnableTOTPAlreadyEnabledMessage(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("totpon", "Passw0rd!2026", "q", "a")
	if _, err := database.DB.Exec("UPDATE users SET totp_enabled = 1 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	c, w := newNoteContext("POST", "/api/auth/enable-totp", "")
	EnableTOTP(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("已启用应 400，得 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已启用") {
		t.Fatalf("文案须指明已启用（可行动），得 %s", w.Body.String())
	}
}
