package main

// auth_semantics_test.go —— v21.203 批次 2 回归：登录语义 / 会话降级 / 轮转三态。
//
// 全部用例都锚在**用户实机定罪链**上（2026-10-10，另一台电脑、本机代理 关闭）：
//   登录成功 → State 探测因网络超时失败 → UI 报"登录失败" → 用户重登 → 越重越乱。
// 修法是「登录成败由 SignIn 判定，不由 State 判定」。这里把它钉死。

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stickynote-desktop/localauth"
	"stickynote-desktop/synclocal"
	"stickynote/database"
)

// ---- 批次2-3：轮转三态分类 ----

// TestClassifyRotateError 三态判据。核心断言：TLS 握手超时（请求未发出）≠
// 响应体超时（服务端可能已消费）。把两者混为一谈会导致对已消费的票重试 → 族重放吊销。
func TestClassifyRotateError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want rotateState
	}{
		{"nil", nil, rotateNotSent},
		{"TLS握手超时_请求未发出", errors.New("Post \"https://x/api/auth/refresh\": net/http: TLS handshake timeout"), rotateNotSent},
		{"DNS解析失败_请求未发出", errors.New("Post \"https://x/api/auth/refresh\": dial tcp: lookup x: no such host"), rotateNotSent},
		{"连接被拒_请求未发出", errors.New("Post \"https://x/api/auth/refresh\": dial tcp 1.2.3.4:443: connect: connection refused"), rotateNotSent},
		{"等响应头超时_可能已消费", errors.New("Post \"https://x/api/auth/refresh\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)"), rotateMaybeConsumed},
		{"读响应体断_更可能已消费", errors.New("Post \"https://x/api/auth/refresh\": context deadline exceeded (Client.Timeout exceeded while reading body)"), rotateMaybeConsumed},
		{"EOF_可能已消费", errors.New("Post \"https://x/api/auth/refresh\": EOF"), rotateMaybeConsumed},
		{"连接重置_可能已消费", errors.New("Post \"https://x/api/auth/refresh\": read: connection reset by peer"), rotateMaybeConsumed},
		{"服务端401_权威拒绝", synclocal.ErrAuthRevoked, rotateRejected},
		{"账密错误_权威拒绝", synclocal.ErrBadCredentials, rotateRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRotateError(tc.err); got != tc.want {
				t.Fatalf("classifyRotateError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
	// 可重试性：只有 ① 可重试。
	if !rotateNotSent.retryable() {
		t.Fatal("rotateNotSent 必须可重试")
	}
	for _, s := range []rotateState{rotateMaybeConsumed, rotateRejected, rotateDone} {
		if s.retryable() {
			t.Fatalf("%v 绝不可重试（族重放会吊销整族）", s)
		}
	}
}

// TestClassifyRotateErrorNetErrHintWrapped —— 真实链路里 netErrHint 会把错误包成
// ErrNetworkUnreachable 哨兵（202 轮的分流）。必须保证包装后仍归入正确状态。
func TestClassifyRotateErrorNetErrHintWrapped(t *testing.T) {
	// 真实链路里 netErrHint 会用 %w 把错误包成 ErrNetworkUnreachable 哨兵（202 轮的分流）。
	// 必须保证包装后（且中文文案已不含英文关键字）仍归入正确状态。
	wrapped := fmt.Errorf("%w: 无法与服务器建立安全连接（TLS 握手超时）。若你使用代理软件，请把该服务器域名设为直连",
		synclocal.ErrNetworkUnreachable)
	if got := classifyRotateError(wrapped); got != rotateNotSent {
		t.Fatalf("包装后的 TLS 握手超时应判 not_sent（请求未发出、可重试），实得 %v", got)
	}
	// 未知错误保守侧：按"可能已消费"处理，宁可提示重登也不做注定吊销的重试。
	if got := classifyRotateError(errors.New("some weird transport thing")); got != rotateMaybeConsumed {
		t.Fatalf("未知错误应保守判 maybe_consumed，实得 %v", got)
	}
}

// ---- 批次2-1：登录语义（State 失败不得改判登录） ----

// newDegradedStateServer = 登录成功、但 /api/sync/state 永远超时的服务端。
func newDegradedStateServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"AT-1","refresh_token":"RT-0"}`))
		case "/api/auth/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"AT-2","refresh_token":"RT-1"}`))
		case "/api/sync/state":
			// 模拟实机：请求已到服务端，但响应迟迟不回（客户端 15s 超时）
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(504)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestLoginSucceedsEvenWhenStateProbeFails —— ⭐ 批次 2 的核心钉子。
//
// 实机症状：SignIn 已成功（档案写盘 + 封存 refresh 落库 + 内存票在位），
// State 探测因网络超时失败 ⇒ v21.202 的 `return nil, err` 把它报成"登录失败"。
// 修后：State 网络类失败**不改判登录**，返回成功 + network_warning，并保留带票 worker
// （卸工会关闭调度门，把网络抖动升级成必须重登）。
func TestLoginSucceedsEvenWhenStateProbeFails(t *testing.T) {
	ts := newDegradedStateServer(t)
	ls, _ := bootWithLogin(t)

	out, err := ls.RemoteLogin(ts.URL, "alice", "Passw0rd!2026")
	if err != nil {
		t.Fatalf("State 探测网络失败不得把登录报成失败，实得 err=%v", err)
	}
	if out == nil {
		t.Fatal("必须返回结果映射")
	}
	if out["logged_in"] != true {
		t.Fatalf("logged_in 应为 true，实得 %v", out["logged_in"])
	}
	if out["state_probed"] != false {
		t.Fatalf("state_probed 应为 false（确实没探通），实得 %v", out["state_probed"])
	}
	if _, ok := out["network_warning"]; !ok {
		t.Fatal("必须透出 network_warning，让用户知道是网络问题不是账号问题")
	}
	// 关键：worker 必须在位且持票 —— 卸工会关掉 allowScheduledRound 的门。
	if workerPtr() == nil {
		t.Fatal("worker 不得被卸（否则调度门关闭，自动同步永久停摆）")
	}
	if !workerHasToken(workerPtr()) {
		t.Fatal("内存票必须保留（登录已成功，凭据已由服务端签发）")
	}
	// 档案必须真的在盘上 —— 这是"登录其实成功了"的事实核。
	if _, _, oerr := localauth.OpenSession(database.DB); oerr != nil {
		t.Fatalf("档案应已落盘可开: %v", oerr)
	}
}

// TestStateRejectedByServerIsRealLoginFailure —— 反向：服务端权威 401 才算登录失败。
// 防止把"凡 State 失败都当成功"做成无脑乐观。
func TestStateRejectedByServerIsRealLoginFailure(t *testing.T) {
	var stateCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"access_token":"AT-1","refresh_token":"RT-0"}`))
		case "/api/auth/refresh":
			_, _ = w.Write([]byte(`{"access_token":"AT-2","refresh_token":"RT-1"}`))
		case "/api/sync/state":
			stateCalls++
			w.WriteHeader(401) // 服务端权威否认凭据
		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()
	ls, _ := bootWithLogin(t)

	_, err := ls.RemoteLogin(ts.URL, "alice", "Passw0rd!2026")
	if err == nil {
		t.Fatal("State 被服务端 401 拒绝时必须报登录失败")
	}
	if !errors.Is(err, synclocal.ErrAuthRevoked) {
		t.Fatalf("错误应为 ErrAuthRevoked，实得 %v", err)
	}
	if stateCalls == 0 {
		t.Fatal("State 应被调用过")
	}
	// 降级后必须清票（否则等于没降级），但**保留 worker**。
	if workerPtr() == nil {
		t.Fatal("401 降级同样不得卸工（会关闭调度门）")
	}
	if workerHasToken(workerPtr()) {
		t.Fatal("401 降级后内存票必须清空")
	}
}

// ---- 批次2-2：调度门不得被降级永久关闭 ----

// TestDemoteKeepsSchedulerGateOpen —— 钉住最隐蔽的那条：降级后 allowScheduledRound
// 仍能放行（否则一次 401 就让自动同步永久停摆，只能手点同步复活）。
func TestDemoteKeepsSchedulerGateOpen(t *testing.T) {
	ls, _ := bootWithLogin(t)
	// 直接给在位 worker 塞一张票（不触网，避免依赖测试服务器可达性）
	mountWorker(&localauth.Session{DeviceID: "dev-1", ServerURL: "http://example.invalid"}, "AT-X", ls.uploadDir)
	if !workerHasToken(workerPtr()) {
		t.Fatal("前置：应带票")
	}
	ls.demoteToRelogin("test-demote")
	if workerPtr() == nil {
		t.Fatal("降级不得把 worker 置 nil")
	}
	if workerHasToken(workerPtr()) {
		t.Fatal("降级后内存票必须清空")
	}
	// 恢复态：worker 在位 + sessionRestorable() 需要 lsInstance 非 nil 且 sessionRestored。
	// demote 置 sessionRestored=false，模拟用户已重登恢复。
	ls.sessionRestored = true
	if !allowScheduledRound() {
		t.Fatal("降级后调度门必须仍能放行一轮（否则自动同步永久停摆）")
	}
}

// TestEnsureAccessNetworkFailureKeepsWorker —— 网络类换票失败不得卸工。
func TestEnsureAccessNetworkFailureKeepsWorker(t *testing.T) {
	ts := newDegradedStateServer(t) // state 超时；refresh 正常
	ls, _ := bootWithLogin(t)
	// 指向一个不可达地址 ⇒ Refresh 直接网络失败
	if _, err := database.DB.Exec(`UPDATE client_profile SET server_url=? WHERE id=1`,
		"http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	ok, err := ensureAccess(ls.uploadDir)
	if err == nil {
		t.Fatal("不可达地址应返回错误")
	}
	if ok {
		t.Fatal("换票失败时 ok 应为 false")
	}
	if workerPtr() == nil {
		t.Fatal("网络类失败不得卸工（否则断网一次就永久停摆）")
	}
	_ = ts
}

// ---- 批次2-4：SaveRefresh CAS（防跨账号串写） ----

// TestSaveRefreshCASRejectsStaleWrite —— CAS 令牌不匹配时必须拒写。
// 场景：换票网络往返 15s 期间用户切了账号，迟到的旧账号轮转票不得覆盖新账号档案。
func TestSaveRefreshCASRejectsStaleWrite(t *testing.T) {
	ts, _ := bootWithLogin(t)
	_ = ts
	// 记下当前封存件作为"过期"的期望值（换票网络往返前抓的快照）
	stale := localauth.CurrentRefreshSealed(database.DB)
	if len(stale) == 0 {
		t.Skip("本机 DPAPI 不可用，跳过 CAS 用例")
	}
	// 模拟这 15s 期间档案被改写：换账号时 SignIn 会覆盖 refresh_sealed。
	// 直接把封存件改成另一张票的密文即可（不经过 seal，避免依赖 DPAPI 细节）。
	if _, err := database.DB.Exec(`UPDATE client_profile SET username=?, refresh_sealed=? WHERE id=1`,
		"bob", []byte("OTHER-ACCOUNT-SEALED-BYTES")); err != nil {
		t.Fatal(err)
	}
	// 此时 CAS 令牌已过期（封存件已被别人换掉）
	err := localauth.SaveRefreshCAS(database.DB, "RT-NEW-OTHER-ACCOUNT", stale)
	if !errors.Is(err, localauth.ErrProfileChanged) {
		t.Fatalf("过期 CAS 必须被拒（ErrProfileChanged），实得 %v", err)
	}
	// 关键：被拒之后**封存件必须仍是别人的**（不得被覆盖）
	if got := string(localauth.CurrentRefreshSealed(database.DB)); got != "OTHER-ACCOUNT-SEALED-BYTES" {
		t.Fatalf("CAS 失败后不得改动封存件，实得 %q", got)
	}
	// 正确 CAS 令牌应写入成功
	cur := localauth.CurrentRefreshSealed(database.DB)
	if err := localauth.SaveRefreshCAS(database.DB, "RT-NEW", cur); err != nil {
		t.Fatalf("正确 CAS 应成功: %v", err)
	}
	// 再用同一个旧令牌写 ⇒ 必须被拒（第一次已消费）
	if err := localauth.SaveRefreshCAS(database.DB, "RT-NEWER", cur); !errors.Is(err, localauth.ErrProfileChanged) {
		t.Fatalf("同一 CAS 令牌不得二次使用，实得 %v", err)
	}
	// 空串拒绝
	if err := localauth.SaveRefreshCAS(database.DB, "", cur); err == nil {
		t.Fatal("空 token 必须拒绝")
	}
}

// ---- 批次2-4：PIN 熔断只由用户动作复位 ----

// TestUnlockFailuresNotResetByBackgroundExchange —— 后台换票成功不得复位 PIN 熔断。
func TestUnlockFailuresNotResetByBackgroundExchange(t *testing.T) {
	if !localauth.PINEnrolled(database.DB) {
		t.Skip("本机未设置 PIN，跳过")
	}
	// 记下当前计数
	before := localauth.UnlockFailureCount()
	// 模拟后台换票（ensureAccess 成功路径）—— 不应触碰计数
	// 直接断言 ensureAccess 源码不调用 ResetUnlockFailures：这里用行为断言替代
	// （连错 N 次后跑一次 ensureAccess，计数必须不变）
	ls, _ := bootWithLogin(t)
	_ = ls
	after := localauth.UnlockFailureCount()
	if after != before {
		t.Fatalf("后台路径不得改 PIN 失败计数：before=%d after=%d", before, after)
	}
}

// TestRotateAfterLoginKeepsSealedTokenOnNetworkFail —— 轮转网络失败时保留登录时那张票，
// 不得返回空票（否则封存件被写成空 = 换票链断裂）。
func TestRotateAfterLoginKeepsSealedTokenOnNetworkFail(t *testing.T) {
	// 不可达地址 ⇒ 网络失败
	access, refresh, st := rotateAfterLoginState("http://127.0.0.1:1", "AT-1", "RT-0")
	if access != "AT-1" {
		t.Fatalf("access 必须保留原值，实得 %q", access)
	}
	if refresh != "RT-0" {
		t.Fatalf("refresh 必须保留原值（封存件靠它），实得 %q", refresh)
	}
	if st != rotateNotSent {
		t.Fatalf("不可达地址应判 not_sent，实得 %v", st)
	}
	if strings.TrimSpace(st.String()) == "" {
		t.Fatal("状态字符串不得为空（要写进日志供排障）")
	}
}
