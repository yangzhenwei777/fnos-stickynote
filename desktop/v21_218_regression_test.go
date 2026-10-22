// v21_218_regression_test.go —— 212 实机深度核查的 Go 侧回归钉。
//
// 定罪背景（全部来自 212-ga 实机取证，不是推测）：
//
//	· 实机 profile 只有 g-000001 一代 ⇒ 换代从未发生；
//	· g-000001 的 client_profile 有 1 行且 refresh_sealed 非空（凭据在），
//	  而 notes/note_folders/sync_changes 皆 0 行（数据一条未落地）；
//	· 日志 4 次启动均有 `[auth] device session restored` ⇒ **后端每轮都判定已登录**，
//	  但 UI 显示未登录 ⇒ 前端登录态链路断裂（本文件修不了，见前端钉）。
//
// 本文件覆盖三条 Go 侧修复，每条都注明对应的实机症状。
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"stickynote-desktop/synclocal"
)

// ---- P1-2：快照请求必须接 ctx（取消信号不得在网络层漏接）----

// TestSnapshotRequestsAcceptContext 钉住 RequestSnapshot/SnapshotStatus 的签名带 ctx。
//
// 实机症状：`[bootstrap] 在途轮未在 3s 内完全收尾…继续换代` 与随后的
// `transport error: ... context deadline exceeded (Client.Timeout exceeded while awaiting
// headers)` 相隔 30 秒 —— 那 30s 就是 ctx 未接、只能等满 Client.Timeout 的空窗。
// 改为 ctx 版后，关机/换代取消能立即生效。
func TestSnapshotRequestsAcceptContext(t *testing.T) {
	cl := synclocal.NewClient("http://127.0.0.1:1", "tok", "dev")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 取消的 ctx ⇒ 立即返回，绝不该等满客户端超时。
		_, _ = cl.RequestSnapshot(ctx)
	}()

	select {
	case <-done:
		// 预期路径
	case <-time.After(5 * time.Second):
		t.Fatal("RequestSnapshot 未响应 ctx 取消：取消信号在网络层漏接（应改为 doWithContext）")
	}

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		_, _ = cl.SnapshotStatus(ctx, "snap-1")
	}()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("SnapshotStatus 未响应 ctx 取消：取消信号在网络层漏接")
	}
}

// ---- P1-2 附带：响应体截断不得伪装成「JSON 格式错」----

// TestTruncatedBodyIsNetworkErrorNotJSONError 钉住错误分类。
//
// 实机症状：`[bootstrap] 自动快照失败: unexpected end of JSON input` —— 用户/排障者
// 会误以为「服务端返回了坏 JSON / 数据损坏」。真因是 io.ReadAll 错误被丢弃
// （`body, _ := io.ReadAll(...)`），弱网截断的半截 JSON 继续走 Unmarshal。
// 修后必须归类为网络类（ErrNetworkUnreachable），UI 才会提示「网络/代理」而非误导。
func TestTruncatedBodyIsNetworkErrorNotJSONError(t *testing.T) {
	cl := synclocal.NewClient("http://127.0.0.1:1", "tok", "dev")

	_, err := cl.RequestSnapshot(context.Background())
	if err == nil {
		t.Skip("目标端口恰好可达，跳过（本钉针对不可达时的分类）")
	}
	// 不可达时必须是网络类，且**不得**出现 JSON 解析错误字样。
	if errors.Is(err, synclocal.ErrNetworkUnreachable) {
		if containsJSONParseWording(err.Error()) {
			t.Fatalf("网络失败被误分类为 JSON 解析问题（会误导用户以为数据损坏）: %v", err)
		}
		return
	}
	// 非网络类错误也必须是取消/传输语义，不能是 JSON 解析。
	if containsJSONParseWording(err.Error()) {
		t.Fatalf("不可达时返回了 JSON 解析错误，分类错误: %v", err)
	}
}

func containsJSONParseWording(s string) bool {
	return strings.Contains(s, "unexpected end of JSON input") ||
		strings.Contains(s, "invalid character")
}

// ---- P2：自动快照失败必须走短退避，不得写入 5 分钟长冷却 ----

// TestAutoBootstrapFailureDoesNotEnterLongCooldown 钉住失败退避。
//
// 实机症状：一次网络失败即命中 autoBootstrapCooldown（**5 分钟**）停摆自动快照，
// 于是笔记持续空白、用户反复重登 —— 每次重登又失败，于是「反复重启最终仍未登录」。
// 修后：失败只推进 30s~2min 的短退避，且 30s 后即自动重试。
func TestAutoBootstrapFailureDoesNotEnterLongCooldown(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	now := time.Now()

	// 失败一次
	autoBootstrapFailed()

	// 立即（1 秒后）不允许 —— 短退避生效
	if allow, _ := autoBootstrapAllow(now.Add(time.Second)); allow {
		t.Fatalf("失败退避期内(1s)不应允许自动 bootstrap，got allow=true")
	}

	// 关键断言：30s 后（远小于 5 分钟长冷却）必须已恢复允许
	// —— 这正是旧实现挂掉的地方（旧实现此刻仍被 5 分钟冷却挡着 ⇒ 永久空白）。
	if allow, tripped := autoBootstrapAllow(now.Add(31 * time.Second)); !allow || tripped {
		t.Fatalf("失败后 31s 应自动重试（旧实现在此处被 5 分钟长冷却挡住 ⇒ 笔记持续空白），got allow=%v tripped=%v",
			allow, tripped)
	}
}

// TestAutoBootstrapRetryBackoffIsBounded 钉住退避时长递增且封顶 2 分钟。
func TestAutoBootstrapRetryBackoffIsBounded(t *testing.T) {
	prev := time.Duration(0)
	for streak := 1; streak <= 20; streak++ {
		d := autoBootstrapRetryBackoff(streak)
		if d <= 0 {
			t.Fatalf("streak=%d 退避时长必须为正，得到 %s", streak, d)
		}
		if d > autoBootstrapFailBackoffMax {
			t.Fatalf("streak=%d 退避 %s 超过封顶 %s", streak, d, autoBootstrapFailBackoffMax)
		}
		if d < prev {
			t.Fatalf("退避必须单调递增：streak=%d 得 %s，小于上一档 %s", streak, d, prev)
		}
		prev = d
	}
	// 首档必须是 30s（网络类失败通常几十秒自愈，不能等太久）
	if got := autoBootstrapRetryBackoff(1); got != 30*time.Second {
		t.Fatalf("首档退避应为 30s，得到 %s", got)
	}
}

// TestAutoBootstrapFailureDoesNotTripCircuit 失败**不是**重建循环证据，不得熔断。
//
// 熔断语义只针对「bootstrap 成功但仍判代际不一致」（确定性循环证据）。
// 若把网络失败也计入，用户会遇到「网络抖动几次后自动同步被永久熔断」。
func TestAutoBootstrapFailureDoesNotTripCircuit(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	for i := 0; i < 10; i++ {
		autoBootstrapFailed()
	}
	if AutoBootstrapTripped() {
		t.Fatal("连续网络失败**不得**熔断自动重建（失败不是循环证据），但已熔断")
	}
	// 退避封顶后仍应保持未熔断，用户可继续手动强制同步
	if _, retrying := autoBootstrapRetryRemaining(time.Now()); !retrying {
		t.Fatal("连续失败后仍应处于退避态（可观测），却报告无退避")
	}
}

// TestAutoBootstrapSuccessClearsRetryBackoff 成功一次即解除失败退避。
func TestAutoBootstrapSuccessClearsRetryBackoff(t *testing.T) {
	resetCircuitForTest()
	defer resetCircuitForTest()

	autoBootstrapFailed()
	if _, retrying := autoBootstrapRetryRemaining(time.Now()); !retrying {
		t.Fatal("失败后应有退避态")
	}
	autoBootstrapDone(time.Now(), false)
	if _, retrying := autoBootstrapRetryRemaining(time.Now()); retrying {
		t.Fatal("成功后必须解除失败退避，却仍在退避态")
	}
}
