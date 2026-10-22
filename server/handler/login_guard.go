package handler

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"stickynote/database"
	"stickynote/logger"
)

// 登录防爆破（v18 → v20.8 持久化）：
// L1：纯内存 map（零延迟，与之前行为一致）—— 主读路径。
// L2：SQLite login_fails 表（异步写，不阻塞登录请求）—— 重启不丢失计数。
// 规则：
//   - 用户名+IP 维度：窗口内连续失败 LOGIN_MAX_FAILS 次 → 锁 LOGIN_LOCK_SECONDS
//   - IP 总量维度：窗口内累计失败 LOGIN_IP_MAX_FAILS 次 → 同样锁定（防换账号扫号）
//
// 锁定中即使密码正确也拒绝（429），话术统一，不暴露账号是否存在。
// 阈值/时长走环境变量，冒烟可用短时长验证"锁定→自动解锁"全流程。
type failRecord struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

type loginGuard struct {
	mu    sync.Mutex
	fails map[string]*failRecord
}

var guard = &loginGuard{fails: make(map[string]*failRecord)}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func loginLimits() (maxFails, ipMaxFails int, window, lock time.Duration) {
	maxFails = envInt("LOGIN_MAX_FAILS", 5)
	ipMaxFails = envInt("LOGIN_IP_MAX_FAILS", 30)
	window = time.Duration(envInt("LOGIN_WINDOW_SECONDS", 300)) * time.Second
	lock = time.Duration(envInt("LOGIN_LOCK_SECONDS", 600)) * time.Second
	return
}

// InitLoginGuard 初始化登录防爆破（v21.93 P1：初始化时序修复）。
//
// 缺陷背景：此前 loadFromDB() 挂在包级 `init()` 中，而 Go 的包级 init() 在
// main() 之前执行——此时 database.Init 尚未运行，database.DB 恒为 nil，
// loadFromDB 首行 `if database.DB == nil { return }` 必然命中直接返回。
// 全库 loadFromDB 仅此一个调用点，意味着「重启后从 DB 恢复未过期失败记录」
// 这条持久化语义（v20.8 引入）在正常启动时从未生效，重启即全部防爆破状态清零。
//
// 修复：改为导出函数，由 StartServer 在 database.Init 成功之后显式调用。
func InitLoginGuard() {
	// 启动持久化串行队列（幂等）后再恢复——顺序保证先于任何写入
	startPersistWorker()

	// 从 DB 恢复未过期记录（v20.8 持久化）
	loadFromDB()

	// 每 10 分钟清扫过期记录（内存+DB），防内存缓慢增长
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			guard.sweep()
		}
	}()
}

// loadFromDB 启动时加载 DB 中未过期的失败记录到内存。
func loadFromDB() {
	if database.DB == nil {
		return
	}
	now := time.Now().Unix()
	rows, err := database.DB.Query("SELECT key, count, window_start, locked_until FROM login_fails WHERE locked_until > ? OR (locked_until = 0 AND window_start > 0)", now)
	if err != nil {
		return
	}
	defer rows.Close()

	guard.mu.Lock()
	defer guard.mu.Unlock()
	for rows.Next() {
		var key string
		var count int
		var ws, lu int64
		if err := rows.Scan(&key, &count, &ws, &lu); err != nil {
			continue
		}
		guard.fails[key] = &failRecord{
			count:       count,
			windowStart: time.Unix(ws, 0),
			lockedUntil: time.Unix(lu, 0),
		}
	}
	logger.Info("loginGuard: loaded %d records from DB", len(guard.fails))
}

// persistFail 异步写 DB（不阻塞登录流程）。
// v21.54 P0 健壮性修复：nil 检查移入 goroutine + recover 兜底——
// ---- 持久化串行队列（v21.93 P1：乱序竞态修复）----
//
// 缺陷背景：persistFail/persistDelete 各自 `go func(){ db.Exec(...) }()`，
// 两个 goroutine 的落库顺序无保证。「请求 A 失败 → 异步 UPSERT」与
// 「请求 B 成功 → 异步 DELETE」可能以 DELETE→UPSERT 的顺序提交，结果刚成功
// 登录的账号在 DB 中被旧失败记录重新写回，重启后 loadFromDB 恢复出已作废的
// 计数 → 误锁定（可用性）或计数偏差（安全控制一致性）。
//
// 为何不直接改同步写：persistFail/persistDelete 是在 g.mu 临界区内被调用的
// （见 fail/success），同步做 DB I/O 会把持锁时间从微秒级拉到毫秒级，
// 且 SQLite MaxOpenConns=2，DB 抖动会直接阻塞所有登录校验路径。
//
// 因此采用单消费者队列：
//   - 顺序保证：FIFO 投递顺序 = 落库顺序，乱序窗口消失
//   - 不持锁做 I/O：DB 写在独立 goroutine 内执行
//   - 非阻塞投递：队列满则丢弃并记日志——防爆破记录属纵深防御，
//     宁可丢一条统计，也不能让 DB 慢/满拖垮登录主链路
const persistQueueSize = 256

type persistKind int

const (
	persistUpsert persistKind = iota
	persistRemove
	persistPurge   // 批量清理窗口外过期记录（sweep 额外 purge，统一经 FIFO）
	persistBarrier // 同步屏障：仅用于测试等待队列排空
)

type persistTask struct {
	kind  persistKind
	key   string
	count int
	ws    time.Time
	lu    time.Time
	// persistPurge 专用：nowUnix=当前时间戳，windowCutoff=window_start 截止（nowUnix-windowSec）
	nowUnix      int64
	windowCutoff int64
}

var (
	persistCh   chan persistTask
	persistAck  = make(chan struct{}, 1)
	persistOnce sync.Once
	// v21.97（审计 L-02）：队列丢弃累计计数（原子）。丢弃本身已有 Error 日志，
	// 此计数供运维确认丢弃规模（大量丢弃 = 登录风暴或 DB 长期不可用信号）。
	persistDropTotal atomic.Int64
)

// persistDropCount 返回队列累计丢弃数（v21.97 L-02，测试/可观测性用）。
func persistDropCount() int64 {
	return persistDropTotal.Load()
}

// startPersistWorker 幂等启动单消费者（由 InitLoginGuard 调用）。
func startPersistWorker() {
	persistOnce.Do(func() {
		persistCh = make(chan persistTask, persistQueueSize)
		go persistWorker()
	})
}

func persistWorker() {
	for task := range persistCh {
		// v21.100（CI go test -race 首跑实测 DATA RACE 修复）：改用 GetDB() 原子读。
		// 本 worker 为进程级单例后台 goroutine，测试中跨测试存活，而每个测试经
		// database.Init() 重初始化新 DB——直接读全局 database.DB 会与 Init 的写入
		// 并发（race）。GetDB() 经 dbMu.RWMutex 与 Init 的写入互斥：无 race，且
		// 每次任务取当前 DB（测试的 upsert 落在当前测试库；生产仅 Init 一次语义不变）。
		db := database.GetDB()
		if db == nil {
			if task.kind == persistBarrier {
				signalPersistAck()
			}
			continue
		}
		var err error
		switch task.kind {
		case persistUpsert:
			_, err = db.Exec(
				"INSERT OR REPLACE INTO login_fails (key, count, window_start, locked_until) VALUES (?, ?, ?, ?)",
				task.key, task.count, task.ws.Unix(), task.lu.Unix(),
			)
		case persistRemove:
			_, err = db.Exec("DELETE FROM login_fails WHERE key = ?", task.key)
		case persistPurge:
			_, err = db.Exec("DELETE FROM login_fails WHERE locked_until <= ? AND window_start < ?",
				task.nowUnix, task.windowCutoff)
		case persistBarrier:
			signalPersistAck()
		}
		if err != nil {
			logger.Error("loginGuard persist task(kind=%d, key=%s): %v", task.kind, task.key, err)
		}
	}
}

func signalPersistAck() {
	select {
	case persistAck <- struct{}{}:
	default:
	}
}

// enqueue 非阻塞投递持久化任务；队列未启动（纯内存单测）或已满时安全丢弃。
func enqueue(task persistTask) {
	if persistCh == nil {
		return
	}
	select {
	case persistCh <- task:
	default:
		persistDropTotal.Add(1) // v21.97 L-02：丢弃计数（可观测性）
		logger.Error("loginGuard: persist queue full, drop task (key=%s)", task.key)
	}
}

// persistFail 记录一次失败到 DB（经串行队列）。
// v21.54 P0 健壮性：DB 为 nil 或已关闭时由 worker 内 nil 检查兜底，避免 panic。
func persistFail(key string, count int, ws, lu time.Time) {
	enqueue(persistTask{kind: persistUpsert, key: key, count: count, ws: ws, lu: lu})
}

// persistDelete 从 DB 删除记录（经串行队列，与 persistFail 共享顺序）。
func persistDelete(key string) {
	enqueue(persistTask{kind: persistRemove, key: key})
}

// persistFlush 等待队列排空（仅测试使用）：投递屏障任务并等待其被消费，
// 屏障被处理即意味着此前所有任务均已落库。
func persistFlush() {
	if persistCh == nil {
		return
	}
	// 先清空可能残留的 ack，避免误读上一轮的信号
	select {
	case <-persistAck:
	default:
	}
	enqueue(persistTask{kind: persistBarrier})
	select {
	case <-persistAck:
	case <-time.After(5 * time.Second):
	}
}

func (g *loginGuard) sweep() {
	_, _, window, _ := loginLimits()
	now := time.Now()
	nowUnix := now.Unix()

	g.mu.Lock()
	var expiredKeys []string
	for k, r := range g.fails {
		if now.After(r.lockedUntil) && now.Sub(r.windowStart) > window {
			delete(g.fails, k)
			expiredKeys = append(expiredKeys, k)
		}
	}
	g.mu.Unlock()

	// P1-1'（v1.0.134 审计整改）：过期记录清理统一经 FIFO 串行队列落库，
	// 不再在 goroutine 内直连 db.Exec 绕过 persistWorker——此前与 persistFail/
	// persistDelete 的 FIFO 出口并存，存在顺序竞态窗口（详见 v21.93 P1 注释）。
	// 窗口外批量 purge 同样经 persistPurge 出口，保证所有 DB 写共享单一串行消费者。
	for _, k := range expiredKeys {
		persistDelete(k)
	}
	windowSec := int64(window.Seconds())
	enqueue(persistTask{
		kind:         persistPurge,
		nowUnix:      nowUnix,
		windowCutoff: nowUnix - windowSec,
	})
}

// locked 判断 (用户名+IP) 或 IP 总量是否处于锁定期。
// lockRetryMessage v21.108 P1-11：锁定类统一话术，携带真实剩余时长（向上取整分钟）。
// 此前各端点写死「请稍后再试」或「请10分钟后再试」——前者用户不知要等多久反复重试，
// 后者与实际剩余时间不符（刚锁定说 10 分钟，即将解锁也说 10 分钟）。
func lockRetryMessage(prefix string, until time.Time) string {
	remain := time.Until(until)
	if remain < 0 {
		remain = 0
	}
	mins := int((remain + time.Minute - 1) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	return fmt.Sprintf("%s，请在 %d 分钟后再试", prefix, mins)
}

func (g *loginGuard) locked(username, ip string) (bool, time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for _, key := range []string{username + "|" + ip, "ip|" + ip} {
		if r, ok := g.fails[key]; ok && now.Before(r.lockedUntil) {
			return true, r.lockedUntil
		}
	}
	return false, time.Time{}
}

// fail 记录一次登录失败，达到阈值即进入锁定期。
func (g *loginGuard) fail(username, ip string) {
	maxFails, ipMaxFails, window, lock := loginLimits()
	now := time.Now()
	g.mu.Lock()
	for _, rec := range []struct {
		key string
		max int
	}{{username + "|" + ip, maxFails}, {"ip|" + ip, ipMaxFails}} {
		r := g.fails[rec.key]
		if r == nil || now.Sub(r.windowStart) > window {
			r = &failRecord{windowStart: now}
			g.fails[rec.key] = r
		}
		r.count++
		if r.count >= rec.max {
			r.lockedUntil = now.Add(lock)
		}
		// 异步持久化到 DB
		persistFail(rec.key, r.count, r.windowStart, r.lockedUntil)
	}
	g.mu.Unlock()
}

// success 登录成功：清掉该 用户名+IP 的失败记录。
// IP 总量记录保留——防止攻击者用"交替账号成功登录"稀释总量计数。
func (g *loginGuard) success(username, ip string) {
	key := username + "|" + ip
	g.mu.Lock()
	delete(g.fails, key)
	g.mu.Unlock()
	persistDelete(key)
}

// ---- 账号维度防爆破（v21.52 P5 审计修复）----
// 找回密码/安全答案验证此前只有 username|IP 与 ip| 维度，攻击者可分散来源 IP
// 持续猜测同一账号的低熵安全答案。新增 account:<username> 全局键：
// 任何 IP 的失败都累计到账号维度，达到阈值即锁定该账号全部来源。

// accountLocked 判断账号全局维度是否锁定。
func (g *loginGuard) accountLocked(username string) (bool, time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := "account:" + username
	if r, ok := g.fails[key]; ok && time.Now().Before(r.lockedUntil) {
		return true, r.lockedUntil
	}
	return false, time.Time{}
}

// accountFail 记录一次账号维度失败（找回密码/安全答案失败时调用）。
func (g *loginGuard) accountFail(username string) {
	maxFails, _, window, lock := loginLimits()
	key := "account:" + username
	now := time.Now()
	g.mu.Lock()
	r := g.fails[key]
	if r == nil || now.Sub(r.windowStart) > window {
		r = &failRecord{windowStart: now}
		g.fails[key] = r
	}
	r.count++
	if r.count >= maxFails {
		r.lockedUntil = now.Add(lock)
	}
	g.mu.Unlock()
	persistFail(key, r.count, r.windowStart, r.lockedUntil)
}

// accountSuccess 账号维度成功（找回密码成功时清零，语义同登录成功）。
func (g *loginGuard) accountSuccess(username string) {
	key := "account:" + username
	g.mu.Lock()
	delete(g.fails, key)
	g.mu.Unlock()
	persistDelete(key)
}

// 兼容 sql 未使用告警
var _ = sql.ErrNoRows
