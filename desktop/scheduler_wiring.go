package main

// scheduler_wiring.go —— 后台同步调度器接线（批次 8-Alt 刀 3；语义与取舍见 ADR-005）。
//
// 两条硬规矩（都写进注释是因为它们**没有别的兜底**）：
//  1. 调度器只通过闭包拿 worker，永不捕获 *Worker —— syncWorker 被六处改写（mountWorker /
//     demoteToRelogin / Logout / PortableImport.RebindDB / BootstrapNow 换库 / restore 挂载），
//     捕获指针 = 换库后往旧库句柄推同步（批次 4"恢复不挂工→漏账"同族）。
//  2. 换库窗口必须 `withSyncPaused` 包住：调度轮若在 Import/Activate 中途落进来，会对着
//     正在被关闭/替换的句柄跑 push，最坏是丢一笔已记账的推送。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stickynote-desktop/bootstrap"
	"stickynote-desktop/synclocal"
	"stickynote/database"
)

// ErrScheduleNoWorker 允许门与执行之间的竞态兜底（极窄窗口：Allow 刚过就被 Logout）。
var ErrScheduleNoWorker = errors.New("同步引擎未挂载，已跳过本轮")

var (
	syncScheduler *synclocal.Scheduler

	// syncPauseDepth 换库/导入/Bootstrap 窗口的**嵌套深度**（v21.203 由 bool 改计数器）。
	//
	// 定罪（审计）：原为 `atomic.Bool`，而暂停窗口是**会嵌套**的 ——
	// `scheduledRound` 的 auto-bootstrap 分支先 `pauseScheduler()` 再调
	// `BootstrapNow`，后者内部又 `pauseScheduler() ... defer resumeScheduler()`。
	// bool 语义下，内层的 resume 会把外层的窗口一并解除 ⇒ auto-bootstrap 还在
	// 换代收尾（重挂 worker / 换全局句柄）时，调度器已被放行一轮，
	// 那一轮对着**正在被 Close 的旧句柄**跑 push（最坏丢一笔已记账的推送）。
	// 计数器语义下只有深度归零才真正解除。
	//
	// 读侧（allowScheduledRound / SyncPaused 绑定）仍按「>0 即暂停」判定，
	// 对外可见行为与原来一致，只是嵌套变正确。
	syncPauseDepth atomic.Int32

	// ---- v21.201 A1：auto-bootstrap 熔断器 ----
	//
	// 定罪（用户实机 19:01~19:05）：同一进程内 bootstrap 被反复触发
	//（19:02:17 g-000002 → 19:03:02 g-000003，两代 watermark 都是 253 —— 服务端
	// 根本没变），用户表现为「要登录很多次 + 期间必须多次关窗口」。
	//
	// 为什么必须有熔断而不能只修判定：判定错误可能来自服务端、代理、Cloudflare、
	// 客户端句柄任一层，**客户端无法保证自己永远能判对**。判定失效时若仍无限
	// 自动重建，等于把用户锁进「重建 → 中断 → 重建」死循环，且每次换代都
	// 是一次全库重写（数据风险 + 流量风险 + 磁盘风险）。
	//
	// 熔断策略（两级）：
	//  1. 冷却：bootstrap 成功后 autoBootstrapCooldown 内不再自动触发
	//     （epoch 真变了会在下一轮 pull 由 ErrEpochChanged 正常升级，不受影响）。
	//  2. 环��：连续 autoBootstrapLoopLimit 次「bootstrap 成功但紧随其后仍判
	//     epoch/resync」⇒ 判定为重建循环，永久停止自动 bootstrap（本次进程内）
	//     并置 tripped，让 UI 明示用户；手动「强制全量同步」不受熔断影响。
	autoBootMu       sync.Mutex
	autoBootLast     time.Time
	autoBootStreak   int
	autoBootTripped  bool
	autoBootTrippedR bool // tripped 的对外只读快照（给绑定层展示）

	// ⭐ v21.218（212 实机深度核查）：**失败短退避**状态。
	// 与上面「成功长冷却（autoBootLast + autoBootstrapCooldown）」是两套独立机制：
	// 成功 → 5 分钟不再自动重建；失败 → 30s~2min 后自动重试。
	// 原实现失败也写 autoBootLast ⇒ 一次网络失败即停摆 5 分钟（212 实机症状）。
	autoBootFailStreak int
	autoBootNextRetry  time.Time

	// autoBootstrapInternal 标记本次 BootstrapNow 由**自动重建路径**发起
	//（调度器 auto-bootstrap / 登录后自动快照）。用于让 BootstrapNow 区分
	// 「用户手动强制同步」（应清熔断）与「自动重建」（不得清熔断，
	// 否则熔断器被自己解除 = 没装）。
	autoBootstrapInternal atomic.Bool
)

// withInternalBootstrap 把随后的一次 BootstrapNow 标记为自动重建来源。
func withInternalBootstrap() { autoBootstrapInternal.Store(true) }

// clearInternalBootstrap 在自动路径的 BootstrapNow 返回后清标记
// （必须在 defer 里，保证成功/失败两条路都清）。
func clearInternalBootstrap() { autoBootstrapInternal.Store(false) }

const (
	// autoBootstrapCooldown 两次自动 bootstrap 的最小间隔。
	// 定版依据：快照在本机实测3s / 1s 完成（19:02、19:03 两轮日志），
	// 但生产有大库 + 弱链路，取 5 分钟可覆盖"网络抖动导致的误判"，又不会
	// 让真实的代际变更等太久（真实变更最多推迟一个冷却窗口）。
	autoBootstrapCooldown = 5 * time.Minute
	// autoBootstrapLoopLimit 连续多少次「bootstrap 成功但仍判 epoch/resync」
	// 判定为重建循环并熔断。
	autoBootstrapLoopLimit = 2
	// ⭐ v21.218：失败退避封顶（2 分钟）。
	// 依据：网络类失败通常几十秒内自愈；封顶 2 分钟保证即便长时间失败也仍在
	// 用户可感知的重试节奏上，不需要手动干预。
	autoBootstrapFailBackoffMax = 2 * time.Minute
)

// autoBootstrapAllow 返回是否允许此刻触发自动 bootstrap，并更新熔断状态。
// ok=false 时 tripped=true 表示已熔断（调用方应停止尝试并提示用户）。
func autoBootstrapAllow(now time.Time) (allow bool, tripped bool) {
	autoBootMu.Lock()
	defer autoBootMu.Unlock()
	if autoBootTripped {
		return false, true
	}
	// ⭐ v21.218：失败短退避优先判定（成功后 autoBootNextRetry 归零，不受影响）。
	if !autoBootNextRetry.IsZero() && now.Before(autoBootNextRetry) {
		return false, false // 退避中：静默跳过，不计 streak（不是循环证据）
	}
	if !autoBootLast.IsZero() && now.Sub(autoBootLast) < autoBootstrapCooldown {
		return false, false // 成功冷却中：静默跳过，不计streak（不是循环证据）
	}
	return true, false
}

// autoBootstrapDone 在一次自动 bootstrap 结束后记账。
// stillMisjudged=true 表示这一轮 bootstrap 刚成功、随后的调度轮又判了
// epoch/resync —— 这正是「重建循环」的确定性证据。
//
// ⭐ v21.218（212 实机深度核查）：**失败与成功分档退避**。
//
// 定罪：原实现无论成败都写 `autoBootLast = now`，而 `autoBootstrapAllow` 见到
// 距上次不足 autoBootstrapCooldown（**5 分钟**）就跳过 ⇒ **一次网络失败就让自动快照
// 停摆 5 分钟**。212 实机正是这个形态：网络慢/抖 → 首次快照失败 → 接下来 5 分钟内
// 调度轮全部跳过 bootstrap → 笔记持续空白 → 用户以为「同步坏了」并反复重登。
//
// 修法：失败走**短退避**（30s 起、指数递增、上限 2 分钟）。理由：
//
//	· 网络类失败通常几十秒内自愈（实测 212 日志「换票首次网络失败，重试成功」）；
//	· 短退避仍远大于一次快照耗时，不构成重建风暴；
//	· 只有**成功**才值得进入 5 分钟长冷却（成功后确实不该再重建）；
//	· 失败**不计入** streak（失败不是"循环"证据），熔断语义保持不变。
func autoBootstrapDone(now time.Time, stillMisjudged bool) {
	_ = now // v21.218：仅成功路径才写 autoBootLast（长冷却），失败走 autoBootstrapFailed 的短退避
	autoBootMu.Lock()
	defer autoBootMu.Unlock()
	if !stillMisjudged {
		autoBootStreak = 0
		autoBootFailStreak = 0
		autoBootNextRetry = time.Time{} // 成功即解除失败退避
		autoBootLast = now
		return
	}
	// 成功但仍判代际不一致：仍是"循环"证据，但本轮确实换过代 ⇒ 记长冷却。
	autoBootNextRetry = time.Time{}
	autoBootLast = now
	autoBootStreak++
	if autoBootStreak >= autoBootstrapLoopLimit {
		autoBootTripped = true
		autoBootTrippedR = true
		log.Printf("[bootstrap] ⚠ 检测到重建循环（连续 %d 次 bootstrap 后仍判代际不一致）："+
			"已停止自动重建，请用【账号与设备 → 强制全量同步】或重启程序恢复"+
			"（手动操作不受熔断影响）", autoBootStreak)
	}
}

// autoBootstrapFailed 记账**一次失败的自动快照**：只推进短退避，不动熔断 streak。
//
// 与 autoBootstrapDone 分离的原因：失败路径必须**不写 autoBootLast**，
// 否则会命中 5 分钟长冷却（这正是 212 实机「失败一次就停摆」的机制）。
func autoBootstrapFailed() {
	autoBootMu.Lock()
	defer autoBootMu.Unlock()
	autoBootFailStreak++
	if autoBootFailStreak > 12 {
		// 防御性上限：长时间失败（可能服务端真的坏了）退避到上限即可，
		// 交给用户手动「强制全量同步」，不必永久熔断（失败不构成重建循环证据）。
		autoBootFailStreak = 12
	}
	autoBootNextRetry = time.Now().Add(autoBootstrapRetryBackoff(autoBootFailStreak))
	log.Printf("[bootstrap] 自动快照失败，退避 %s 后自动重试（失败不计入熔断）",
		time.Until(autoBootNextRetry).Round(time.Second))
}

// autoBootstrapRetryBackoff 失败退避时长：30s 起，指数递增，封顶 2 分钟。
func autoBootstrapRetryBackoff(streak int) time.Duration {
	if streak < 1 {
		streak = 1
	}
	d := 30 * time.Second
	for i := 1; i < streak && d < autoBootstrapFailBackoffMax; i++ {
		d *= 2
	}
	if d > autoBootstrapFailBackoffMax {
		d = autoBootstrapFailBackoffMax
	}
	return d
}

// AutoBootstrapTripped 供绑定层/UI 读取熔断状态。
func AutoBootstrapTripped() bool {
	autoBootMu.Lock()
	defer autoBootMu.Unlock()
	return autoBootTrippedR
}

func autoBootstrapTrippedSnapshot() bool {
	autoBootMu.Lock()
	defer autoBootMu.Unlock()
	return autoBootTrippedR
}

// autoBootstrapRetryRemaining 返回失败退避的剩余时间。
// 成功一次即清零（autoBootstrapDone 里 autoBootNextRetry 被重置），故此处为
// 纯读快照，与 autoBootstrapCooldownRemaining 对称。
func autoBootstrapRetryRemaining(now time.Time) (time.Duration, bool) {
	autoBootMu.Lock()
	defer autoBootMu.Unlock()
	if autoBootNextRetry.IsZero() || !now.Before(autoBootNextRetry) {
		return 0, false
	}
	return autoBootNextRetry.Sub(now), true
}

// autoBootstrapCooldownRemaining 冷却剩余时长；cooling=false 表示不在冷却
// （从未触发过、已过冷却、或已熔断 —— 熔断时不该再报冷却）。
func autoBootstrapCooldownRemaining(now time.Time) (d time.Duration, cooling bool) {
	autoBootMu.Lock()
	defer autoBootMu.Unlock()
	if autoBootTripped || autoBootLast.IsZero() {
		return 0, false
	}
	if rem := autoBootstrapCooldown - now.Sub(autoBootLast); rem > 0 {
		return rem, true
	}
	return 0, false
}

// verifyAnchorsAfterTakeover v21.201 A2：换代完成后**立刻**自检锚点。
//
// 为什么必须有：此前换代"成功"的唯一证据是 `rn.Run` 返回 nil，
// 而下一次调度轮（实测 44 秒后）才发现 `RunOnce` 仍判 epoch/resync ——
// 中间那44 秒用户看到的是"界面正常但数据不更新"，且日志里只有一个
// 事后才知道的 `[sched] epoch/resync`。把比对前移到换代收口处，
// 能在**同一轮**就把差值（哪个键、差多少）打进日志。
//
// 返回 true = 不一致（调用方据此判定重建循环）。
func (l *LocalService) verifyAnchorsAfterTakeover() bool {
	if workerPtr() == nil {
		return false
	}
	st, err := workerPtr().State()
	if err != nil {
		// state 都拿不到（网络/代理/服务端不可达）⇒ 无法判定为"重建循环"，
		// 返回 false 让熔断器不计数：把网络故障误判成循环会导致同步永久停摆。
		log.Printf("[bootstrap] 接管后锚点自检跳过：state 不可达（%v）", err)
		return false
	}
	db := database.GetDB()
	if db == nil {
		return false
	}
	epochS, _ := synclocal.StateValue(db, "server_epoch")
	instS, _ := synclocal.StateValue(db, "server_instance_id")
	cursorS, _ := synclocal.StateValue(db, "cursor")
	var diffs []string
	if epochS != fmt.Sprint(st.ServerEpoch) {
		diffs = append(diffs, fmt.Sprintf("server_epoch 本地=%s 服务端=%d", epochS, st.ServerEpoch))
	}
	if instS != st.ServerInstanceID {
		diffs = append(diffs, fmt.Sprintf("server_instance_id 本地=%s 服务端=%s", instS, st.ServerInstanceID))
	}
	cur, _ := strconv.ParseInt(cursorS, 10, 64)
	if cur < st.MinAvailableChangeSeq {
		diffs = append(diffs, fmt.Sprintf("cursor 本地=%d 服务端min_available=%d", cur, st.MinAvailableChangeSeq))
	}
	if len(diffs) == 0 {
		log.Printf("[bootstrap] 接管后锚点自检 OK（epoch=%d cursor=%d instance=%s）",
			st.ServerEpoch, cur, shortInst(st.ServerInstanceID))
		return false
	}
	log.Printf("[bootstrap] ⚠ 接管后锚点自检不一致（%d 项）：%s", len(diffs), strings.Join(diffs, " | "))
	return true
}

// shortInst 把 UUID 缩到首段用于日志。
//
// 只在**严格 UUID 形状**（8-4-4-4-12 全十六进制）时截断 —— 早期用
// `IndexByte(s,'-')` 会把 `no-dash` 这类非UUID 串误截成 `no`（回归钉已拦下）。
func shortInst(s string) string {
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return s
	}
	for i, want := range []int{8, 4, 4, 4, 12} {
		if len(parts[i]) != want || !isHex(parts[i]) {
			return s
		}
	}
	return parts[0]
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return len(s) > 0
}

// resetAutoBootstrap 手动强制同步成功后清熔断状态（用户已主动处置）。
//
// ⚠️ 必须**同时清 autoBootLast**（冷却计时）：否则用户点完【强制全量同步】，
// 熔断虽解，但冷却期仍会把随后的自动重建挡 5 分钟 —— 用户刚处置完却仍看到
// "自动重建被跳过"，等于没修（回归钉 TestManualPathClearsTrip 已拦下）。
func resetAutoBootstrap() {
	autoBootMu.Lock()
	defer autoBootMu.Unlock()
	autoBootStreak = 0
	autoBootTripped = false
	autoBootTrippedR = false
	autoBootLast = time.Time{}
}

// pauseScheduler / resumeScheduler 换库窗口配对（Import 双代激活、Bootstrap 换句柄）。
// 用法固定为 `pauseScheduler(); defer resumeScheduler()` 紧贴换库段——调度轮若在中途落进来，
// 会对着正在被关闭/替换的句柄跑 push，最坏丢一笔已记账的推送。
//
// v21.203：改为**嵌套计数器**（原 bool 的嵌套解除缺陷见 syncPauseDepth 注释）。
// 多resume 少 pause 的调用点错误会被夹紧在 0，不会把深度减成负数。
func pauseScheduler() { syncPauseDepth.Add(1) }

// resumeScheduler 解除一层暂停，深度归零时立刻敲一次：失败路径也要敲，
// 否则挂账会静默躺平。**只有真正归零才敲**（内层解除不该唤醒调度器）。
func resumeScheduler() {
	if syncPauseDepth.Add(-1) > 0 {
		return
	}
	if syncPauseDepth.Load() < 0 {
		syncPauseDepth.Store(0) // 夹紧：多 resume 的调用点 bug 不应让深度变负
	}
	wakeScheduler()
}

// startScheduler 幂等启动（Startup 调用；重复调用只会合并成一次唤醒）。
func startScheduler(l *LocalService) {
	if l == nil {
		return
	}
	if syncScheduler == nil {
		s, err := synclocal.NewScheduler(synclocal.SchedOptions{
			Idle: 60 * time.Second, Probe: 60 * time.Second, MaxBackoff: 30 * time.Minute,
			FirstDelay: 2 * time.Second,
			Round:      l.scheduledRound,
			Allow:      allowScheduledRound,
			Logf:       func(f string, a ...any) { log.Printf("[sched] "+f, a...) },
			OnRound: func(err error, st synclocal.SchedState) {
				if err == nil {
					return
				}
				// 后台轮次拿到 401 类终态：与手工 SyncNow 同一降级口径（只卸工不清封存），
				// 否则用户看到的是"自动同步一直在跑却没反应"。
				if errors.Is(err, synclocal.ErrAuthRevoked) {
					l.demoteToRelogin("后台同步期间凭据失效")
				}
			},
		})
		if err != nil {
			log.Printf("[sched] 调度器创建失败（自动同步不可用，手工同步不受影响）: %v", err)
			return
		}
		syncScheduler = s
	}
	// ⭐ v21.211（210 实机铁证·点 X 后进程 72 秒不退出）：
	//
	// 此前传的是 `l.ctx`（Wails 启动上下文）。而 `gracefulShutdown` 的
	// `cancelShutdown()` 广播取消的是 `appCtx()` —— **两个不同的 ctx**，
	// 于是取消广播根本到不了调度器：`Stop()` 只能干等它自己那个 60s 兜底超时。
	// 实机日志两次「[sched] 调度器未在预期时限内退出」+ 12s 的
	// 「换代在关机窗口内未收尾」= 60 + 12 = 72 秒，与用户「需要很长时间」完全吻合。
	//
	// 修法：调度器绑 **进程级 appCtx**，与所有长任务同根。这样 cancelShutdown
	// 一广播，在途轮的 ctx 立刻取消（P1 传输层 ctx 贯通后是**秒级**响应），
	// Stop 的兜底超时从 60s 降到 15s（见 scheduler.go）也能安全成立。
	ctx := appCtx()
	if err := syncScheduler.Start(ctx); err != nil {
		log.Printf("[sched] 启动失败: %v", err)
	}
}

// stopScheduler 收敛调度 goroutine（OnShutdown / 测试拆工装用）。幂等。
func stopScheduler() {
	if syncScheduler != nil {
		syncScheduler.Stop()
	}
}

// wakeScheduler 请求立刻走一轮（登录/换库/手工同步/回前台之后）。未启动时静默无害。
func wakeScheduler() {
	if syncScheduler != nil {
		syncScheduler.Wake()
	}
}

// lastNetworkFailure 最近一次**网络/代理类**失败的摘要（供 UI 展示，P0-3）。
//
// 定罪（用户实机 2026-10-10 10:20:05）：bootstrap 失败只打进日志
// （`[bootstrap] 自动快照失败: ...TLS handshake timeout`），
// **用户侧完全不可见** ⇒ 表现为"登录后白屏 + 没有任何提示"，
// 用户无从判断是网络问题还是软件坏了。
//
// 现在把最近一次网络类失败提炼成一句可执行的中文，经既有
// `SyncScheduleStatus` 返回（**零新增绑定**），账号卡片直接显示。
var (
	lastNetFailMu   sync.Mutex
	lastNetFailText string
	lastNetFailAt   time.Time
)

// recordNetworkFailure 记一次网络/代理类失败（非网络类不记，避免误导）。
func recordNetworkFailure(err error) {
	if err == nil {
		return
	}
	if !errors.Is(err, synclocal.ErrNetworkUnreachable) {
		return // 业务错误（账密/验证码/权限）不该显示成"检查网络"
	}
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, "NETWORK_UNREACHABLE") {
		msg = msg[i+2:]
	}
	lastNetFailMu.Lock()
	lastNetFailText = msg
	lastNetFailAt = time.Now()
	lastNetFailMu.Unlock()
	log.Printf("[net] 网络类失败已记录（供 UI 展示）: %s", msg)
}

// clearNetworkFailure 网络恢复后清掉（成功跑一轮就清，否则横幅会一直挂着）。
func clearNetworkFailure() {
	lastNetFailMu.Lock()
	lastNetFailText = ""
	lastNetFailAt = time.Time{}
	lastNetFailMu.Unlock()
}

// networkFailureSnapshot 供 SyncScheduleStatus 组装。
func networkFailureSnapshot() (string, string) {
	lastNetFailMu.Lock()
	defer lastNetFailMu.Unlock()
	if lastNetFailText == "" {
		return "", ""
	}
	return lastNetFailText, lastNetFailAt.Format(time.RFC3339)
}

// allowScheduledRound 换库窗口不跑；此外"有票"或"设备会话可恢复（空票）"都允许跑。
//
// ⚠️ v21.142 修死锁：此前条件是裸 `workerHasToken(syncWorker)` —— 而**冷启动恢复的设备会话
// 是空票挂工的**（access 只存内存 §87，重启即失；`restoreDeviceSession` 用 access="" 挂工）。
// 于是每一轮都被这道门挡掉 → `scheduledRound` 里的 `ensureAccess`（惰性换票）**永远不会被执行**
// → 永不换票 → 永不同步；而 `restoreDeviceSession` 又因 access=="" 不敲调度器，双重锁死。
// 唯一的出路退化成"用户手点【立即同步】"（那条路径显式调 ensureAccess）。
// 现改为：空票但**会话可恢复**时也放行一轮 —— 该轮会先 ensureAccess 换票，成功即恢复自动同步；
// 真正无档案（离线单机）或换票失败仍被挡住/由退避与终态停手兜底，不会空转。
func allowScheduledRound() bool {
	if syncPauseDepth.Load() > 0 {
		return false
	}
	if workerPtr() == nil {
		return false
	}
	return workerHasToken(workerPtr()) || sessionRestorable()
}

// sessionRestorable 本进程是否已建立设备会话（`mountWorker` 带票/空票挂工都会置位）。
// 空票 + 可恢复 ⇒ 值得放行一轮去惰性换票。
func sessionRestorable() bool {
	return lsInstance != nil && lsInstance.sessionRestored
}

// scheduledRound 一轮后台同步：惰性换票（与手工同步同一入口）→ 运行时读全局 worker → RunOnce。
func (l *LocalService) scheduledRound(ctx context.Context) error {
	// ⭐ v21.212：先装配让路检查，再进 ensureAccess。
	// 顺序理由：让路请求可能在 ensureAccess（换票往返）期间到达，
	// 装配必须在最前面，才能让换票之后的每个检查点都认得它。
	if syncScheduler != nil {
		if w := workerPtr(); w != nil {
			s := syncScheduler
			w.SetYieldCheck(s.TryYield)
		}
	}
	if _, err := ensureAccessCtx(ctx, l.uploadDir); err != nil {
		return err
	}
	w := workerPtr() // 每次现读：换库/重挂后自动指向新句柄
	if w == nil || !w.HasToken() {
		return ErrScheduleNoWorker
	}
	_, err := w.RunOnce(ctx)
	if err != nil {
		recordNetworkFailure(err) // P0-3：网络/代理类失败对用户可见
	} else {
		clearNetworkFailure() // 本轮成功 ⇒ 之前的网络提示已过期
	}
	// v21.146：换代/游标落后时，如果 outbox 为空（无未推送的本地改动），自动 Bootstrap
	// 下载服务端完整快照重建本地库，而不是永久停手让用户手动操作。
	if errors.Is(err, synclocal.ErrEpochChanged) || errors.Is(err, synclocal.ErrResyncNeeded) {
		// E4①（2026-09-23）：只数**未收账** op（PENDING/SENDING/FAILED）。
		// 此前 COUNT(*) 把 ACKED/CONFLICT 审计行也算进去 → 恒 >0 → 服务端换代后
		// auto-bootstrap 永不触发，客户端永久停手等人工（真机账本实锤）。
		// CONFLICT 不计入：属待人工处置资产，其内容经 BootstrapNow 的保留重排路径继承。
		var outboxN int
		database.DB.QueryRow(`SELECT COUNT(*) FROM sync_outbox
			WHERE status IN ('PENDING','SENDING','FAILED')`).Scan(&outboxN)
		if outboxN != 0 {
			return err // 有未推送的本地改动：不换代（BootstrapNow 会搬 Outbox），保留错误让退避生效
		}
		// v21.201 A1：熔断器先判。冷却中静默跳过；已熔断则永久停手并明示用户。
		if allow, tripped := autoBootstrapAllow(time.Now()); !allow {
			if tripped {
				log.Printf("[sched] 自动重建已熔断（本轮跳过）：请用【账号与设备 → 强制全量同步】手动恢复")
			} else {
				log.Printf("[sched] 自动重建冷却中（距上次 %s 内），本轮跳过", autoBootstrapCooldown)
			}
			return nil // 不返回 err：冷却/熔断不是失败，不该推进退避
		}
		log.Printf("[sched] epoch/resync + outbox empty → auto-bootstrap")
		pauseScheduler()
		withInternalBootstrap()
		// v21.203：走进程 ctx —— 关机时这一轮换代能被取消，而不是跑完整个下载。
		_, berr := l.bootstrapNowCtx(ctx)
		clearInternalBootstrap()
		resumeScheduler()
		if berr != nil {
			// ⭐ v21.218：同 maybeAutoBootstrap —— 失败走短退避，不写 5 分钟长冷却。
			autoBootstrapFailed()
			log.Printf("[sched] auto-bootstrap failed（将自动重试）: %v", berr)
			return berr
		}
		// v21.201 A2：换代后立刻自检 —— 主动读新代锚点与服务端 state 比对，
		// 不一致时把**哪个键差多少**打进日志（下一轮不必再靠 44 秒后的调度器才发现）。
		mismatch := l.verifyAnchorsAfterTakeover()
		autoBootstrapDone(time.Now(), mismatch)
		if mismatch {
			return synclocal.ErrResyncNeeded // 交给退避，下一轮再自检；不再无限重建
		}
		return nil // Bootstrap 成功，本轮视为完成
	}
	return err
}

// ---- 绑定面（前端可见性/锁屏之外，UI 只读这三块）----

// SyncScheduleStatus 自动同步健康度（数据中心/冲突中心头部展示；不是控制开关——
// 自动同步恒开，用户只有"手工立即同步"这一个主动作）。
func (l *LocalService) SyncScheduleStatus() map[string]any {
	// v21.165：换代代数恒在响应里（哪怕调度器未启动）——前端轮询靠它感知
	// 「数据所在的库已换」而刷新列表（换代不产生 rounds，单看轮次会漏）。
	genEpochVal := genEpoch.Load()
	if syncScheduler == nil {
		return map[string]any{"enabled": false, "reason": "调度器未启动",
			"gen_epoch": genEpochVal, "auto_bootstrap_tripped": autoBootstrapTrippedSnapshot()}
	}
	st := syncScheduler.State()
	out := map[string]any{
		"enabled": true, "started": st.Started, "running": st.Running,
		"rounds": st.Rounds, "offline": st.Offline, "need_attention": st.NeedAttention,
		"consecutive_failures": st.ConsecutiveFailures, "last_error": st.LastError,
		"gen_epoch":              genEpochVal,
		"auto_bootstrap_tripped": autoBootstrapTrippedSnapshot(),
		// ⭐ v21.221：凭据作废（服务端 401 权威拒绝）—— 与 need_attention 分开，
		// 因为它是**唯一真正需要用户重新登录**的情况。UI 据此显示第四态
		// 「已停手 · 需要重新登录」并给出重登入口，而不是让用户对着「恢复中」干等
		// （214 实机症状：关闭重开后永久「恢复中」，笔记列表空，无法使用）。
		"relogin_required": st.ReloginRequired,
		// 下次自动重试的剩余秒数：长退避阶梯（5/15/30min）期间让 UI 能说清
		// 「什么时候会自己再试」，而不是让人怀疑是否彻底停手。
		"retry_in_sec": int(time.Until(st.NextAt).Seconds()),
	}
	// P0-3：最近一次网络/代理失败（若有）—— UI 显示"无法连接服务器，检查网络/代理"
	if txt, at := networkFailureSnapshot(); txt != "" {
		out["last_network_failure"] = txt
		out["last_network_failure_at"] = at
	}
	// 冷却中也回报剩余秒数：让"这段时间没自动同步"可解释，而不是静默。
	if cd, cooling := autoBootstrapCooldownRemaining(time.Now()); cooling {
		out["auto_bootstrap_cooldown_sec"] = int(cd.Seconds())
	}
	// ⭐ v21.218：失败退避剩余秒数（短退避 30s~2min）。
	// 与上面的成功长冷却分开回报，UI 才能区分「刚失败，等 45 秒重试」与
	// 「刚成功，5 分钟内不再重建」—— 两者对用户的含义完全不同。
	if rd, retrying := autoBootstrapRetryRemaining(time.Now()); retrying {
		out["auto_bootstrap_retry_sec"] = int(rd.Seconds())
	}
	if !st.NextAt.IsZero() {
		out["next_at"] = st.NextAt.UTC().Format(time.RFC3339)
	}
	if !st.LastRoundAt.IsZero() {
		out["last_round_at"] = st.LastRoundAt.UTC().Format(time.RFC3339)
	}
	if !st.LastOKAt.IsZero() {
		out["last_ok_at"] = st.LastOKAt.UTC().Format(time.RFC3339)
	}
	return out
}

// SyncPaused v21.158 · X5：换代窗口内是否处于暂停态（Bootstrap换代 10~30s /
// Portable 导入双代激活）。**纯只读**，不改变任何同步语义——返回的是调度闸门
// `syncPaused` 原子量的真实读数。
//
// 背景（X5 挂账）：换库窗口内 pauseScheduler 会让此期间的用户点「立即同步」
// 排队/延后，此前UI 完全无感知 → 用户以为同步坏了。UI 据此提示"正在初始化全量
// 同步，期间同步会排队"，窗口结束后自动消失。
func (l *LocalService) SyncPaused() bool {
	return syncPauseDepth.Load() > 0
}

// BootstrapStatus 只读（2.0.3 · P1-3）：换代进行到哪个阶段 + 进入时刻。
// 纯可见性：前端 SyncInitBanner 据此显示「阶段：下载快照（已等待 Ns）」，
// 替代此前只有整段窗口提示、无阶段感的体验。nil = 本进程内无换代记录（idle）。
func (l *LocalService) BootstrapStatus() *bootstrap.PhaseState {
	return bootstrap.CurrentPhase()
}

// NotifyResume 由前端在页面重新可见/获得焦点时调用（最小化后恢复、锁屏解锁回前台）。
// 只"敲一下"调度器：合并语义保证不会因此叠加轮次（ADR-005 §二.4 触发器集合）。
func (l *LocalService) NotifyResume() map[string]any {
	wakeScheduler()
	return map[string]any{"ok": true}
}

// ---- 登录后等待首轮同步（v21.140）----

// FirstSyncWait 登录后等待首轮同步完成的上限。
//
// 用户要求"登录即等首轮同步完成"：让"登录成功"与"数据可见"不再脱节。
// 库大时首轮可能远超此值，所以是**有界等待**（v21.143：20s→8s，因为登录后整页重载会在后台继续同步，
// 不必把用户按在登录按钮上等 20 秒）——
// 超时不算失败：后台调度继续跑，只是把"仍在同步中"如实告知前端，绝不做无限阻塞。
const FirstSyncWait = 8 * time.Second

// schedulerRounds 当前已完成的后台轮次数（调度器未启动返回 -1）。
func schedulerRounds() int {
	if syncScheduler == nil {
		return -1
	}
	return syncScheduler.State().Rounds
}

// waitFirstRound 有界等待"首轮同步完成"（以轮次计数增长为完成信号，天然 single-flight：
// 走调度器而不是自己再调 RunOnce，避免与后台轮次并发跑同一 worker）。
// 返回 (是否已完成, 面向用户的说明；已完成且无错时说明为空串)。
func waitFirstRound(baseline int, timeout time.Duration) (bool, string) {
	if syncScheduler == nil {
		return false, "后台同步调度未启动，数据将在下次启动后同步"
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := syncScheduler.State()
		if st.Rounds > baseline {
			if st.LastError != "" {
				return false, "首轮同步未完成：" + st.LastError
			}
			return true, ""
		}
		if st.NeedAttention {
			return false, "自动同步已停手（需人工处理）：" + st.LastError
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false, "首轮同步仍在后台进行，数据会陆续出现（可在「账号与设备」查看进度或点「立即同步」）"
}
