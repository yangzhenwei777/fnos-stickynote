package handler

import (
	"testing"
	"time"
)

// 账号维度防爆破测试（v21.54 P0：v21.52 P5 回归点）
// 覆盖：accountLocked 边界、accountFail 达到阈值锁定、accountSuccess 清零、
// 与 username|IP 维度相互独立。

func setLoginEnv2(t *testing.T, maxFails, ipMaxFails int, window, lock time.Duration) {
	t.Helper()
	setLoginEnv(t, maxFails, ipMaxFails, window, lock)
}

// TestAccountGuard_LockAfterFails 账号维度连续失败达到阈值即锁定。
func TestAccountGuard_LockAfterFails(t *testing.T) {
	setLoginEnv2(t, 3, 30, 300, 600)
	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord) // 清空共享 guard，避免测试间污染
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	username := "victim"

	// 前 2 次失败：未锁定
	guard.accountFail(username)
	if locked, _ := guard.accountLocked(username); locked {
		t.Fatal("第 1 次失败后不应锁定")
	}
	guard.accountFail(username)
	if locked, _ := guard.accountLocked(username); locked {
		t.Fatal("第 2 次失败后不应锁定")
	}
	// 第 3 次失败：达到阈值 → 锁定
	guard.accountFail(username)
	locked, until := guard.accountLocked(username)
	if !locked {
		t.Fatal("第 3 次失败后应锁定")
	}
	if !time.Now().Before(until) {
		t.Error("锁定到期时间应在未来")
	}
}

// TestAccountGuard_SuccessClears 成功清零：锁定后成功调用解除账号锁定。
func TestAccountGuard_SuccessClears(t *testing.T) {
	setLoginEnv2(t, 3, 30, 300, 600)
	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	username := "lucky"
	guard.accountFail(username)
	guard.accountFail(username)
	guard.accountFail(username)
	if locked, _ := guard.accountLocked(username); !locked {
		t.Fatal("应已锁定")
	}

	guard.accountSuccess(username)
	if locked, _ := guard.accountLocked(username); locked {
		t.Fatal("成功后应解除锁定")
	}
}

// TestAccountGuard_IndependentOfIP 账号维度与 username|IP 维度相互独立：
// 不同 IP 攻击同一账号 → IP 维度不锁定，但账号维度累计锁定。
func TestAccountGuard_IndependentOfIP(t *testing.T) {
	setLoginEnv2(t, 3, 30, 300, 600)
	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	username := "multiip"
	// 3 个不同 IP 各失败 1 次：IP 维度各自 1 次（不锁），账号维度累计 3 次（锁定）
	guard.fail(username, "1.1.1.1")
	guard.fail(username, "2.2.2.2")
	guard.fail(username, "3.3.3.3")
	guard.accountFail(username)
	guard.accountFail(username)
	guard.accountFail(username)

	// IP 维度：每个 IP 仅 1 次失败，不锁定
	if locked, _ := guard.locked(username, "1.1.1.1"); locked {
		t.Error("单 IP 1 次失败不应锁定（IP 维度）")
	}
	// 账号维度：3 次失败 → 锁定
	if locked, _ := guard.accountLocked(username); !locked {
		t.Error("账号维度累计 3 次应锁定")
	}
}

// TestAccountGuard_WindowReset 窗口过期后计数重置：旧失败不累计到新窗口。
func TestAccountGuard_WindowReset(t *testing.T) {
	setLoginEnv2(t, 3, 30, 1, 600) // 窗口 1 秒
	guard.mu.Lock()
	guard.fails = make(map[string]*failRecord)
	guard.mu.Unlock()
	defer func() {
		guard.mu.Lock()
		guard.fails = make(map[string]*failRecord)
		guard.mu.Unlock()
	}()

	username := "windowuser"
	guard.accountFail(username)
	time.Sleep(1100 * time.Millisecond) // 窗口过期
	guard.accountFail(username)
	if locked, _ := guard.accountLocked(username); locked {
		t.Error("窗口过期后 2 次失败不应锁定（计数已重置）")
	}
}
