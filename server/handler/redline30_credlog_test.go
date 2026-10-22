package handler

// redline30_credlog_test.go —— 红线 30「明文凭据入日志」的**运行时断言**。
//
// 台账原缺口：静态面已闭环（`check_secrets.py` CI 常驻扫源码），且 auth 路径日志结构上
// 只记 ID/错误（不打印请求体）—— 但**没有任何运行时断言**证明「实际写出去的日志行里
// 不含口令」。静态扫描只能证明「源码里没有硬编码口令」，证明不了「运行期格式化拼接
// 出来的字符串不含口令」。举例：若有人改成
//
//	logger.Info("login attempt: user=%s pwd=%s", req.Username, req.Password)
//
// 源码里没有硬编码口令（静态扫描过得），但日志里就是明文 —— 这正是红线 30 的事故形态。
//
// 本用例的断言方式：把 logger 初始化到临时目录 → 跑**真实登录路径**（成功 / 口令错误 /
// 锁定拒绝 / 请求体畸形）→ 读回 info+error+audit 三个日志文件全文 → 断言：
//   ① 每个用到的口令明文都**不出现**在任一日志文件里；
//   ② 但审计字段（用户名、结果）**确实出现**（否则就是「靠不写日志来通过」，是退化解）；
//   ③ 请求体原文（含口令）整体不得出现。
//
// 注意本文件放在 handler 包（非 logger 包）—— 必须跑真实 handler 才能覆盖格式化路径。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stickynote/logger"
	"stickynote/model"
)

const r30Password = "S3cret-Passw0rd-R30"

func redline30ReadLogs(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读日志目录: %v", err)
	}
	day := time.Now().Format("2006-01-02")
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), day) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读 %s: %v", e.Name(), err)
		}
		sb.WriteString("----- " + e.Name() + " -----\n")
		sb.Write(b)
	}
	if sb.Len() == 0 {
		t.Fatalf("日志目录 %s 内没有当日日志文件（断言会空转）", dir)
	}
	return sb.String()
}

func TestRedline30NoPlaintextCredentialInLogs(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()

	logDir := t.TempDir()
	if err := logger.Init(logDir); err != nil {
		t.Fatalf("logger.Init: %v", err)
	}
	defer logger.Close()

	if _, err := model.RegisterUser("r30user", r30Password, "q", "答案"); err != nil {
		t.Fatalf("注册: %v", err)
	}
	// 干净起见：清掉注册过程可能写的日志只看登录段也不影响，但保留更真实
	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	// ① 成功登录
	c, w := newNoteContext("POST", "/api/auth/login",
		`{"username":"r30user","password":"`+r30Password+`"}`)
	Login(c)
	if w.Code != 200 {
		t.Fatalf("成功登录应 200，实得 %d body=%s", w.Code, w.Body.String())
	}

	// ② 口令错误（走失败审计分支）
	c, w = newNoteContext("POST", "/api/auth/login",
		`{"username":"r30user","password":"`+r30Password+`-WRONG"}`)
	Login(c)
	if w.Code != 401 {
		t.Fatalf("错误口令应 401，实得 %d", w.Code)
	}

	// ③ 连续失败触发锁定 → blocked 分支
	for i := 0; i < 8; i++ {
		c, w = newNoteContext("POST", "/api/auth/login",
			`{"username":"r30user","password":"WRONG-`+r30Password+`"}`)
		Login(c)
	}

	// ④ 畸形请求体（ShouldBindJSON 失败分支）
	c, _ = newNoteContext("POST", "/api/auth/login", `{"username":`)
	Login(c)

	logs := redline30ReadLogs(t, logDir)

	// 决定性断言：所有出现过的口令明文一律不得落盘
	forbidden := []string{
		r30Password,
		r30Password + "-WRONG",
		"WRONG-" + r30Password,
	}
	for _, secret := range forbidden {
		if strings.Contains(logs, secret) {
			t.Fatalf("红线30：明文凭据落进日志（口令 %q）\n---- 日志 ----\n%s", mask(secret), logs)
		}
	}
	// 请求体整体也不得出现（含 "password" 字段连同值）
	for _, frag := range []string{`"password"`, `"password":`} {
		if strings.Contains(logs, frag) {
			t.Fatalf("红线30：日志出现请求体片段 %q（可能连带口令）\n---- 日志 ----\n%s", frag, logs)
		}
	}

	// 反向断言（防「靠不写日志来通过」的退化解）：审计字段必须真实在位
	for _, want := range []string{`"action":"login"`, `"actor":"r30user"`, `"result":"success"`, `"result":"failed"`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("红线30：审计字段 %s 缺失 —— 说明断言是靠「没写日志」通过的（退化解）\n---- 日志 ----\n%s", want, logs)
		}
	}
	// 锁定拒绝分支也必须留痕
	if !strings.Contains(logs, `"result":"blocked"`) {
		t.Fatalf("红线30：锁定拒绝未留审计痕迹\n---- 日志 ----\n%s", logs)
	}
}

// mask 口令打码（避免失败信息里回显凭据本身）。
func mask(s string) string {
	if len(s) <= 4 {
		return "***"
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

// TestRedline30AuditLoggerRejectsCredentialFields —— 结构面补钉：AUDIT 条目的
// 固定字段里**没有**任何承载凭据的字段位（防止将来有人往 AuditEntry 加 password/totp 字段）。
func TestRedline30AuditEntryHasNoCredentialField(t *testing.T) {
	// AuditEntry 是审计日志的唯一结构（logger.go:AuditEntry）；它的 JSON tag 集合即
	// 「允许出现在审计日志里的一切」。逐个核对，出现凭据类字段即失败。
	dir := t.TempDir()
	if err := logger.Init(dir); err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	logger.AuditEvent("login", "u", "1.2.3.4", "success", "")
	b, err := os.ReadFile(filepath.Join(dir, "audit-"+time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatal(err)
	}
	line := string(b)
	for _, banned := range []string{"password", "passwd", "pwd", "secret", "token", "totp", "otp", "pin", "jwt", "refresh"} {
		if strings.Contains(strings.ToLower(line), banned) {
			t.Fatalf("红线30：审计条目含凭据类字段名 %q\n%s", banned, line)
		}
	}
}
