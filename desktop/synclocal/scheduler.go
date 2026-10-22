// scheduler.go —— 桌面后台同步调度器（批次 8-Alt 刀 3；触发面与取舍见 ADR-005）。
//
// 形状约束（两条，都是本仓库踩出来的）：
//  1. **不持有 *Worker**：syncWorker 是包级变量，会被 mountWorker/demoteToRelogin/Logout/
//     PortableImport(RebindDB)/BootstrapNow(换库重建) 六处改写。调度器若捕获指针，换库后会
//     往旧库句柄推同步（批次 4"恢复不挂工→漏账"的同族缺陷）。故只注入 Round/Allow 闭包，
//     运行时读全局。
//  2. **调度状态只存内存**：outbox/cursor 才是需要跨进程的账；再存一份"调度器自己的账"必然
//     出现两份不一致。重启后由登录/会话恢复重新起度（ADR-005 §二.3）。
//
// 单飞是结构性的：轮次在循环 goroutine 内**内联执行**，不存在第二轮并行；执行期间到达的
// Wake 只留一格（容量 1 的 channel），天然合并。
package synclocal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 调度节奏默认值（可被 SchedOptions 覆盖；ADR-005 §二.2/§二.4 定版）。
const (
	DefaultSchedIdle       = 5 * time.Minute
	DefaultSchedProbe      = 60 * time.Second
	DefaultSchedMaxBackoff = 30 * time.Minute
	DefaultSchedFirstDelay = 2 * time.Second
)

// SchedOptions 调度器配置。Round 必填；其余零值取默认。
type SchedOptions struct {
	Idle       time.Duration
	Probe      time.Duration
	MaxBackoff time.Duration
	// FirstDelay 启动后首轮延迟（默认 2s）：给"启动即挂工/换票"留完成窗口，又不让用户干等 Idle。
	FirstDelay time.Duration
	// Round 执行一轮同步（通常 = 换票 + RunOnce）。返回 error 即视为该轮失败。
	Round func(ctx context.Context) error
	// Allow 为假时本轮跳过且**不计失败**（未持票/锁屏/正在导入或恢复）。nil = 恒允许。
	Allow func() bool
	// OnRound 可选回调：每轮结束后拿（结果, 错误, 新状态）——供上层做 401 降级等副作用。
	// 在调度 goroutine 内同步调用，实现里不得长阻塞。
	OnRound func(err error, st SchedState)
	// Logf 可选日志（默认丢弃）。
	Logf func(format string, args ...any)
	// After 可注入的等待函数（仅测试用；nil = time.After 语义）。
	After func(d time.Duration) <-chan time.Time
}

// SchedState 调度器可见状态（供绑定层直接回传前端，不做二次加工）。
type SchedState struct {
	Started             bool      `json:"started"`
	Running             bool      `json:"running"`
	NextAt              time.Time `json:"next_at"`
	LastRoundAt         time.Time `json:"last_round_at"`
	LastOKAt            time.Time `json:"last_ok_at"`
	LastError           string    `json:"last_error,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	Offline             bool      `json:"offline"`        // 传输层失败中 → 短探测节奏
	NeedAttention       bool      `json:"need_attention"` // 协议终态 → 长退避后自动再试（v21.221：不再停手）
	// ⭐ v21.221：凭据作废（服务端 401 权威拒绝）—— 与 NeedAttention 分开，
	// 因为它是**唯一真正需要用户重新登录**的情况，UI 必须给出可执行引导而不是含糊提示。
	ReloginRequired bool `json:"relogin_required"`
	Rounds          int  `json:"rounds"`
}

// Scheduler 单实例后台调度器。零值不可用，必须经 NewScheduler。
type Scheduler struct {
	opt SchedOptions

	mu     sync.Mutex
	st     SchedState
	done   chan struct{}
	wake   chan struct{}
	cancel context.CancelFunc

	// ⭐ v21.212（211 实机铁证·登录卡 67 秒）：在途轮**主动让路**信号。
	//
	// 定罪（211 实机日志 `摘指针后在途轮仍未在 1m0s 内收尾`）：
	// 211 修法假设"摘 worker 指针 ⇒ 在途轮读到 nil 即早退"，**实测证伪** ——
	// `scheduledRound` 开头 `w := workerPtr()` 已把 worker 存进**局部变量**，
	// 之后一路 `w.RunOnce(ctx)` 跑完整轮，摘全局指针对它**完全无效**。
	// 于是 WaitIdle 只能实打实等满 60s，而这段时间又因登录同步等待
	// 而被整段搬到登录按钮上 ⇒ 用户看到「转圈 60+ 秒才进主页面」。
	//
	// 修法：给在途轮一条**显式的让路信号**。换代窗口广播一次，
	// 在途轮在每个分页循环边界检查到就**立即 return**（不碰旧库、不推进游标），
	// 从而把"等网络往返跑完"降为"等一个检查点"= 毫秒级。
	//
	// 为什么不用 ctx 取消：调度器的 ctx 是**进程级 appCtx**，取消它等于停掉整个
	// 调度器（关机语义），不能用于"临时让一轮让路"。故用独立的 round-yield 通道。
	yield    chan struct{}
	yielding bool // 本轮已被要求让路（供 State/诊断读）
}

// yieldRound 在途轮让路信号（容量 1：多次广播合并为一次即可）。
func (s *Scheduler) yieldRound() chan struct{} {
	if s.yield == nil {
		s.yield = make(chan struct{}, 1)
	}
	return s.yield
}

// YieldInFlight 广播「在途轮请立即让路」。幂等、非阻塞。
//
// 调用方（换代窗口）在广播后用 WaitIdle 等待在途轮真正退出。
// 无在途轮时是纯空操作（信号留在缓冲区，下次开跑时被消费掉——
// 故 runOnce 开跑前需先清缓冲，避免"上一轮的让路要求"误伤下一轮）。
func (s *Scheduler) YieldInFlight() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.yielding = true
	y := s.yieldRound()
	s.mu.Unlock()
	select {
	case y <- struct{}{}:
	default: // 已有未消费的让路请求，够用
	}
}

// RoundYielded 报告当前在途轮是否已被要求让路（诊断/日志用）。
func (s *Scheduler) RoundYielded() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.yielding
}

// clearYield 清掉让路请求与标志（每轮开跑前调用）。
func (s *Scheduler) clearYield() {
	s.mu.Lock()
	s.yielding = false
	if s.yield != nil {
		select {
		case <-s.yield:
		default:
		}
	}
	s.mu.Unlock()
}

// TryYield 非阻塞检查让路信号（供 Worker 装配为让路检查点）。
func (s *Scheduler) TryYield() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	y := s.yieldRound()
	s.mu.Unlock()
	select {
	case <-y:
		s.mu.Lock()
		s.yielding = false
		s.mu.Unlock()
		return true
	default:
		return false
	}
}

// NewScheduler 校验并填默认值。Round 缺失即报错——调度器没有"静默什么都不做"的余地。
func NewScheduler(opt SchedOptions) (*Scheduler, error) {
	if opt.Round == nil {
		return nil, errors.New("scheduler: Round 必填")
	}
	if opt.Idle <= 0 {
		opt.Idle = DefaultSchedIdle
	}
	if opt.Probe <= 0 {
		opt.Probe = DefaultSchedProbe
	}
	if opt.MaxBackoff <= 0 {
		opt.MaxBackoff = DefaultSchedMaxBackoff
	}
	if opt.FirstDelay <= 0 {
		opt.FirstDelay = DefaultSchedFirstDelay
	}
	if opt.MaxBackoff < opt.Idle {
		opt.MaxBackoff = opt.Idle
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	return &Scheduler{
		opt:   opt,
		wake:  make(chan struct{}, 1),
		done:  make(chan struct{}),
		yield: make(chan struct{}, 1),
	}, nil
}

// Start 起调度循环。ctx 取消或 Stop 调用即收敛。重复调用幂等（已在跑则只 Wake 一次）。
func (s *Scheduler) Start(ctx context.Context) error {
	if s == nil || ctx == nil {
		return errors.New("scheduler: Start 需要非空 ctx")
	}
	s.mu.Lock()
	if s.st.Started {
		s.mu.Unlock()
		s.Wake()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	// ⭐ v21.203：每次 Start **重建 done**。原实现只在 NewScheduler 建一次，
	// 而 `loop` 的 `defer close(s.done)` 会在退出时关掉它 —— 第二次 Start
	// 会让新 loop 再 close 同一个已关闭的 channel ⇒ `panic: close of closed
	// channel`，把整个进程带走。
	// 场景真实存在：Stop() 后再 Start()（本轮新增的关机/重启语义会走到，
	// 未来任何"重新启用自动同步"的入口也会走到）。
	s.done = make(chan struct{})
	s.st.Started = true
	s.st.NextAt = time.Now() // 首轮即刻（登录后不该干等 5 分钟）
	s.mu.Unlock()

	go s.loop(runCtx)
	s.Logf("后台同步调度已启动（空闲 %s / 探测 %s / 退避上限 %s）", s.opt.Idle, s.opt.Probe, s.opt.MaxBackoff)
	return nil
}

// Stop 停止调度并等在途轮次收敛（Round 需尊重 ctx）。幂等：未启动/已停止都直接返回。
func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.st.Started {
		s.mu.Unlock()
		return
	}
	s.st.Started = false
	s.st.Running = false
	s.st.NextAt = time.Time{}
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// 兜底时限：v21.203 从 35s 提到 60s，理由是"Round 全部尊重 ctx"。
	// ⭐ v21.211（210 实机：点 X 后进程 72 秒不退出）**降到 15s**，因为两个前提
	// 现在都真正成立了：
	//  ① 调度器绑的是**进程级 appCtx**，关机 `cancelShutdown()` 广播即取消在途轮
	//     （此前传 l.ctx，取消广播到不了 ⇒ 这里每次都只能白等满 60s 兜底，
	//     实机日志两次「调度器未在预期时限内退出」）；
	//  ② Round 链路（State/Pull/Refresh）已全部走带 ctx 的通道，ctx 一取消，
	//     在途 HTTP 往返立即中断（秒级），不再等 `http.Client.Timeout`。
	// 弱网 + 大库的最坏情况（换代写代库fsync）也已由 v21.212 的**让路信号**
	// 从"等在途轮跑完"降为"在途轮在分页边界即止"，且换票锁已可取消
	// （v21.212 exchangeMu 改 channel 语义锁）⇒ 8s 有充分余量，且仍是有界的。
	select {
	case <-s.done:
	case <-time.After(8 * time.Second):
		s.Logf("调度器未在预期时限内退出，已放弃等待（DB/日志即将关闭，此后若有写入会记为 closed）")
	}
}

// Wake 请求立刻跑一轮（登录/回前台/手工同步/换库/导入之后调用）。非阻塞；
// 在途时只留一格，因此**多次 Wake 合并为至多一次额外轮次**。
func (s *Scheduler) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// State 返回状态快照。
func (s *Scheduler) State() SchedState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// Logf 统一走注入的日志器（默认丢弃），避免各处判空。
func (s *Scheduler) Logf(format string, args ...any) {
	if s.opt.Logf != nil {
		s.opt.Logf(format, args...)
	}
}

func (s *Scheduler) loop(ctx context.Context) {
	defer close(s.done)
	// 单只可复用定时器：循环内每轮新建 timer + defer Stop 会让 defer 累积到函数退出才执行
	// （常驻 goroutine 里就是句柄/内存泄漏），故手工管 Reset/Stop 并在每轮开头清残留。
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-timer.C: // 上一轮已到期但被 Wake 抢先消费过：清掉，避免 hold 时误触发
		default:
		}
		wait, hold := s.planWait()
		var tick <-chan time.Time
		if !hold {
			if s.opt.After != nil { // 测试注入的等待函数
				tick = s.opt.After(wait)
			} else {
				timer.Reset(wait)
				tick = timer.C
			}
		} // hold 时 tick 为 nil 通道 = 只等 Wake（自动轮次已停手）
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.runOnce(ctx, true) // 终态停手期间唯一的重试入口（"用户动作再试一次"）
		case <-tick:
			s.runOnce(ctx, false)
		}
	}
}

// planWait 计算下一轮等待时长。hold=true 表示"自动轮次已停手"，只等 Wake。
//
// ⭐ v21.221（214 实机定案）：**NeedAttention 不再导致 hold（永久停手）**。
//
// 定罪：原实现 `if s.st.NeedAttention { return 0, true }` —— hold 使 loop 里
// `tick = nil 通道` ⇒ 定时器分支永不就绪 ⇒ **只有 Wake 能触发下一轮**。
// 而终态错误（epoch/resync/authRevoked）在弱网 + outbox 积压下极易出现
// ⇒ 214 实机「关闭重开后永久『恢复中』、笔记列表空」即由此而来。
//
// 为什么必须改：「需要用户注意」是**提示义务**，「停止自动尝试」是**可用性自杀**。
// 数据类终态错误在换代 / 手工同步 / 网络恢复后完全可能自愈。
// 现在改为：NeedAttention 只影响**退避时长**（长退避）与 UI 提示，不夺走自动恢复能力。
//
// ⚠ 保留 hold 的唯一情形：调度器显式 Stop（由 stopScheduler 走另一条路径，不经此处）。
func (s *Scheduler) planWait() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Rounds == 0 { // 启动后首轮不等 Idle（登录/恢复挂载后应立即收敛一次）
		s.st.NextAt = time.Now().Add(s.opt.FirstDelay)
		return s.opt.FirstDelay, false
	}
	d := s.nextDelayLocked()
	s.st.NextAt = time.Now().Add(d)
	return d, false
}

// terminalBackoff 终态错误的退避阶梯：5min → 15min → 30min（封顶）。
//
// 取值理由：终态错误通常需要"换代/手工同步/凭据重登"这类**用户或系统级动作**才自愈，
// 短退避只会白跑（每轮都要换票 + 拉全量，网络与 CPU 双重浪费）；
// 但也不能太长，否则用户改完配置后要干等很久才见效。30 分钟封顶是可接受的兜底节奏，
// 且用户任何一次「立即同步 / 重新登录」都能通过 Wake 立刻打破退避。
func terminalBackoff(failures int) time.Duration {
	switch {
	case failures <= 1:
		return 5 * time.Minute
	case failures == 2:
		return 15 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func (s *Scheduler) nextDelayLocked() time.Duration {
	// ⭐ v21.221：NeedAttention 走**长退避阶梯**，不再让调度器停手。
	if s.st.NeedAttention {
		return terminalBackoff(s.st.ConsecutiveFailures)
	}
	if s.st.Offline {
		return s.opt.Probe
	}
	if s.st.ConsecutiveFailures == 0 {
		return s.opt.Idle
	}
	n := s.opt.Idle << uint(s.st.ConsecutiveFailures-1)
	if n <= 0 || n > s.opt.MaxBackoff { // 位移溢出也走封顶
		n = s.opt.MaxBackoff
	}
	return n
}

// runOnce 执行一轮。inline=true 表示由 Wake 触发（终态停手时允许的那一次重试）。
func (s *Scheduler) runOnce(ctx context.Context, inline bool) {
	s.mu.Lock()
	if s.st.Running {
		s.mu.Unlock()
		return // 结构性单飞（循环内联执行，这里只是纵深防御）
	}
	if !s.st.Started {
		s.mu.Unlock()
		return
	}
	s.st.Running = true
	// ⭐ v21.221：不再因 NeedAttention 阻断自动轮次（214 实机永久「恢复中」的成因之一）。
	// 该判定在 planWait 已彻底移除，此处仅保留变量以兼容未来显式停手路径。
	hold := false
	s.mu.Unlock()

	// ⭐ v21.212：每轮开跑前清掉上一轮遗留的让路请求。
	// 否则「换代窗口广播的让路」会被下一轮误消费，导致开跑即退（同步静默停摆）。
	s.clearYield()

	// 纵深防御：hold 期间 tick 是 nil 通道，正常不可能走到这里；真到了（未来改动）就静默跳过，
	// 绝不把"跳过"计成失败——那会污染退避计数并让 UI 误报。
	if hold && !inline {
		s.mu.Lock()
		s.st.Running = false
		s.mu.Unlock()
		return
	}
	if s.opt.Allow != nil && !s.opt.Allow() {
		// 不允许 = 本轮什么都不做且**不计失败**（离线单机态是正常状态，不是错误）
		s.mu.Lock()
		s.st.Running = false
		s.mu.Unlock()
		return
	}

	err := s.roundSafely(ctx)
	s.finishRun(ctx, err)
}

// roundSafely 把业务 panic 关在一轮之内：UI 绑定的换票/入队路径都可能踩到空句柄，
// 但绝不允许把调度 goroutine 带走——那会让"自动同步"从此静默消失（最难发现的一类失效）。
func (s *Scheduler) roundSafely(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("同步轮次 panic: %v", r)
			s.Logf("后台同步轮次 panic 已捕获: %v", r)
		}
	}()
	return s.opt.Round(ctx)
}

func (s *Scheduler) finishRun(ctx context.Context, err error) {
	out := classifyRoundErr(err)
	s.mu.Lock()
	s.st.Running = false
	s.st.Rounds++
	now := time.Now()
	switch {
	case err == nil:
		s.st.ConsecutiveFailures = 0
		s.st.Offline = false
		s.st.NeedAttention = false
		s.st.ReloginRequired = false
		s.st.LastError = ""
		s.st.LastOKAt = now
	// ⭐ v21.221（214 实机定案）：**终态错误不再永久停手**，改长退避继续自动尝试。
	//
	// 214 实机症状：桌面端首次同步正常、关闭重开后永久「恢复中」，笔记列表空。
	// 取证坐实：`classifyRoundErr` 把某类错误判为 outcomeTerminal ⇒
	// NeedAttention=true ⇒ `planWait` 返回 `hold=true` ⇒ **定时器变成 nil 通道**
	// ⇒ 自动轮次**永不再跑**，只有用户手动点按钮才 Wake。
	// 于是 UI 永远停在 `online=false` =「恢复中」永不变化。
	//
	// ⚠ 设计错误：「需要用户注意」是**提示义务**，不是「可用性自杀」。
	// 数据类终态错误（epoch/resync）退避后完全可能自愈（换代 / 手工同步 / 网络恢复），
	// 永久停手等于把可自愈的故障变成不可自愈。
	//
	// 现在的语义：
	//   · **仍然置 NeedAttention**（UI 照旧显示醒目提示，不隐藏问题）；
	//   · 但**不再 hold**，改为按 terminalBackoff 长退避后自动再试（5→15→30min 封顶）。
	case out == outcomeTerminal:
		s.st.NeedAttention = true
		s.st.Offline = false
		s.st.ConsecutiveFailures++
		s.st.LastError = err.Error()
	// ⭐ v21.221：凭据作废单独处置 —— 这是**唯一**真正需要用户主动重登的情况。
	// 同样不永久停手（用户可能已在别处重登、稍后网络恢复即自愈），
	// 但置 ReloginRequired 让 UI 明确显示「需要重新登录」而非含糊的「恢复中」。
	case out == outcomeAuthRevoked:
		s.st.NeedAttention = true
		s.st.ReloginRequired = true
		s.st.Offline = false
		s.st.ConsecutiveFailures++
		s.st.LastError = err.Error()
	case out == outcomeTransport:
		s.st.Offline = true
		s.st.NeedAttention = false
		s.st.ReloginRequired = false
		s.st.ConsecutiveFailures++
		s.st.LastError = err.Error()
	default:
		s.st.Offline = false
		s.st.NeedAttention = false
		s.st.ReloginRequired = false
		s.st.ConsecutiveFailures++
		s.st.LastError = err.Error()
	}
	if !now.IsZero() {
		s.st.LastRoundAt = now
	}
	st := s.st
	s.mu.Unlock()

	if err != nil {
		s.Logf("后台同步轮次失败（第 %d 次连续）: %v", st.ConsecutiveFailures, err)
	}
	if s.opt.OnRound != nil {
		s.opt.OnRound(err, st)
	}
}

// ---- 错误分类（ADR-005 §一.3：拿协议终态去退避重试等于周期性打服务端）----

type roundOutcome int

const (
	outcomeNone roundOutcome = iota
	outcomeTransport
	outcomeTerminal
	outcomeRetry
	// ⭐ v21.221：凭据作废（服务端 401 权威拒绝）**单独成类**。
	//
	// 分家的理由：它与 epoch/resync 类终态错误的**性质完全不同**——
	//   · epoch/resync = 「本地与服务端代际不一致」⇒ 数据问题，退避后会自愈；
	//   · authRevoked  = 「这张票被服务端作废了」⇒ **本地怎么等都不会好**，
	//     唯一出路是重新登录。
	// 二者共用同一个 `outcomeTerminal` ⇒ 处置也共用（见下方 NeedAttention），
	// 于是"必须重登"和"等等就好"被同等对待，UI 也无法给出正确引导。
	outcomeAuthRevoked
)

func classifyRoundErr(err error) roundOutcome {
	if err == nil {
		return outcomeNone
	}
	switch {
	// ⭐ v21.221：先判 AUTH_REVOKED（必须排在 epoch/resync 之前：
	// 服务端拒绝时两者可能同时被包装，且"要重登"是更强、更可执行的那个结论）。
	case errors.Is(err, ErrAuthRevoked):
		return outcomeAuthRevoked
	case errors.Is(err, ErrEpochChanged), errors.Is(err, ErrResyncNeeded):
		return outcomeTerminal
	}
	if isTransportErr(err) {
		return outcomeTransport
	}
	return outcomeRetry
}

// isTransportErr 判定"连不上"类失败：网络栈错误、DNS、超时、连接被拒、EOF（服务端掉线）。
// 客户端各层既有 *url.Error 包装，也有 plain string 错误，故两层都认。
func isTransportErr(err error) bool {
	if err == nil {
		return false
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, kw := range []string{"no such host", "dial tcp", "connection refused", "i/o timeout",
		"network is unreachable", "connection reset", "tls handshake", "eof", "temporarily unavailable"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// WaitIdle 等在途轮次真正结束（v21.170）。
//
// 背景：换代（BootstrapNow）要关闭旧库句柄，而 database/sql 的 DB.Close()
// 会**立刻**让后续所有查询返回 sql: database is closed。官方明确要求
// 「close 只应在没有 goroutine 会再碰它之后调用」—— 切换句柄时必须等活跃查询结束再关。
//
// 旧行为为什么不够（209 实机教训）：pauseScheduler() 只是 syncPauseDepth 计数器，
// 只拦得住**新**轮；而 scheduledRound 里 w := workerPtr() 已把旧 worker 存进
// **局部变量**，随后才 w.RunOnce() —— 摘指针/暂停都管不到这已在途的一轮。
// 于是换代关库瞬间那一轮正好撞上 database is closed（真机日志同秒两条）。
//
// 语义：true = 空闲（可安全关库）；false = 超时仍有轮次在跑（调用方应放弃换代，
// 绝不能强关旧库——那正是本缺陷的成因）。
func (s *Scheduler) WaitIdle(ctx context.Context, timeout time.Duration) bool {
	if s == nil {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		running := s.st.Running
		s.mu.Unlock()
		if !running {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
			// 轮询 tick：不依赖轮次走到特定广播路径（panic 早退也算结束，下轮即读到 false）
		}
	}
}
