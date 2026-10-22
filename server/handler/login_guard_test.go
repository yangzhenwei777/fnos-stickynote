package handler

import (
	"os"
	"testing"
	"time"
)

// 登录防爆破核心逻辑（v18→v20.8）：
// L1 内存锁：用户名+IP 维度达到 LOGIN_MAX_FAILS 锁定；IP 总量维度独立计数。
// 测试用短时长环境变量，不影响默认生产值。

func setLoginEnv(t *testing.T, maxFails, ipMaxFails int, window, lock time.Duration) {
	t.Helper()
	os.Setenv("LOGIN_MAX_FAILS", itoa(maxFails))
	os.Setenv("LOGIN_IP_MAX_FAILS", itoa(ipMaxFails))
	os.Setenv("LOGIN_WINDOW_SECONDS", itoa(int(window.Seconds())))
	os.Setenv("LOGIN_LOCK_SECONDS", itoa(int(lock.Seconds())))
	t.Cleanup(func() {
		os.Unsetenv("LOGIN_MAX_FAILS")
		os.Unsetenv("LOGIN_IP_MAX_FAILS")
		os.Unsetenv("LOGIN_WINDOW_SECONDS")
		os.Unsetenv("LOGIN_LOCK_SECONDS")
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// 连续失败达到阈值 → 锁定；锁定中即使调用 locked 也返回 true
func TestLoginGuardLockAfterThreshold(t *testing.T) {
	setLoginEnv(t, 2, 10, 60*time.Second, 60*time.Second)
	g := &loginGuard{fails: make(map[string]*failRecord)}

	g.fail("alice", "10.0.0.1")
	if locked, _ := g.locked("alice", "10.0.0.1"); locked {
		t.Fatal("第一次失败不应锁定")
	}
	g.fail("alice", "10.0.0.1")
	locked, until := g.locked("alice", "10.0.0.1")
	if !locked {
		t.Fatal("第二次失败（达阈值）应锁定")
	}
	if until.IsZero() {
		t.Fatal("锁定应有截止时间")
	}
	if !time.Now().Before(until) {
		t.Fatal("锁定截止时间应在未来")
	}
}

// 不同用户名同 IP：IP 总量维度独立计数（防换账号扫号）
func TestLoginGuardIPTotalDimension(t *testing.T) {
	setLoginEnv(t, 5, 2, 60*time.Second, 60*time.Second)
	g := &loginGuard{fails: make(map[string]*failRecord)}

	g.fail("user_a", "10.0.0.2")
	g.fail("user_b", "10.0.0.2")
	// IP 总量达 2 → 该 IP 下任何用户名都锁定
	locked, _ := g.locked("user_c", "10.0.0.2")
	if !locked {
		t.Fatal("IP 总量达阈值后新用户名也应被锁")
	}
	// 其他 IP 不受影响
	if locked, _ := g.locked("user_a", "10.0.0.3"); locked {
		t.Fatal("其他 IP 不应被锁")
	}
}

// 登录成功清掉 用户名+IP 记录；IP 总量保留（防稀释计数）
func TestLoginGuardSuccessClearsUserKey(t *testing.T) {
	setLoginEnv(t, 2, 10, 60*time.Second, 60*time.Second)
	g := &loginGuard{fails: make(map[string]*failRecord)}

	g.fail("alice", "10.0.0.4")
	g.success("alice", "10.0.0.4")
	locked, _ := g.locked("alice", "10.0.0.4")
	if locked {
		t.Fatal("成功登录后用户名|IP 记录应清除")
	}
	if _, ok := g.fails["ip|10.0.0.4"]; !ok {
		t.Fatal("IP 总量记录应保留（防交替账号稀释）")
	}
}

// 锁定到期后自动解锁；过期记录由 sweep 清理
func TestLoginGuardAutoUnlockAndSweep(t *testing.T) {
	setLoginEnv(t, 2, 10, 60*time.Second, 1*time.Second)
	g := &loginGuard{fails: make(map[string]*failRecord)}
	now := time.Now()

	// 直接注入一条已过期的锁定记录（窗口起点在 2 分钟前，超出 60s 窗口）
	g.fails["bob|10.0.0.5"] = &failRecord{
		count:       2,
		windowStart: now.Add(-2 * time.Minute),
		lockedUntil: now.Add(-1 * time.Second),
	}
	if locked, _ := g.locked("bob", "10.0.0.5"); locked {
		t.Fatal("过期的锁定记录不应再判定为锁定")
	}

	g.sweep()
	if _, ok := g.fails["bob|10.0.0.5"]; ok {
		t.Fatal("sweep 应清理窗口外过期记录")
	}
}

// 窗口滑动：窗口外的失败次数归零重计
func TestLoginGuardWindowReset(t *testing.T) {
	setLoginEnv(t, 2, 10, 60*time.Second, 60*time.Second)
	g := &loginGuard{fails: make(map[string]*failRecord)}
	now := time.Now()

	g.fail("carol", "10.0.0.6")
	// 把窗口起点拨到 2 分钟前（超出窗口）
	g.mu.Lock()
	g.fails["carol|10.0.0.6"].windowStart = now.Add(-2 * time.Minute)
	g.mu.Unlock()

	g.fail("carol", "10.0.0.6")
	// 窗口重置后首次失败 count=1 < 2，不应锁定
	if locked, _ := g.locked("carol", "10.0.0.6"); locked {
		t.Fatal("窗口外失败应重置计数，不应锁定")
	}
}

// P1-1'（v1.0.134 审计整改）：sweep 的过期清理经 FIFO 串行队列投递，
// 不再在 goroutine 内直连 db.Exec。验证：① 启动 worker 后 sweep 不 panic；
// ② 内存记录被同步删除；③ persistFlush 能排空队列（含 persistPurge 任务）。
// 测试环境无真实 DB（database.DB==nil），worker 安全丢弃任务，仅验证路由与内存语义。
func TestLoginGuardSweepViaFIFO(t *testing.T) {
	setLoginEnv(t, 2, 10, 60*time.Second, 1*time.Second)
	startPersistWorker() // 启动 FIFO 单消费者（幂等）

	g := &loginGuard{fails: make(map[string]*failRecord)}
	now := time.Now()
	g.fails["fifo|10.0.0.9"] = &failRecord{
		count:       2,
		windowStart: now.Add(-2 * time.Minute),
		lockedUntil: now.Add(-1 * time.Second),
	}

	g.sweep()
	persistFlush() // 等待队列排空（含 persistPurge），不应 panic

	if _, ok := g.fails["fifo|10.0.0.9"]; ok {
		t.Fatal("sweep 应经 FIFO 清理窗口外过期记录（内存）")
	}
}
