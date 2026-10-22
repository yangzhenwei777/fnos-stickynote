//go:build windows

package main

// link_integration_windows_test.go —— A2 L2 链路集成用例（2026-09-23，第一轮门禁补全）。
//
// 覆盖三条此前只有"半链"覆盖的链路（auth 半链在 auth_lifecycle_windows_test.go，
// 引擎×真服务端半链在 synclocal/integration_test.go，本文件缝合 desktop 包内的全链）：
//   ① 冷启动空票 → ensureAccess 惰性换票 → RunOnce 全链通（182 类死锁的链路级回归）
//   ② RemoteLogin → 带票挂工（mountWorker + wakeScheduler 接线）→ 首轮同步跑通
//   ③ 在位工 access 丢失 → ensureAccess 自愈（补票不重挂）→ 同步恢复
//
// 服务端 = httptest 假面，但**形状逐字段对齐真 handler 契约**（syncspec/StateResp/PullResp）。
// 为什么不用真 syncsrv handlers：server/sync handlers 与桌面 localauth 共用全局 database.DB
// （无句柄注入口），测试进程内两者互斥 —— 这是登记在案的测试基建缺口，补 SetDB 注入后
// 本文件应升级为真服务端收敛断言（台账《桌面E2E门禁-缺口与根因-20260923.md》）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"stickynote-desktop/localauth"
	"stickynote/database"
)

// newLinkServer auth 票轮换 + sync 契约假面。reject 控制开关置真时 sync 面 401（模拟票失效）。
func newLinkServer(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var refreshes atomic.Int32
	var reject atomic.Bool
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if reject.Load() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"会话已失效，请重新登录"}`))
				return
			}
			h(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		n := refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "AL" + strconv.Itoa(int(n)), "refresh_token": "RL" + strconv.Itoa(int(n)),
		})
	})
	mux.HandleFunc("/api/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		n := refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "AL" + strconv.Itoa(int(n)), "refresh_token": "RL" + strconv.Itoa(int(n)),
		})
	})
	mux.HandleFunc("/api/sync/state", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocol_version":1,"server_instance_id":"i-test","server_epoch":1,
			"current_change_seq":0,"min_available_change_seq":0}`))
	}))
	mux.HandleFunc("/api/sync/pull", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"changes":[],"next_cursor":0,"watermark":0,"has_more":false,
			"server_epoch":1,"min_available_change_seq":0}`))
	}))
	mux.HandleFunc("/api/sync/push", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &reject
}

// 用例 ①：冷启动空票 → 惰性换票 → RunOnce 全链通。
func TestColdStartEmptyTicketExchangesAndRunsRound(t *testing.T) {
	ts, _ := newLinkServer(t)
	ls, dir := bootWithLogin(t)
	if _, err := database.DB.Exec(`UPDATE client_profile SET server_url=? WHERE id=1`, ts.URL); err != nil {
		t.Fatal(err)
	}

	// 冷启动恢复：空票挂工（access 只存内存，重启即失）。
	setWorker(nil)
	if err := ls.restoreDeviceSession(dir); err != nil {
		t.Fatal(err)
	}
	if workerPtr() == nil || workerHasToken(workerPtr()) {
		t.Fatal("冷启动恢复应为空票挂工（§87）")
	}

	// 空票期编辑必须已入账（§25：漏账 = 联网后无账可推）。
	if _, err := ls.CreateNote("boot-edit", "body", "", nil); err != nil {
		t.Fatal(err)
	}

	// 惰性换票（与 scheduledRound 同一入口）→ 带票。
	if ok, err := ensureAccess(ls.uploadDir); err != nil || !ok {
		t.Fatalf("ensureAccess: (%v,%v)", ok, err)
	}
	if !workerHasToken(workerPtr()) {
		t.Fatal("换票后应带票")
	}
	// 轮转票已封存（一次一换）：封存的应是最新 RL。
	_, rf, err := localauth.OpenSession(database.DB)
	if err != nil || rf == "R0" {
		t.Fatalf("封存票应已轮转，得 %q %v", rf, err)
	}

	// 一轮全链：State + Push(outbox 记账) + Pull 均走通。
	if _, err := workerPtr().RunOnce(context.Background()); err != nil {
		t.Fatalf("空票换票后的首轮 RunOnce 应全链通: %v", err)
	}
}

// 用例 ②：RemoteLogin → mountWorker 带票挂工 → 首轮同步跑通。
func TestLoginMountsTokenedWorkerAndFirstRoundRuns(t *testing.T) {
	ts, _ := newLinkServer(t)
	ls, _ := bootWithLogin(t) // 仅备本地库与 LocalService（模拟未登录前状态）

	setWorker(nil)
	res, err := ls.RemoteLogin(ts.URL, "alice", "pw-123456")
	if err != nil {
		t.Fatalf("RemoteLogin: %v", err)
	}
	// mountWorker 正规入口的三个可观测结果：带票工在位、deviceID 落位、回传账号名。
	if workerPtr() == nil || !workerHasToken(workerPtr()) {
		t.Fatal("登录后应挂带票工（v21.140 修复：不得绕过 mountWorker）")
	}
	if ls.deviceID == "" {
		t.Fatal("登录后 deviceID 必须落位")
	}
	if res["username"] != "alice" {
		t.Fatalf("登录回传 username: %v", res["username"])
	}
	// 首轮同步（调度器在单测环境未启动 → 显式跑一轮等价验证链路）。
	if _, err := workerPtr().RunOnce(context.Background()); err != nil {
		t.Fatalf("登录后首轮 RunOnce: %v", err)
	}
}

// 用例 ③：在位票被服务端 401 拒绝 → 降级卸工（封存件保留）→ 重换票重挂 → 同步恢复。
// 链路形状与调度器 OnRound 的真实自愈路径一致（demoteToRelogin 只卸工不清封存）。
func TestAccessTokenLossSelfHealsInPlace(t *testing.T) {
	ts, reject := newLinkServer(t)
	ls, _ := bootWithLogin(t)
	if _, err := database.DB.Exec(`UPDATE client_profile SET server_url=? WHERE id=1`, ts.URL); err != nil {
		t.Fatal(err)
	}
	// 先正常换到票。
	if ok, err := ensureAccess(ls.uploadDir); err != nil || !ok {
		t.Fatalf("initial exchange: (%v,%v)", ok, err)
	}
	if !workerHasToken(workerPtr()) {
		t.Fatal("前置：应带票")
	}
	// 服务端开始拒绝（票失效语义）。
	reject.Store(true)
	_, runErr := workerPtr().RunOnce(context.Background())
	if runErr == nil {
		t.Fatal("票失效时 RunOnce 应报错")
	}
	// 调度器 OnRound 的降级口径：清票留工、不清封存件。
	//
	// ⭐ v21.203 语义修正：旧断言是「降级后应卸工（syncWorker == nil）」。但
	// `allowScheduledRound` 的第一道门就是 `if syncWorker == nil { return false }`
	// ⇒ 卸工 = **本进程内调度器永久停摆**，自动同步再也醒不过来，只能用户手点
	// 【立即同步】复活（那条路不经过 allowScheduledRound）。一次偶发 401 就让
	// 自动同步永久停摆，是审计定罪的最隐蔽一条。现语义：清票、留工、敲调度器。
	ls.demoteToRelogin("test-401")
	if workerPtr() == nil {
		t.Fatal("降级后 worker 必须保留在位（置 nil 会关闭调度门，自动同步永久停摆）")
	}
	if workerHasToken(workerPtr()) {
		t.Fatal("降级后内存票必须清空（否则等于没降级）")
	}
	// 服务端恢复 → 下一轮 ensureAccess 重换票重挂，同步链复活。
	reject.Store(false)
	if ok, err := ensureAccess(ls.uploadDir); err != nil || !ok {
		t.Fatalf("self-heal: (%v,%v)", ok, err)
	}
	if workerPtr() == nil || !workerHasToken(workerPtr()) {
		t.Fatal("自愈后应重挂带票工")
	}
	_, rf, err := localauth.OpenSession(database.DB)
	if err != nil || rf == "" {
		t.Fatalf("自愈换票后封存件必须滚动: %q %v", rf, err)
	}
	if _, err := workerPtr().RunOnce(context.Background()); err != nil {
		t.Fatalf("自愈后 RunOnce: %v", err)
	}
}

// 用例 ④（v21.151，生产 Bug2 回归）：2FA 账号的桌面登录全链——
// 第一步必须透出 {totp_required, totp_token}（旧缺陷：静默空票 → 下游 401 → 误报「会话已失效」）；
// 第二步验证码换票 → 带票挂工 → 首轮同步跑通；错误验证码文案原样透传且不挂工。
func TestRemoteLoginTOTPFlow(t *testing.T) {
	var codeOK atomic.Bool
	codeOK.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totp_required":true,"totp_token":"T-1","user":{"id":1,"username":"alice"}}`))
	})
	mux.HandleFunc("/api/auth/verify-totp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !codeOK.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"验证码错误"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"AV","refresh_token":"RV","user":{"id":1,"username":"alice"}}`))
	})
	mux.HandleFunc("/api/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"AV2","refresh_token":"RV2"}`))
	})
	mux.HandleFunc("/api/sync/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocol_version":1,"server_instance_id":"i-test","server_epoch":1,"current_change_seq":0,"min_available_change_seq":0}`))
	})
	mux.HandleFunc("/api/sync/pull", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"changes":[],"next_cursor":0,"watermark":0,"has_more":false,"server_epoch":1,"min_available_change_seq":0}`))
	})
	mux.HandleFunc("/api/sync/push", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	ls, _ := bootWithLogin(t)
	setWorker(nil)

	// 第一步：不得报错（旧缺陷在此误报「会话已失效」），须透出临时票；未过二因子不得挂工。
	res, err := ls.RemoteLogin(ts.URL, "alice", "pw-123456")
	if err != nil {
		t.Fatalf("2FA 账号第一步不得报错: %v", err)
	}
	if res["totp_required"] != true || res["totp_token"] != "T-1" {
		t.Fatalf("第一步必须透出 {totp_required, totp_token}，得 %+v", res)
	}
	if workerPtr() != nil {
		t.Fatal("未过二因子不得挂工")
	}

	// 第二步（错码）：服务端文案原样透传，不挂工。
	codeOK.Store(false)
	if _, err := ls.RemoteLoginTOTP(ts.URL, "alice", "T-1", "000000"); err == nil || err.Error() != "验证码错误" {
		t.Fatalf("验证码错误必须原样透传（不含传输层噪声），得 %v", err)
	}
	if workerPtr() != nil {
		t.Fatal("验证失败不得挂工")
	}

	// 第三步（对码）：换票 → 带票挂工 → 首轮同步全链通。
	codeOK.Store(true)
	res2, err := ls.RemoteLoginTOTP(ts.URL, "alice", "T-1", "123456")
	if err != nil {
		t.Fatalf("RemoteLoginTOTP: %v", err)
	}
	if res2["username"] != "alice" || res2["server_url"] != ts.URL {
		t.Fatalf("登录结果形状异常: %+v", res2)
	}
	if !workerHasToken(workerPtr()) {
		t.Fatal("通过二因子后必须带票挂工")
	}
	if _, err := workerPtr().RunOnce(context.Background()); err != nil {
		t.Fatalf("二因子登录后首轮 RunOnce 应全链通: %v", err)
	}
}
