//go:build windows

package main

// 批次 4 · 刀 3 Windows 集成：PIN 封套全链 + ensureAccess 换票挂工 + 吊销降级。
// 走真实 localauth/dpapi 栈；refresh 端点用 httptest 假服务端（形状 = 真 handler 契约）。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stickynote-desktop/localstore"
	"stickynote-desktop/synclocal"
	"stickynote/database"

	"stickynote-desktop/localauth"
)

func bootWithLogin(t *testing.T) (*LocalService, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := bootLocalstore(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		setWorker(nil)
		database.GetDB().Close()
	})
	ls := NewLocalService(dir)
	if _, err := localauth.SignIn(database.DB, "http://server.invalid/", "alice",
		func() (string, string, error) { return "A0", "R0", nil }); err != nil {
		t.Fatal(err)
	}
	return ls, dir
}

func TestPINEnvelopeUnlockChain(t *testing.T) {
	ls, _ := bootWithLogin(t)
	if localauth.PINEnrolled(database.DB) {
		t.Fatal("PIN must start unenrolled")
	}
	if err := ls.SetUnlockPIN("123456"); err != nil {
		t.Fatal(err)
	}
	if !localauth.PINEnrolled(database.DB) {
		t.Fatal("PINEnrolled must flip")
	}
	ok, err := ls.UnlockLocal("wrong-pin")
	if ok || err != localauth.ErrWrongPIN {
		t.Fatalf("wrong pin: got (%v,%v) want (false,ErrWrongPIN)", ok, err)
	}
	ok, err = ls.UnlockLocal("123456")
	if !ok || err != nil {
		t.Fatalf("right pin: (%v,%v)", ok, err)
	}
	if !ls.sessionRestored || len(ls.unlockedSecret) != 32 {
		t.Fatalf("unlock must populate session state: %+v", ls)
	}
	// 封套与 DPAPI 层解出的 secret 必须一致。
	sess, _, err := localauth.OpenSession(database.DB)
	if err != nil || string(sess.DeviceSecret) != string(ls.unlockedSecret) {
		t.Fatalf("envelope secret mismatch: %v", err)
	}
	// 解除需正确 PIN。
	if _, err := ls.UnlockLocal("123456"); err != nil {
		t.Fatal(err)
	}
	if err := ls.RemoveUnlockPIN("bad-pin"); err != localauth.ErrWrongPIN {
		t.Fatalf("remove with bad pin must fail: %v", err)
	}
	if err := ls.RemoveUnlockPIN("123456"); err != nil {
		t.Fatal(err)
	}
	if localauth.PINEnrolled(database.DB) {
		t.Fatal("PIN must be removed")
	}
}

func TestUnlockLockout(t *testing.T) {
	ls, _ := bootWithLogin(t)
	if err := ls.SetUnlockPIN("abcdef"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(localauth.ResetUnlockFailures)
	for i := 0; i < localauth.MaxUnlockFailures; i++ {
		if _, err := ls.UnlockLocal("nope!!"); err != localauth.ErrWrongPIN {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := ls.UnlockLocal("abcdef"); err != localauth.ErrLocked {
		t.Fatalf("must be locked after max failures, got %v", err)
	}
	localauth.ResetUnlockFailures() // 在线动作（登录成功路径）复位
	if ok, err := ls.UnlockLocal("abcdef"); !ok || err != nil {
		t.Fatalf("after reset must unlock: (%v,%v)", ok, err)
	}
}

// fakeRefreshServer 形状对齐 handler.Refresh：200=轮转双票；401=吊销。
func fakeRefreshServer(t *testing.T, newAccess, newRefresh string, revoke bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/refresh" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if revoke {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "会话已失效，请重新登录"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": newRefresh})
	}))
}

func TestEnsureAccessMountsWorker(t *testing.T) {
	ls, dir := bootWithLogin(t)
	srv := fakeRefreshServer(t, "A1", "R1", false)
	defer srv.Close()
	if _, err := database.DB.Exec(`UPDATE client_profile SET server_url=? WHERE id=1`, srv.URL); err != nil {
		t.Fatal(err)
	}
	setWorker(nil)
	ok, err := ensureAccess(filepath.Join(dir, "uploads"))
	if err != nil || !ok {
		t.Fatalf("ensureAccess: (%v,%v)", ok, err)
	}
	if workerPtr() == nil {
		t.Fatal("worker must be mounted")
	}
	if !ls.sessionRestored || ls.deviceID == "" {
		t.Fatalf("service state must reflect restore: %+v", ls)
	}
	// 轮转票必须即存（一次一换）。
	p, err := localauth.Load(database.DB)
	if err != nil {
		t.Fatal(err)
	}
	_, rf, err := localauth.OpenSession(database.DB)
	if err != nil || rf != "R1" {
		t.Fatalf("rotated refresh must be sealed+readable: %q %v", rf, err)
	}
	_ = p
}

// ⭐ v21.213 **推翻 v21.203 的旧决策**：AUTH_REVOKED **不再清封存票**。
//
// 旧行为（v21.203，本钉原名 TestEnsureAccessRevokedDropsRefreshKeepsSecret）：
// 服务端 401 ⇒ DropRefreshSealed（清 refresh、留 device_secret）。
// 当时的理由是「服务端权威拒绝，票已死，留着无意义」。
//
// v21.213 推翻理由（211b 实机铁证，`日志 12:10:04 请求已取消` 紧接
// `12:11:25 AUTH_REVOKED`，且用户「关掉再打开登录失效 / 反复重登仍失败」）：
//
//	① refresh 是**一次一换**：`RefreshCtx` 的请求一旦抵达服务端，服务端**已轮转并
//	   吊销旧票**；若此时客户端因 ctx 取消丢弃响应，本地那张旧票**已经作废**。
//	② 下次启动仍拿作废票换票 ⇒ **必然 401** ⇒ 走进本分支。
//	③ 而旧行为在此**把最后一张也清掉** ⇒ 本地彻底回到「从未登录」，
//	   用户只能重登；重登后若再次撞上取消窗口（关机/换代时换票）⇒ **死循环**。
//
// 结论：一次 AUTH_REVOKED **不足以证明**本地封存票已死——它极可能是
// 「上次换票被取消」的下游后果，而那张票本可修复。清掉它把「一次可恢复的降级」
// 变成「本地凭据归零」，代价远大于收益（保留的代价只是下次多一次 401 往返，有界无副作用）。
func TestEnsureAccessRevokedKeepsSealedButFlagsRelogin(t *testing.T) {
	_, dir := bootWithLogin(t)
	srv := fakeRefreshServer(t, "", "", true)
	defer srv.Close()
	if _, err := database.DB.Exec(`UPDATE client_profile SET server_url=? WHERE id=1`, srv.URL); err != nil {
		t.Fatal(err)
	}
	setWorker(nil)
	if ok, err := ensureAccess(filepath.Join(dir, "uploads")); ok || err == nil {
		t.Fatalf("revoked must fail: (%v,%v)", ok, err)
	}
	p, err := localauth.Load(database.DB)
	if err != nil {
		t.Fatal(err)
	}
	// ⭐ v21.213：封存票**必须保留**（本地无权判定其死活，服务端才是权威）。
	if p.RefreshSealed == nil {
		t.Fatal("revoked 后不得清封存 refresh：一次 401 可能是「上次换票被取消」的下游后果，" +
			"清掉会把一次可恢复的降级变成凭据归零 ⇒ 用户陷入反复重登死循环")
	}
	if p.SecretSealed == nil {
		t.Fatal("device_secret must survive for re-login identity reuse")
	}
	// 必须把 UI 明确引导到「需要重新登录」（sessionRestored=false 即该语义）。
	if lsInstance != nil && lsInstance.sessionRestored {
		t.Fatal("吊销后应置 sessionRestored=false，让 UI 显示「需要重新登录」而非「已登录」")
	}
}

// 走查实锤缺陷回归：断网（网络类错误）时档案可开 → 仍挂 worker，离线编辑必须入队记账。
func TestEnsureAccessNetworkFailureMountsWorker(t *testing.T) {
	ls, dir := bootWithLogin(t)
	setWorker(nil)
	if _, err := database.DB.Exec(`UPDATE client_profile SET server_url='http://127.0.0.1:9/' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	ok, err := ensureAccess(filepath.Join(dir, "uploads"))
	if ok || err == nil {
		t.Fatalf("offline must not report ready: (%v,%v)", ok, err)
	}
	if workerPtr() == nil {
		t.Fatal("network-failure path must still mount worker for outbox accounting")
	}
	if _, err := ls.CreateNote("offline-edit", "body", "", nil); err != nil {
		t.Fatal(err)
	}
	st, err := synclocal.Status(database.DB)
	if err != nil {
		t.Fatal(err)
	}
	if st["pending_ops"].(int) < 1 {
		t.Fatalf("offline edit must enqueue: %+v", st)
	}
	setWorker(nil)
}

// 走查时序洞回归：restoreDeviceSession 启动即挂空票工（同次运行编辑不漏账）。
func TestRestoreMountsWorkerForAccounting(t *testing.T) {
	ls, dir := bootWithLogin(t)
	setWorker(nil)
	if err := ls.restoreDeviceSession(dir); err != nil {
		t.Fatal(err)
	}
	if workerPtr() == nil {
		t.Fatal("restorable profile must mount worker at boot (empty token ok)")
	}
	if workerHasToken(workerPtr()) {
		t.Fatal("boot mount is token-less; exchange happens lazily via ensureAccess")
	}
	if _, err := ls.CreateNote("boot-edit", "b", "", nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE status='PENDING'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("edit right after boot restore must enqueue, got %d", n)
	}
	// 换票成功后为在位工补票（不重复挂载）。
	srv := fakeRefreshServer(t, "A9", "R9", false)
	defer srv.Close()
	if _, err := database.DB.Exec(`UPDATE client_profile SET server_url=? WHERE id=1`, srv.URL); err != nil {
		t.Fatal(err)
	}
	w1 := workerPtr()
	if ok, err := ensureAccess(filepath.Join(dir, "uploads")); err != nil || !ok {
		t.Fatalf("ensureAccess with server up: (%v,%v)", ok, err)
	}
	if workerPtr() != w1 {
		t.Fatal("must top-up existing worker token, not remount")
	}
	if !workerHasToken(workerPtr()) {
		t.Fatal("token must be injected after exchange")
	}
	setWorker(nil)
}

// TestAuthStatusNoRefreshNotLoggedIn v21.152 回归（生产实锤）：封存 refresh 缺失
// （主动登出残留 / 已被吊销）时不得再报"已登录"——旧口径只看 server_url，
// 造出"僵尸已登录"：卡片显示已连接，但同步/重建同步全失败（NO_REFRESH），用户无从下手。
func TestAuthStatusNoRefreshNotLoggedIn(t *testing.T) {
	ls, _ := bootWithLogin(t)
	if st, err := ls.AuthStatus(); err != nil || st["logged_in"] != true {
		t.Fatalf("有封存票的档案应报已登录: %v %v", st, err)
	}
	// 模拟服务端吊销后的落盘形态：清 refresh 封存件，保留 server_url/device_secret
	if _, err := database.DB.Exec(`UPDATE client_profile SET refresh_sealed=NULL WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	setWorker(nil)
	st, err := ls.AuthStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st["logged_in"] == true {
		t.Fatalf("无封存票不得报已登录（僵尸已登录回归）: %+v", st)
	}
	if st["relogin_required"] != true {
		t.Fatalf("无封存票应标记 relogin_required: %+v", st)
	}
	if st["session_recoverable"] != false {
		t.Fatalf("session_recoverable 必须为 false: %+v", st)
	}
	if st["server_url"] == "" || st["server_url"] == nil {
		t.Fatalf("断线重登仍需展示服务器地址: %+v", st)
	}
}

// TestEnsureAccessNoRefreshNeedsRelogin v21.152：封存票缺失时错误文案必须可行动
// （旧行为抛裸 "NO_REFRESH"，生产 UI 原样弹出该字样）。
func TestEnsureAccessNoRefreshNeedsRelogin(t *testing.T) {
	_, dir := bootWithLogin(t)
	if _, err := database.DB.Exec(`UPDATE client_profile SET refresh_sealed=NULL WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	setWorker(nil)
	ok, err := ensureAccess(filepath.Join(dir, "uploads"))
	if ok || err == nil {
		t.Fatalf("无票必须失败: (%v,%v)", ok, err)
	}
	if !errors.Is(err, ErrNeedRelogin) {
		t.Fatalf("want ErrNeedRelogin, got %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "重新登录") {
		t.Fatalf("文案须可行动（含重新登录）: %q", got)
	}
	// 防回归：不得把档案里的 device_secret 清掉（重登沿用设备身份）
	p, lerr := localauth.Load(database.DB)
	if lerr != nil || p.SecretSealed == nil {
		t.Fatalf("device_secret 必须保留: %v %v", p, lerr)
	}
}

// TestHealIdentityAcrossGenerations v21.152：代际切换后身份缺席 → 启动自愈从上一代找回。
// 生产实锤场景：快照激活切代后新代无 client_profile（carry 未落），用户侧=「账号与设备」
// 显示未登录 + 同步全失败（NO_REFRESH），无任何可操作出路。
func TestHealIdentityAcrossGenerations(t *testing.T) {
	ls, dir := bootWithLogin(t) // g-000001（active）内含身份
	_ = ls
	if p, err := localauth.Load(database.DB); err != nil || p.ServerURL == "" {
		t.Fatalf("precondition: 身份应在 g-000001: %v %v", p, err)
	}
	// 造一个"新代"（无 desktop 专属表），并让它成为当代库（模拟切代后身份未随迁）
	gen2dir := filepath.Join(dir, "generations", "g-000002")
	if err := os.MkdirAll(gen2dir, 0700); err != nil {
		t.Fatal(err)
	}
	gen2, err := localstore.OpenGenDB(filepath.Join(gen2dir, "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gen2.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatal(err)
	}
	old := database.GetDB()
	database.SetDB(gen2)
	t.Cleanup(func() { gen2.Close(); database.SetDB(old) })
	if _, err := localauth.Load(database.DB); !errors.Is(err, localauth.ErrNoProfile) {
		t.Fatalf("precondition: 新代应无身份行, got %v", err)
	}

	healIdentityAcrossGenerations(dir)

	p, err := localauth.Load(database.DB)
	if err != nil || p.ServerURL == "" || p.Username != "alice" {
		t.Fatalf("自愈必须把上一代身份迁回当代: %+v %v", p, err)
	}
	if len(p.RefreshSealed) == 0 {
		t.Fatal("refresh 封存件必须随迁（否则会话不可恢复）")
	}
	// 幂等：再跑一次不得覆盖/报错
	healIdentityAcrossGenerations(dir)
	if p2, err := localauth.Load(database.DB); err != nil || p2.ServerURL != p.ServerURL {
		t.Fatalf("自愈必须幂等: %+v %v", p2, err)
	}
}
