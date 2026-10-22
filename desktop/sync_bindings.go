package main

// sync_bindings.go —— 桌面同步与设备会话绑定（P0-D → 批次 4 刀 2 · P3 LocalAuth）。
// 安全姿态（§87）：access token 只存内存；refresh/device_secret 经 DPAPI 封存落
// client_profile（localauth 包）；冷启动 restoreDeviceSession 恢复设备会话（不起 worker——
// access 必须换发，换票+挂工在刀 3 接入重验循环）。未登录（worker 未挂载）时所有本地变异
// 零入队，离线单机行为完全不变。红线 29：全程无监听端口。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stickynote-desktop/bootstrap"
	"stickynote-desktop/localauth"
	"stickynote-desktop/localstore"
	"stickynote-desktop/synclocal"
	"stickynote/database"
	"stickynote/model"
)

// syncWorker 在位 worker（v21.203：由裸指针改 atomic.Pointer）。
//
// 定罪（审计 + 项目纪律）：此前是包级 `var syncWorker *synclocal.Worker`，
// 有 3 处写（mountWorker / PortableImport 的 `= nil` / BootstrapNow 换库重挂）
// 与 40 处读，而**全程无同步**。写者是 UI goroutine（点登录 / 点导入），
// 读者是调度轮 goroutine（scheduledRound 每分钟跑）⇒ 标准 data race。
// 后果不是理论上的：`interface` 两字（type,data）在 32/64 位上是两个独立字，
// 撕裂时会把**别的对象的类型指针配到本对象的 data** ⇒ 运行期直接 crash，
// 且崩溃点与真正的原因相隔很远，几乎无法复现。
//
// 改法：atomic.Pointer 存/取，**保留 syncWorker 这个名字**（40 处调用点
// 不必改语义，只需把 `syncWorker` 换成两个访问器 workerPtr() / setWorker()）。
var syncWorkerPtr atomic.Pointer[synclocal.Worker]

// genEpoch 换代代数计数器（v21.165）：每次 bootstrap 换代成功 +1。
//
// 用途：前端 `installSyncRefreshPolling` 靠轮询 `SyncScheduleStatus` 感知
// 「该刷新数据了」，但它此前只看 `rounds`（后台同步轮次）——而**换代不产生轮次**
// ⇒ 首装登录时前端渲染的空列表永远不会被刷新（204 轮走查定罪的另一半根因：
// 库里已有服务端同步来的笔记，UI 却恒显「共 0 条笔记」直到重启）。
// 把它透出到同一响应里，前端零新增绑定即可感知换代完成。
var genEpoch atomic.Int64

// ⭐ v21.212：gen_epoch **跨进程持久化**（211 实机：「换代成功但 UI 仍空白」的根治）。
//
// 定罪（211 实机）：genEpoch 是**纯内存** atomic，进程重启即归零。
// 而 211 的登录路径把快照放在后台 goroutine 里跑 —— 用户等不到换代完成
// 就关窗/切走，重启后：
//
//	·库里**已有**换代结果（实测 g-000002 = 43 笔记 / 8 文件夹 / 258 changes）；
//	· genEpoch 又从 0 开始，**且前端渲染的"上次已知值"也是 0**
//	  ⇒ `gen_epoch > 上次值` 恒不成立 ⇒ 前端**永远不刷新**。
//
// 这就是「登录后空白 + 显示未登录」的真正机制：数据在，UI 不看。
//
// 修法：把代数落进**当代库**的 sync_state（换代时与新代一起持久化），
// 启动时读回 ⇒ 跨进程单调递增，前端那次"大于上次"的比较必然成立。
// 选sync_state 而非独立文件：它随代库原子切换（Activate 事务内），
// 不会出现"指针已换但代数没落"的窗口。
const genEpochStateKey = "gen_epoch"

// leftoverOldGen 换代后保留的旧代句柄（v21.217c：本进程内不关，防前端
// 绑定调用撞已关闭句柄）。gracefulShutdown / 测试 cleanup 统一关闭。
var leftoverOldGen atomic.Pointer[sql.DB]

// persistGenEpoch 把当前代数落进**新代**库（换代成功后调用）。
//
// ⚠ `sync_state` 属synclocal 层（由 synclocal.EnsureTables 建），而换代链路上
// 它**未必已就位**（首登首代可能还没跑过 EnsureTables）。故这里显式兜底建表——
// 否则持久化会静默失效（Exec 报no such table 被吞掉），D 项根治形同虚设。
func persistGenEpoch(db *sql.DB) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS sync_state (
		key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	_, _ = db.Exec(`INSERT INTO sync_state(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, genEpochStateKey,
		strconv.FormatInt(genEpoch.Load(), 10))
}

// loadGenEpoch 从当代库读回代数（启动时调用）。
func loadGenEpoch(db *sql.DB) {
	if db == nil {
		return
	}
	var v string
	// 表不存在= 首次运行，保持 0（这是正常态，不是错误）。
	if err := db.QueryRow(`SELECT value FROM sync_state WHERE key=?`, genEpochStateKey).Scan(&v); err != nil {
		return
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		genEpoch.Store(n)
	}
}

// workerPtr 读当前 worker（并发安全）。
func workerPtr() *synclocal.Worker { return syncWorkerPtr.Load() }

// setWorker 写当前 worker。**只有需要"确实置空"时才允许传 nil** ——
// 业务上的"降级"应调 clearWorkerToken（保留 worker，见 auth_lifecycle.go），
// 因为置 nil 会关闭 allowScheduledRound 的调度门。
func setWorker(w *synclocal.Worker) { syncWorkerPtr.Store(w) }

// ⭐ v21.212：换代窗口的等待预算（211 的分档已被实机证伪，收敛为单档 3s）。
//
// 211 曾按"摘 worker 指针能让在途轮自然收敛"分档（日常 30s / 首登 60s），
// 但 211 实机日志 `摘指针后在途轮仍未在 1m0s 内收尾` **证伪了该假设**
// （`w := workerPtr()` 早已取进局部变量，摘全局指针无效）。
// 真正让在途轮秒级返回的是**让路信号**（Scheduler.YieldInFlight），
// 它把"等外部网络往返跑完"降为"等一个分页循环边界"。
// 有了它3s 足够（毫秒级收敛 + 3s 纵深余量）。
const (
	// swapWaitBudget 换代等待在途轮退出的上限。
	//
	// ⭐ v21.218（212 实机深度核查定罪）：**超时即中止换代（fail-safe）**，
	// 首登待补数据态除外（见 bootstrapNowCtx 的 firstLoginPending 分支）。
	// v21.213/214 曾改成「超时也继续换代」（fail-open），已定罪回退：
	// 在途轮握着局部变量里的旧 worker，会直读即将被 Close 的旧库 ⇒
	// `sql: database is closed`，且该错误随日志关闭丢失、无法事后取证。
	// 中止只是**推迟**：下一轮 maybeAutoBootstrap 条件仍成立，会自动重试。
	swapWaitBudget = 3 * time.Second
	// firstLoginSwapBudget 首登档：首登换代的耗时由**快照下载**决定，
	// 与「等在途轮」无关，故不再需要更长预算（保留常量名以免调用点漂移）。
	firstLoginSwapBudget = swapWaitBudget
)

// detachWorkerForSwap 换代前置：把 worker 指针摘下，让在途同步轮自然撞空收敛。
//
// ⭐ v21.212 修正注释：摘指针**不能**让在途轮早退（211 已证伪）。
// 它在本处的真实作用是**挡住新轮**：`scheduledRound` 开头 `w := workerPtr()`
// 拿不到 worker 即 ErrScheduleNoWorker 返回；而**已在途**的那一轮由
// YieldInFlight 让路信号处理（见 bootstrapNowCtx 的三步序）。
//
// 摘指针是安全的：换代成功后必然重挂新 worker（见 bootstrapNowCtx 尾部），
// 失败路径则由 defer resumeScheduler 恢复调度门，且 worker 会在下次
// ensureAccess / mountWorker 时重建（冷启动恢复态本就允许空 worker 在位）。
//
// 返回 true = 确实摘下了一个在位 worker。
func detachWorkerForSwap() bool {
	if workerPtr() == nil {
		return false
	}
	setWorker(nil)
	return true
}

// firstLoginPending 判定是否处于「首登待补数据」态：本地既无笔记也无文件夹。
//
// 依据 210 实机读用户实例库：notes 0 行 + note_folders 0 行 + profile 有档，
// 即凭据已签发但数据一条没落地。此态下换代是**必需**动作而非可选优化，
// 因此不能因为等待预算耗尽就放弃（那正是「反复登录仍空白」的机制）。
func (l *LocalService) firstLoginPending() bool {
	var notes, folders int
	// 库句柄在换代窗口内可能正被切换，读失败一律按"非首登"保守处理，
	// 退化成日常档预算，不会因此放松「不强关旧库」的红线。
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM notes WHERE deleted_at IS NULL`).Scan(&notes); err != nil {
		return false
	}
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM note_folders`).Scan(&folders); err != nil {
		return false
	}
	return notes == 0 && folders == 0
}

// bootstrapMu BootstrapNow 单飞（v21.153 R-1 取证坐实）：三条入口可并发进入完整换代
// ——登录 goroutine（maybeAutoBootstrap）/ 调度轮（ErrResyncNeeded→auto-bootstrap，
// scheduler_wiring.go:142）/ 用户显式动作。并发 Run = 双代激活抢写 pointer + 互相
// Close 对方句柄 + carry 源库被对方关闭。TryLock 后到者立即得「重建中」语义返回
// （胜者完成后败者的触发理由通常已消失：数据已同步）。
var bootstrapMu sync.Mutex

// revalAtOf 读最近重验时刻（无档案/列空 → 零值=到期）。
func revalAtOf() time.Time {
	p, err := localauth.Load(database.DB)
	if err != nil {
		return time.Time{}
	}
	return p.LastRevalAt
}

// rotateAfterLogin 登录成功后立即轮转一次 refresh，把"未消耗的末代票"留作封存件（v21.90 一次一换）。
//
// ⭐ v21.203 三态语义（P0）：服务端 refresh 是**一次一换 + 族重放吊销**。轮转失败时
// 「能不能重试」完全取决于请求有没有真的到服务端：
//
//	① 请求未发出（DNS/拨号/TLS 失败）⇒ 服务端没看到这张票 ⇒ **可安全重试**。
//	   2026-10-10 实机正是这一类（TLS handshake timeout / Fake-IP 隧道不通）。
//	② 请求已发出但响应丢失（awaiting headers / while reading body 超时）⇒ 服务端
//	   **很可能已经消费了 R0 并签发了 R1** ⇒ 再花同一张 R0 = 族重放 ⇒ **整族被吊销**，
//	   用户直接掉线必须重登。此态**禁止重试**，只能按现票封存。
//	③ 服务端权威拒绝（401/403）⇒ 票已失效，重试无意义 ⇒ 直接用现票（或引导重登）。
//
// 旧实现把三种情况一律当"失败 → 封存现票"，②③ 是对的，① 是**不必要的失败**
// （网络恢复后仍要等用户重登），而更严重的是日志里只写"rotate failed"，
// 用户与排障者都无法判断这张票到底还在不在 family 里 —— 这正是 202 版
// 「post-login rotate failed 后重启却 device session restored」的困惑来源。
//
// 返回 (access, refresh, 状态枚举)，状态供调用方决定是否提示重试。
type rotateState int

const (
	rotateDone          rotateState = iota // 轮转成功
	rotateNotSent                          // ① 请求未发出 → 可重试
	rotateMaybeConsumed                    // ② 响应丢失，服务端可能已消费 → 禁重试
	rotateRejected                         // ③ 服务端权威拒绝
)

func (s rotateState) String() string {
	switch s {
	case rotateDone:
		return "done"
	case rotateNotSent:
		return "not_sent"
	case rotateMaybeConsumed:
		return "maybe_consumed"
	default:
		return "rejected"
	}
}

// retryable ① 才是可重试的。
func (s rotateState) retryable() bool { return s == rotateNotSent }

func rotateAfterLogin(baseURL, access, refresh string) (string, string) {
	a, r, _ := rotateAfterLoginState(baseURL, access, refresh)
	return a, r
}

// rotateAfterLoginState 是带状态判定的实现（三态）。返回的 access/refresh 恒可直接使用。
//
// v21.169（用户实机：反复开关桌面后掉登录、笔记消失）：本函数会**消费一张 refresh 票**
// （L128 synclocal.Refresh），而服务端 refresh 是「一次一换 + 族重放吊销」——
// 同一张封存票被花第二次即吊销整族，桌面落地成 AUTH_REVOKED + 未登录 + 笔记不可见。
//
// ⚠ 锁契约（v21.212：语义锁不可重入，改动前务必读）：**调用方必须已持换票锁**。
// 三个登录入口（SignIn / RemoteLoginTOTP）与 ensureAccess 都已持这把锁，
// 本函数**故意不再自己加锁**（否则 Go 的非可重入 Mutex 会自死锁，wails build 与 CI 全红）。
// 防「后续新增调用点漏持锁」改用文档契约 + 下方 rotateAfterLogin 包装器在测试中钉住；
// 真正的兜底是 v21.169 给 ensureAccess 的 AUTH_REVOKED 降级（不再丢封存票、直接引导重登）。
func rotateAfterLoginState(baseURL, access, refresh string) (string, string, rotateState) {
	if refresh == "" {
		return access, refresh, rotateNotSent // 无票可轮转（不该发生，防御性）
	}
	access2, refresh2, rerr := synclocal.Refresh(baseURL, refresh)
	if rerr == nil && refresh2 != "" {
		if access2 != "" {
			access = access2
		}
		return access, refresh2, rotateDone
	}
	st := classifyRotateError(rerr)
	if st == rotateNotSent {
		log.Printf("[auth] post-login rotate: request never reached server (retryable), sealing login refresh: %v", rerr)
	} else if st == rotateMaybeConsumed {
		// 这条日志是 202 版排障最大的盲点：必须写清"这张票可能已被消费，
		// 不要再重试"，否则下一次排障还会把网络抖动误判成"服务端问题"。
		log.Printf("[auth] post-login rotate: response lost, server MAY have consumed the token "+
			"(DO NOT retry with the same token; a retry would trip family-replay revocation): %v", rerr)
	} else {
		log.Printf("[auth] post-login rotate: rejected by server (token invalid), sealing login refresh: %v", rerr)
	}
	// 三态一律回退现票：access 在内存可用；refresh 封存件保持登录时那张。
	return access, refresh, st
}

// classifyRotateError 把轮转错误归入三态。
//
// ⚠ v21.203 实现要点：**不能只依赖 ErrNetworkUnreachable 哨兵**。那条路径只有
// `Client.do` 内部走过 `netErrHint` 时才成立；而 `Rotate` 链路上还有裸的
// `fmt.Errorf("sync transport: %w", err)` 包装与非网络来源的错误文本。
// 首版实现因此把「TLS handshake timeout」「dial tcp: connection refused」全判成
// maybe_consumed（=禁止重试的保守侧）—— 功能上不致命（保守侧永不出错），
// 但让 ① 类（明明可安全重试）也背上"必须重登"的语义，判定精度不达标。
//
// 现在分三步判，**顺序即优先级**：
//
//	① 服务端权威拒绝（401/403）→ rejected；
//	② 响应阶段标记（awaiting headers / while reading body / EOF / connection reset）
//	   ⇒ 请求已到服务端，可能已消费 → maybe_consumed；
//	③ 连接阶段标记（TLS handshake / no such host / connection refused / dial tcp /
//	   代理 CONNECT 失败）⇒ 请求根本没写出 → not_sent（可安全重试）；
//	④ 兜底：一律 maybe_consumed（保守侧 —— 宁可提示重登，也不做一次注定
//	   触发族重放的重试）。
func classifyRotateError(err error) rotateState {
	if err == nil {
		return rotateNotSent
	}
	if errors.Is(err, synclocal.ErrAuthRevoked) || errors.Is(err, synclocal.ErrBadCredentials) {
		return rotateRejected
	}
	lower := strings.ToLower(err.Error())
	// ① 响应阶段：请求已发出，服务端可能已消费。
	if strings.Contains(lower, "awaiting headers") || strings.Contains(lower, "while reading body") ||
		strings.Contains(lower, "eof") || strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection aborted") {
		return rotateMaybeConsumed
	}
	// ② 连接阶段：请求未发出。
	if errors.Is(err, synclocal.ErrNetworkUnreachable) ||
		strings.Contains(lower, "tls handshake") || strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "connection refused") || strings.Contains(lower, "dial tcp") ||
		strings.Contains(lower, "proxyconnect") || strings.Contains(lower, "no route to host") ||
		strings.Contains(lower, "network is unreachable") {
		return rotateNotSent
	}
	return rotateMaybeConsumed
}

// finishRemoteLogin 登录成功后的公共链（v21.151 抽自 RemoteLogin 原实现，两条登录路径共用）：
// 挂工（正规入口 mountWorker，带 wakeScheduler）→ state 探测 → 有界等待首轮 → 自动 bootstrap 探测。
//
// ⭐ v21.203 语义收口（P0，2026-10-10 用户实机定罪）：**登录成败由 SignIn 判定，不由
// State 探测判定**。
//
// 定罪链：用户在另一台电脑（本机代理 关闭）点登录 → SignIn 成功（档案写盘、封存 refresh
// 落库、access 在内存）→ mountWorker 挂上带票 worker → State() 因网络/代理超时失败
// → v21.202 的回滚逻辑 `return nil, err` ⇒ **UI 报"登录失败"**，而实际登录已成功。
// 用户重启后又看到 `device session restored`，说明档案确实在 —— 这就是
// 「登录成功却被报失败」的机制，也是 202 版仍未解决的那个症状。
//
// 正确口径（三态，缺一不可）：
//
//	① State 成功            → 正常登录，透出 epoch/seq；
//	② State **网络类**失败  → **仍是登录成功**。凭据已由服务端签发并落盘，网络问题
//	  是"同步链路"问题，不是"认证"问题。返回成功 + network_warning，让 UI 提示
//	  "已登录，但暂时连不上服务器，数据会在网络恢复后同步"。此时**保留 worker**
//	  （它带票，联网后立刻能同步）；卸工反而会关掉调度门（allowScheduledRound 要求
//	  syncWorker != nil），把「网络抖动」升级成「必须重登」。
//	③ State **401/403**（ErrAuthRevoked）→ 服务端权威否认凭据 → 才算登录失败并降级。
func (l *LocalService) finishRemoteLogin(username string, sess *localauth.Session) (map[string]any, error) {
	device := sess.DeviceID
	access := sess.AccessToken

	// 挂工走**正规入口 mountWorker**：它带票挂工后会 `wakeScheduler()`，于是"登录成功"立刻
	// 接一轮同步。此前这里直接 `synclocal.NewWorker(...)`，绕过了那一步 → 登录只做了一次
	// state 探测，数据要等调度器下一个空闲周期（Idle=5min）才来，用户看到的是
	// "登录成功但同步不完整"（v21.140 修）。
	before := schedulerRounds()
	mountWorker(sess, access, l.uploadDir)
	l.deviceID = device
	// PIN 熔断只在**用户确实完成认证**时复位。v21.202 曾把它挂在 State() 成功路径上，
	// 但 State 是网络探测 —— 网络失败不该让用户的 PIN 失败计数继续累加（那是另一类锁定）。
	// 这里挂在「凭据已签发」这一事实点上，即 SignIn 返回成功之后，与网络无关。
	localauth.ResetUnlockFailures()

	// ⭐ v21.177（216 实机：刚登录「账号与设备」即显示「距上次联网重验已超期」）：
	// 登录 = 用户亲历的联网重验（与 RevalidateNow 同语义），必须写入重验时刻。
	// 此前登录不写 last_revalidate_at ⇒ 零值=恒到期 ⇒ 刚登录也提示"超期"，误导用户重登。
	if terr := localauth.TouchRevalidate(database.DB, time.Now()); terr != nil {
		log.Printf("[auth] touch revalidate after login: %v", terr)
	}

	// 登录结果基线：无论后续探测如何，凭据已签发并落盘 ⇒ 登录成功。
	out := map[string]any{
		"device_id": device, "username": username, "server_url": sess.ServerURL,
		"logged_in": true, "state_probed": false,
	}
	st, err := workerPtr().State()
	switch {
	case err == nil:
		out["state_probed"] = true
		out["server_epoch"] = st.ServerEpoch
		out["current_change_seq"] = st.CurrentChangeSeq
		clearNetworkFailure() // 探测通 ⇒ 之前的网络横幅已过期
	case errors.Is(err, synclocal.ErrAuthRevoked):
		// 唯一的真失败：服务端权威否认本次凭据。此时才降级（卸工、不清封存件 ——
		// 与 demoteToRelogin 同一口径：封存票是服务端权威，本地不清）。
		recordNetworkFailure(err)
		l.demoteToRelogin("登录后首次 state 探测被服务端拒绝: " + err.Error())
		log.Printf("[auth] login rejected by server (worker unmounted, sealed kept): %v", err)
		return nil, err
	default:
		// 网络类 / 其他传输层失败：**登录仍然成功**（凭据已签发）。保留带票 worker，
		// 记网络失败让 UI 横幅可见，让用户知道"登录好了、只是网络不通"。
		recordNetworkFailure(err)
		out["network_warning"] = err.Error()
		log.Printf("[auth] login succeeded but state probe failed (network); worker kept: %v", err)
	}
	// 登录即等首轮同步（v21.140，用户要求）：有界等待，超时不算失败——后台继续跑，
	// 只把"仍在同步中"如实透出，避免把登录请求变成无限阻塞。
	firstDone, firstNote := waitFirstRound(before, FirstSyncWait)
	out["first_sync_done"] = firstDone
	out["first_sync_note"] = firstNote
	// E4-5（2026-09-23 真机实锤·条件修正）：首登增量 pull 拉不到 changes 窗口外的历史实体
	//（服务端旧文件夹的 CREATE 事件已被保留窗口清理 → 本地 folders=0 永不补齐）。
	// 触发条件 = **folders 为空** 且 本轮会话未自动 bootstrap 过（sync_state 标记防重复）——
	// 不能要求 notes 也为空：notes 往往先拉到，原条件永不满足（第一版缺陷）。
	// Bootstrap 快照覆盖 notes/folders，未推送改动经 carryOverOutbox 保留重排，安全。
	//
	// ⚠⚠ v21.212 **回滚 211 的同步等待**（211 实机证伪）：
	// 211 把裸 `go` 改成"同步等待自动快照跑完"，想消除"登录成功但数据还在路上"的竞态。
	// 实测**适得其反**：快照要下载完整库（211 实机换代耗时 66s），
	// 而这段等待被**整段搬到登录按钮上** ⇒ 用户看到「转圈 60+ 秒才进主页面」
	// （日志 `摘指针后在途轮仍未在 1m0s 内收尾`）。更糟的是前端
	// `q()` 的 `setTimeout(X,500)` 跳转在 await 之后，登录迟迟不返回 ⇒
	// **界面停在登录前渲染的空状态**，用户据此认定"登录后空白 + 显示未登录"
	//（211 实机：换代其实成功，g-000002 有 43 笔记 / 8 文件夹 / 258 changes）。
	//
	// 现在的正确拆解是**三件事解耦**，而不是"登录必须等数据满"：
	//  ① 登录返回**立即**（凭据已签发即成功，不等任何网络往返）；
	//  ② 快照**异步发起**（回 `go`），但**确保真的发起了**——若 bootstrapMu
	//     已被占（另一条换代在途），说明数据已在路上，不是缺陷；
	//  ③ UI 必定刷新换代结果 —— 由 D 项「gen_epoch 跨进程持久化」保证。
	if l.firstLoginPending() {
		// 仅确认"是否已在路上"，不等完成。
		if started := l.autoBootstrapInFlight(); started {
			out["auto_bootstrap_started"] = true
		} else {
			go l.maybeAutoBootstrap()
			out["auto_bootstrap_started"] = true
		}
	} else {
		go l.maybeAutoBootstrap()
	}
	return out, nil
}

// autoBootstrapInFlight 报告「已有一条换代在途」（v21.212，取代 211 的
// triggerAutoBootstrapSync 同步等待）。
//
// 语义差别是关键：211 的版本会**等换代跑完**（实机66 秒），把等待搬到登录按钮上；
// 现在只**看一眼状态**（bootstrapMu 是否被占）就返回，恒为微秒级。
// 语义正当性：若已有换代在途，说明数据已经在下载途中 —— 那正是"已发起"，
// 无需再发起一条（bootstrapMu 的 TryLock 本就会拒绝第二条）。
func (l *LocalService) autoBootstrapInFlight() bool {
	if bootstrapMu.TryLock() {
		// 锁空闲：此刻无人换代。**必须立即释放** —— 否则这个"探测"
		// 会变成"占用"，反而把真正该跑的那条换代挡在门外。
		bootstrapMu.Unlock()
		return false
	}
	return true // 拿不到锁 = 已有人在换代 = 数据已在路上
}

// RemoteLogin 真实账号换 JWT → 即刻轮转一次（v21.90 一次一换）→ **落盘未消耗的末代票**
// → 挂载 worker（access 仅内存，§87）。
// 为什么先轮转再封存：登录返回的 R0 若不先花掉，"登录即用"与"跨进程重放"语义冲突——
// 登录会话本身持有 access（无需 R0），把 R0 落盘后一旦下个进程用它换票，服务端 family
// 重放防护会撤销整族（走查 4 实锤）。登录流程内立即花一次：拿到未消费的 R1 再封存，
// 冷启动 OpenSession→Refresh(R1) 恒为首次使用（ensureAccess 同规则滚动花+存）。
// v21.151（生产 Bug2）：2FA 账号的登录响应为 200 {totp_required, totp_token}（无 token）——
// 透出给前端走 RemoteLoginTOTP 第二步；此前该响应被静默当成功（空票），下游 401 被误报
// 「登录会话已失效（可能在别处登出或被吊销）」。
func (l *LocalService) RemoteLogin(baseURL, username, password string) (map[string]any, error) {
	// 先校验再触网：空值/畸形地址不该打到服务端（既污染对方的登录防爆破计数，
	// 也让用户等到 15s 超时才看到一句"失败"）。文案面向用户，可直接上屏。
	baseURL = strings.TrimSpace(baseURL)
	username = strings.TrimSpace(username)
	if baseURL == "" || username == "" || password == "" {
		return nil, errors.New("服务器地址、用户名、密码都要填")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("服务器地址要写成 http:// 或 https:// 开头，例如 http://192.168.31.100:13389")
	}
	if err := synclocal.EnsureTables(database.DB); err != nil {
		return nil, err
	}
	// v21.203：SignIn 也必须持 exchangeMu。审计定罪：此前只有 ensureAccess 持锁，
	// 而 SignIn 走的是同一条「一次一换 + 族重放吊销」的服务端契约 ——
	// 用户在调度轮正在换票时点登录，同一张封存票被花两次，第二次必 401 并吊销整族
	// （症状：登录看着成功，下一条同步就 AUTH_REVOKED，只能重登）。
	// v21.212：改用可取消锁（关机/换代期间不必排队干等）。
	if !lockExchange(appCtx()) {
		return nil, errors.New("正在退出登录，请稍后重试")
	}
	defer unlockExchange()
	var rot rotateState
	sess, err := localauth.SignInState(database.DB, baseURL, username, func() (string, string, error) {
		access, refresh, lerr := synclocal.Login(baseURL, username, password)
		if lerr != nil {
			return "", "", lerr
		}
		access, refresh, rot = rotateAfterLoginState(baseURL, access, refresh)
		return access, refresh, nil
	})
	if err != nil {
		// v21.151：2FA 账号——透出临时票让前端收验证码走第二步（不得混入"吊销"误报）
		var totpErr *synclocal.TOTPRequiredError
		if errors.As(err, &totpErr) {
			return map[string]any{"totp_required": true, "totp_token": totpErr.Token}, nil
		}
		return nil, err
	}
	out, ferr := l.finishRemoteLogin(username, sess)
	if ferr != nil {
		return out, ferr
	}
	// 轮转三态透出（前端可据此提示"网络恢复后会自动重试"或"请重新登录"）。
	// ②（可能已消费）必须让用户知道，因为下一次 ensureAccess 换票会拿到 401。
	out["rotate_state"] = rot.String()
	if rot == rotateMaybeConsumed {
		out["rotate_warning"] = "登录成功，但网络中断导致令牌轮转结果未知；若稍后同步提示登录失效，请重新登录"
	}
	return out, nil
}

// RemoteLoginTOTP 两步验证第二步（v21.151）：临时票 + 动态码（或恢复码）换正式会话，
// 复用与 RemoteLogin 一致的「轮转 → 封存 → 挂工 → 首轮等待」链；密码不在本步出现。
// 服务端文案（验证码错误/验证已过期/防爆破锁定）经 friendlyHTTPError 原样透出给 UI。
func (l *LocalService) RemoteLoginTOTP(baseURL, username, totpToken, code string) (map[string]any, error) {
	baseURL = strings.TrimSpace(baseURL)
	username = strings.TrimSpace(username)
	totpToken = strings.TrimSpace(totpToken)
	code = strings.TrimSpace(code)
	if baseURL == "" || username == "" || totpToken == "" || code == "" {
		return nil, errors.New("两步验证参数不完整，请返回重新登录")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("服务器地址要写成 http:// 或 https:// 开头，例如 http://192.168.31.100:13389")
	}
	if err := synclocal.EnsureTables(database.DB); err != nil {
		return nil, err
	}
	// v21.203：与 RemoteLogin 同一把锁（同一族一次一换契约）；
	// v21.212：改用可取消锁。
	if !lockExchange(appCtx()) {
		return nil, errors.New("正在退出登录，请稍后重试")
	}
	defer unlockExchange()
	var rot rotateState
	sess, err := localauth.SignInState(database.DB, baseURL, username, func() (string, string, error) {
		access, refresh, verr := synclocal.VerifyTOTP(baseURL, totpToken, code)
		if verr != nil {
			return "", "", verr
		}
		access, refresh, rot = rotateAfterLoginState(baseURL, access, refresh)
		return access, refresh, nil
	})
	if err != nil {
		return nil, err
	}
	out, ferr := l.finishRemoteLogin(username, sess)
	if ferr != nil {
		return out, ferr
	}
	out["rotate_state"] = rot.String()
	if rot == rotateMaybeConsumed {
		out["rotate_warning"] = "登录成功，但网络中断导致令牌轮转结果未知；若稍后同步提示登录失效，请重新登录"
	}
	return out, nil
}

// restoreDeviceSession 冷启动设备会话恢复（main 启动尾调用）：导入旧 device_id 文件 +
// 解封存档案。非 Windows/换机 = 明确降级（日志可见，不静默）；不致命。
func (l *LocalService) restoreDeviceSession(profileDir string) error {
	// v21.152 自愈：代际切换后身份档案可能缺席（carry 未落/历史版本产物）→ 启动期从上一代找回。
	// 必须在 OpenSession 之前（否则会被判成"从未登录"直接回离线单机态）。
	healIdentityAcrossGenerations(profileDir)
	if _, err := localauth.ImportLegacyDeviceID(database.DB, profileDir); err != nil {
		return err
	}
	sess, refreshToken, err := localauth.OpenSession(database.DB)
	if err != nil && !errors.Is(err, localauth.ErrNoProfile) {
		// v21.146：DPAPI 解密偶尔瞬态失败（Windows 更新/用户上下文切换），重试一次
		time.Sleep(500 * time.Millisecond)
		sess, refreshToken, err = localauth.OpenSession(database.DB)
	}
	if errors.Is(err, localauth.ErrNoProfile) {
		return nil
	}
	if err != nil {
		log.Printf("[auth] device session not restored after retry (re-login required): %v", err)
		return nil
	}
	// v21.152（生产实锤）：封存 refresh 缺失（主动登出残留 / 已被服务端吊销）时**不挂空票 worker**。
	// 挂了只会让调度器每轮 ensureAccess 必失败（NO_REFRESH/AUTH_REVOKED）→ 退避 → 终态
	// 「自动同步已暂停，需要你处理」，把"该重新登录"误报成"需要人工处理故障"。
	// 不挂 = 干净的离线单机态；UI 经 AuthStatus.relogin_required 引导重登。
	if refreshToken == "" {
		log.Printf("[auth] profile present but no recoverable refresh (logged out / revoked): offline mode, re-login required")
		return nil
	}
	// 档案可开 = 设备会话成立：**启动即挂空票 worker**（走查实锤：若等首次 SyncNow 才挂，
	// 同次运行的本地编辑会因入队门 syncWorker==nil 漏账）。换票由 ensureAccess 惰性完成。
	mountWorker(sess, "", filepath.Join(profileDir, "uploads"))
	log.Printf("[auth] device session restored: device=%s server=%s", sess.DeviceID, sess.ServerURL)
	return nil
}

// AuthStatus 会话/档案可见性（数据中心 & 锁屏 UI 消费面）。
//
// ⭐ v21.220（213 实机定罪·降级不抛错）：**本绑定不得因内部读表失败而抛错**。
//
// 定罪：原实现首个 `EnsureTables` 失败即 `return nil, err`。而 Wails 绑定返回 error 时
// 前端 Promise **reject** ⇒ `refresh()` 进 catch ⇒ `st` 停在 `{}`
// ⇒ 账号卡片显示「未登录：处于离线单机状态」。
// 213 实机首屏恰逢换代窗口（DB 句柄切换中）⇒ 就是这么撞上的。
//
// 「读不到状态」与「用户未登录」是两件完全不同的事：
//
//	· 前者 = 我还不知道（必须重试，且 UI 不该断言"未登录"）；
//	· 后者 = 我确认了，确实没登录。
//
// 抛错把前者变成了后者的**反向误报** —— 用户据此反复重登，
// 而每次重登又制造新的换代窗口 ⇒ 恶性循环（213 实机即此）。
//
// 故改为：**永远返回结构完整的 map**，把失败信息放进 `status_error` 字段。
// 前端凭 `status_degraded` 显示「正在重新读取」而不是「未登录」。
func (l *LocalService) AuthStatus() (map[string]any, error) {
	if err := synclocal.EnsureTables(database.DB); err != nil {
		log.Printf("[auth] AuthStatus degraded: EnsureTables failed: %v", err)
		online := workerHasToken(workerPtr())
		return map[string]any{
			"status_degraded":  true,
			"status_error":     err.Error(),
			"profile":          false,
			"online":           online,
			"accounting":       workerPtr() != nil,
			"logged_in":        online,
			"relogin_required": false,
			// ⚠ session_recoverable **刻意留 false**：此刻读不到封存件，
			//   断言"可恢复"是猜测。前端见 degraded 会持续重试。
		}, nil
	}
	st, err := synclocal.Status(database.DB)
	if err != nil {
		// 同上：Status 失败（换代窗口 DB 句柄切换中 = sql: database is closed）
		// 同样降级返回而非抛错，否则前端又会把"读不到"误报成"未登录"。
		log.Printf("[auth] AuthStatus degraded: Status failed: %v", err)
		online := workerHasToken(workerPtr())
		return map[string]any{
			"status_degraded":  true,
			"status_error":     err.Error(),
			"profile":          false,
			"online":           online,
			"accounting":       workerPtr() != nil,
			"logged_in":        online,
			"relogin_required": false,
		}, nil
	}
	// E4-6（2026-09-23 真机实锤）：logged_in 语义修正——**账号已绑定（封存档案可开且
	// server_url 在位）即"已登录"**，无论 access 在内存（在线态）还是需惰性换票（恢复态）。
	// 此前判定 = workerHasToken（内存票）→ 重启/换代后 worker 空票 → 同步明明正常
	//（ensureAccess 惰性换票）UI 却显示"未登录"，用户被误导反复重登（六轮反馈的钉子）。
	// v21.152 再收窄（生产实锤）：**可恢复还要求封存 refresh 在位**——只看 server_url 会造出
	// "僵尸已登录"（卡片显示已连接，但每一条同步路径都失败 NO_REFRESH/AUTH_REVOKED）。
	// online 单列：实时推送能力（有票）；前端据此区分「已连接」与「恢复中」。
	online := workerHasToken(workerPtr())
	st["online"] = online
	st["accounting"] = workerPtr() != nil
	st["session_restored"] = l.sessionRestored
	st["pin_enrolled"] = localauth.PINEnrolled(database.DB)
	st["revalidate_due"] = shouldRevalidate(revalAtOf(), time.Now())
	p, err := localauth.Load(database.DB)
	switch {
	case errors.Is(err, localauth.ErrNoProfile):
		st["profile"] = false
		st["device_id"] = l.deviceID
		st["logged_in"] = online       // 无档案（从未登录）：内存票不应存在，防御性兜底
		st["relogin_required"] = false // 没登录过 ≠ 需要重登
		// R-1 诊断（v21.153）：worker 在位而当代库读不到档案 = 异常组合
		//（正常离线单机是「worker nil + 无档案」）；命中即留痕，供换库窗口取证。
		if workerPtr() != nil {
			log.Printf("[auth] AuthStatus anomaly: worker mounted but client_profile missing in current DB")
		}
	case err != nil:
		// ⚠ v21.203：本分支实际只覆盖 **SQL 层错误**（表缺失 / 库 locked / 句柄已关）。
		//
		// 定罪（审计）：`localauth.Load` **不做 DPAPI 解封** —— 它只把
		// `refresh_sealed` / `secret_sealed` 当原始字节读出来。真正的"档案在本机
		// 打不开"（换机 / 篡改 / 瞬态 DPAPI 故障）发生在 `OpenSession` 里，而它的
		// 探针位于下面的 `default` 分支（`profile_openable`）。所以 202 轮写在
		// 这一分支的"DPAPI 不可解时回填 server_url"修复**永远不触发**：该分支既不
		// 发生 DPAPI 失败，真发生时又读不动列。
		//
		// 现状保留：仍然尽力回填（server_url 是明文列，与 DPAPI 无关），读不出就
		// 算了 —— 那说明连 SQL 都不通，回填无意义。
		st["profile"] = true
		st["profile_openable"] = false
		st["profile_error"] = err.Error()
		st["logged_in"] = online
		st["relogin_required"] = !online
		var rawURL string
		if qerr := database.DB.QueryRow(
			`SELECT server_url FROM client_profile WHERE id=1`).Scan(&rawURL); qerr == nil && rawURL != "" {
			st["server_url"] = rawURL
			st["server_url_recovered"] = true // UI 可据此提示"档案本机打不开，请重新登录"
		}
	default:
		st["profile"] = true
		st["device_id"] = p.DeviceID
		st["server_url"] = p.ServerURL
		st["username"] = p.Username
		if !p.LastLoginAt.IsZero() {
			st["last_login_at"] = p.LastLoginAt.Format("2006-01-02T15:04:05Z07:00")
		}
		if !p.LastRevalAt.IsZero() {
			st["last_revalidate_at"] = p.LastRevalAt.Format("2006-01-02T15:04:05Z07:00")
		}
		// 可开性探针：档案行在 ≠ 封存档位在本机可解（换机/篡改只有 OpenSession 能证伪）。
		//
		// ⭐ v21.203：**这里才是「档案本机打不开」的真实分支**。本分支 Load 成功
		// ⇒ p.ServerURL 已是权威明文值（上面已回填）⇒ 用户看到的是**真实地址**
		// 而不是 placeholder；额外置 `server_url_recovered` 让前端能区分
		//「档案在但本机打不开」与「正常已登录」，从而给出"重新登录"而不是"重填地址"。
		openable := true
		if _, _, oerr := localauth.OpenSession(database.DB); oerr != nil {
			openable = false
			st["profile_openable"] = false
			st["profile_error"] = oerr.Error()
			if p.ServerURL != "" {
				st["server_url_recovered"] = true
			}
		} else {
			st["profile_openable"] = true
		}
		// 可恢复 = 档案可开 且 封存 refresh 在位（空=主动登出残留或已被吊销）。
		recoverable := openable && len(p.RefreshSealed) > 0
		st["session_recoverable"] = recoverable
		st["logged_in"] = online || recoverable
		st["relogin_required"] = !online && !recoverable

		// ⭐ v21.218（212 实机深度核查）：**直接下发前端可写入的 user 展示对象**。
		//
		// 此前前端恢复展示态必须走 `/auth/login` → LocalLogin() 桩绑定，而这条路有三重
		// 静默失败（桥未就绪即跳过对账 / catch{} 吞错 / 已对账标志永不复位），导致 Go 侧
		// 明明 logged_in=true 而 UI 恒显未登录、整个进程永不自愈。
		// 改为：Go 把权威展示态随 AuthStatus 一起下发，前端**零网络、零桥二次调用**即可
		// 写入本地 user，从根上消除「恢复展示态」这条链路上的失败点。
		//
		// role 恒为 user：桌面端本地库只有 provision 出来的占位账号（users 表仅
		// username=local 一行），真实权限判定在服务端，此处只驱动前端菜单展示。
		if st["logged_in"] == true {
			st["session_user"] = map[string]any{
				"id":       1,
				"username": p.Username,
				"role":     "user",
				"status":   "active",
			}
		}
	}
	return st, nil
}

// SyncNow 手动"立即同步"（§36：唤醒同一 Worker 语义）。刀 3：离线恢复后首触此处换票挂工；
// 服务端吊销（401）→ 登出级降级并原样返回错误（Outbox 不丢，红线 1/6）。
func (l *LocalService) SyncNow() (map[string]any, error) {
	if !workerHasToken(workerPtr()) {
		if _, err := ensureAccess(l.uploadDir); err != nil {
			if errors.Is(err, synclocal.ErrAuthRevoked) {
				l.demoteToRelogin("syncnow-refresh-401")
			}
			// v21.152：凭据失效（NO_REFRESH）与吊销同列——UI 据此把"重登"入口亮出来。
			return map[string]any{"error": err.Error(),
				"relogin_required": errors.Is(err, synclocal.ErrAuthRevoked) || errors.Is(err, ErrNeedRelogin)}, err
		}
	}
	if !workerHasToken(workerPtr()) {
		return nil, errors.New("NOT_LOGGED_IN")
	}
	sum, err := workerPtr().RunOnce(context.Background())
	if err != nil {
		if errors.Is(err, synclocal.ErrAuthRevoked) {
			l.demoteToRelogin("runonce-401")
			return map[string]any{"error": err.Error(), "relogin_required": true}, err
		}
		if errors.Is(err, synclocal.ErrEpochChanged) || errors.Is(err, synclocal.ErrResyncNeeded) {
			// P5 §89：不自动换代（用户显式动作才 Bootstrap），但语义位透出给 UI 引导。
			return map[string]any{"error": err.Error(), "resync_required": true}, err
		}
		return map[string]any{"error": err.Error()}, err
	}
	// 手工同步成功 = 链路确认可用：敲一次调度器把退避/离线/停手三态归零（不叠加轮次）。
	wakeScheduler()
	return map[string]any{
		"applied": sum.Applied, "conflicts": sum.Conflicts,
		"pushed": sum.Pushed, "acked": sum.Acked,
	}, nil
}

// ForceFullResync 强制全量重拉：将 cursor 重置为 0，下次调度轮会从服务端 change_log 头部
// 重新拉取所有变更。适用于"cursor 与服务端实际数据不匹配"（服务端重建/换代/数据修复后）。
// **不会**丢弃 outbox（本机未推送的改动仍在，会在下一轮推送）。
func (l *LocalService) ForceFullResync() (map[string]any, error) {
	if workerPtr() == nil {
		// v21.152：吊销/登出后 worker 可能未挂——先尝试换票挂工；失败时明确引导重登
		//（旧行为抛裸「同步引擎未挂载」，用户无从得知要重新登录）。
		if ok, err := ensureAccess(l.uploadDir); err != nil || !ok {
			msg := "同步引擎未挂载"
			if err != nil {
				msg = err.Error()
			}
			return map[string]any{"error": msg,
				"relogin_required": errors.Is(err, ErrNeedRelogin) || errors.Is(err, synclocal.ErrAuthRevoked)}, errors.New(msg)
		}
	}
	// v21.147：先跑一轮把 outbox 里的本地改动推上服务端，再重置 cursor
	// 否则重置后 pull 覆盖本地，outbox 引用的实体可能已被覆盖 → push 时丢失
	if workerPtr().HasToken() {
		ctx := context.Background()
		if _, perr := workerPtr().RunOnce(ctx); perr != nil {
			log.Printf("[resync] pre-push round failed (non-fatal): %v", perr)
		}
	}
	// 重置 cursor → 下轮RunOnce 从头拉
	if _, err := database.DB.Exec(
		`UPDATE sync_state SET value = '0' WHERE key = 'cursor'`); err != nil {
		return nil, fmt.Errorf("重置 cursor 失败: %w", err)
	}
	// ⭐ v21.213（211b 实机铁证·「自动同步已暂停」的直接成因）：**不再删epoch/instance**。
	//
	// 211b 实机坐实：用户点【强制全量同步】后 `sync_state` 变成
	// `cursor=0` 且 **server_epoch / server_instance_id 双双消失**（本函数第 736 行删的）。
	// 而 g-000002 是刚由快照换代出来的，锚点自检明明是 `epoch=2 cursor=258 instance=215a85a6`。
	// 删锚点后的连锁反应：
	//   ① RunOnce 首跑 `saved==""` → **跳过 §74/§92 代际锚校验**（本该拦住的越界没了）；
	//   ② `cursor(0) < MinAvailableChangeSeq` 极易成立 → `ErrResyncNeeded`；
	//   ③ 但 `auto_bootstrapped=1` 已置位 ⇒ `maybeAutoBootstrap` 直接 return；
	//   ④ ⇒ 调度轮**永久停手**，UI 显示「自动同步已暂停，需要你处理」
	//      +「本机凭据已失效」—— 与211b 实机截图逐字吻合。
	//
	// 正确口径：重置 cursor 的语义是「**增量重新拉一遍**」，而 epoch/instance 恰恰是
	// 「本机数据与服务端是否同代」的**权威锚点**，删它等于把安全网一起拆了。
	// 保留锚点后：cursor=0 会触发一次完整 pull（正是"强制全量"的本意），
	// 而 epoch 校验继续生效—— 若服务端真的换代了，走 `ErrEpochChanged` 的正规路径
	// （auto-bootstrap）重建，而不是撞在「锚点缺失 + 已标记不重建」的夹缝里。
	//
	// v21.201 A1：手动强制同步是用户显式处置 ⇒ 清熔断状态 + 清冷却计时，
	// 否则用户刚点完「强制全量同步」，冷却期内的自动重建仍被挡住。
	resetAutoBootstrap()
	wakeScheduler()
	return map[string]any{"ok": true, "message": "已重置同步游标，后台将重新拉取全部数据"}, nil
}

// SyncDiag 诊断：返回同步全貌，供 UI 展示或远程排查。
func (l *LocalService) SyncDiag() (map[string]any, error) {
	out := map[string]any{}
	if workerPtr() == nil {
		out["worker"] = "nil"
		return out, nil
	}
	st, err := workerPtr().State()
	if err != nil {
		out["state_err"] = err.Error()
		return out, err
	}
	out["server_epoch"] = st.ServerEpoch
	out["server_instance_id"] = st.ServerInstanceID
	out["server_current_change_seq"] = st.CurrentChangeSeq
	out["server_min_available_change_seq"] = st.MinAvailableChangeSeq
	out["worker_has_token"] = workerPtr().HasToken()
	if syncScheduler != nil {
		ss := syncScheduler.State()
		out["sched_rounds"] = ss.Rounds
		out["sched_running"] = ss.Running
		out["sched_need_attention"] = ss.NeedAttention
		out["sched_last_error"] = ss.LastError
		out["sched_last_ok_at"] = ss.LastOKAt.UTC().Format(time.RFC3339)
	}
	// DB 侧
	for _, k := range []string{"cursor", "server_epoch", "server_instance_id", "last_sync_at"} {
		var v string
		database.DB.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, k).Scan(&v)
		out["db_"+k] = v
	}
	var outboxN int
	database.DB.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&outboxN)
	out["outbox_count"] = outboxN
	var notesN, notesAlive int
	database.DB.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&notesN)
	database.DB.QueryRow(`SELECT COUNT(*) FROM notes WHERE deleted_at IS NULL`).Scan(&notesAlive)
	out["notes_total"] = notesN
	out["notes_alive"] = notesAlive
	return out, nil
}

// BootstrapNow P5 显式重建（§89 SERVER_EPOCH_CHANGED / §140 cursor 落后 / 首次装机）：
// 快照下载→双代激活→cursor=watermark→RESYNC Outbox 搬运。失败零破坏（红线 1/6/9/17）。
// 前置：有效 access（登录态或换票成功），否则明确拒绝。
func (l *LocalService) BootstrapNow() (map[string]any, error) {
	return l.bootstrapNowCtx(nil)
}

// bootstrapNowCtx 带取消的换代实现（v21.203 批次 3）。
// ctx 非 nil 时可被取消（关机钩子传 shutdownCtx）；nil = 不可取消（旧语义）。
func (l *LocalService) bootstrapNowCtx(ctx context.Context) (map[string]any, error) {
	if !bootstrapMu.TryLock() {
		return map[string]any{"error": "重建同步已在进行中，请稍候", "bootstrap_busy": true},
			errors.New("重建同步已在进行中，请稍候")
	}
	defer bootstrapMu.Unlock()
	if !workerHasToken(workerPtr()) {
		if ok, err := ensureAccess(l.uploadDir); err != nil || !ok {
			if err != nil {
				return map[string]any{"error": err.Error(),
					"relogin_required": errors.Is(err, ErrNeedRelogin) || errors.Is(err, synclocal.ErrAuthRevoked)}, err
			}
			return map[string]any{"error": "NOT_LOGGED_IN", "relogin_required": true}, errors.New("NOT_LOGGED_IN")
		}
	}
	sess, _, err := localauth.OpenSession(database.DB)
	if err != nil {
		return map[string]any{"error": err.Error()}, err
	}
	sess.AccessToken = workerPtr().AccessToken() // ensureAccess 成功路径必持票；nil 防御性兜底
	if sess.AccessToken == "" {
		return map[string]any{"error": "NO_ACCESS"}, errors.New("NO_ACCESS")
	}
	pauseScheduler() // Run 内含双代激活：调度轮不得插进换库窗口（ADR-005 接线纪律）
	defer resumeScheduler()
	// ⭐ v21.211（210 实机铁证·登录成功但笔记空白·三症状之一）：
	//
	// 定罪（用户实例日志 app-2026-10-13.log 11:04:23→11:04:31）：
	//
	//	[bootstrap] 本地无文件夹且本轮未自动快照过：执行一次快照全量同步
	//	[bootstrap] 在途同步轮未在8s 内收尾，本次换代中止（不强关旧库，避免 database is closed）
	//	[bootstrap] 自动快照失败（可在账号与设备手动强制全量同步重试）
	//
	// 读用户实例 g-000001/notes.db 坐实：notes 0 行、note_folders 0 行、sync_outbox 空，
	// 而 client_profile 有 1 行（refresh_sealed 515 字节在位）⇒「登录态在、数据一条没有」。
	//
	// 机制：登录成功 → mountWorker 唤醒后台轮 → `go maybeAutoBootstrap()` 也发起换代。
	// 两条路径并发。bootstrapNowCtx 先 pauseScheduler（只拦**新**轮），再等
	// **在途**轮；而在途轮 `RunOnce` 要换票 + 拉全量 changes，弱网下收不了。
	//
	// ⚠ v21.212 **修正 211 的错误修法**（211 实机日志 `摘指针后在途轮仍未在 1m0s
	// 内收尾` 已证伪）：211 假设"摘 worker 指针 ⇒ 在途轮读到 nil 即早退"。
	// 实测**无效** —— `scheduledRound` 开头 `w := workerPtr()` 早已把 worker
	// 取进**局部变量**，之后一路 `w.RunOnce(ctx)` 跑完整轮，摘全局指针对它无效。
	// 于是 WaitIdle 实打实等满 60s，而这段等待又因登录同步等待被整段搬到
	// 登录按钮上 ⇒ 用户实机看到「转圈 60+ 秒才进主页面」。
	//
	// ⭐⭐ v21.214（211c 实机铁证·用户选定方案 C）：**换代窗口前先持有换票锁**。
	//
	// 211c 实机：`13:01:06 [sched] 调度器未在预期时限内退出` 与 `13:01:06 新进程启动`
	// **同秒**，紧接着 `13:01:09 凭据被服务端拒绝（AUTH_REVOKED）`。
	// 机制：v21.213 给换票加了 `WithoutCancel`（换票不可被取消）——这对慢网是对的，
	// 但在换代窗口里制造了新的竞态：
	//   ·换代侧：让路 → 摘指针 → `WaitIdle(3s)` 超时但**继续换代**（v21.213 定的"不放弃"）；
	//   · 在途轮侧：它可能**正在 `ensureAccess` 里换票**，而 `WithoutCancel` 使换票
	//     **不会被让路信号打断**（让路只在 `RunOnce` 的分页循环边界检查，换票段无检查点）；
	//   ⇒ 换代切走 `database.DB` 并 `oldDB.Close()`；在途轮换票随后完成，
	//     `SaveRefreshCAS(database.DB, ...)` 写向**已关闭的旧句柄** ⇒ **新票丢失**；
	//     而服务端已轮转作废旧票 ⇒ 下次启动**必然 401**。
	//
	// 为什么 C 能从根上消除：换票全程持 `exchangeMu`。换代侧在进入窗口前
	// **先拿这把锁**，就保证「此刻没有换票在进行」——要么它已结束（票已落盘），
	// 要么它还没开始（换代后才会发起，届时写的是**新代**句柄）。
	// 于是不再依赖「等多久」这种外部时长做同步屏障，这是可设计的。
	//
	// 为什么不靠「超时也不放弃」：那种兜底是在赌在途轮不会碰旧库，而换票段
	// 恰恰会写旧库（SaveRefreshCAS）——赌输了就是必然掉登录。持锁是消除而非兜底。
	if !lockExchange(ctx) {
		// ctx 已取消（关机中）：不换代。这是正确取舍——
		// 换代是可选动作，丢票是不可恢复的，两者不可比。
		log.Printf("[bootstrap] 关机/取消中，跳过本次换代（避免在途换票跨越换代导致丢票）")
		return map[string]any{"error": "正在退出，请稍后重试"}, errors.New("bootstrap skipped: shutting down")
	}
	defer unlockExchange()
	// ⚠ 持锁期间**不得**再调ensureAccess（它自己也要取这把锁 → 死锁）。
	// 此时 worker 已带票（函数开头已校验），换票需求已由调度轮/登录路径满足。

	if syncScheduler != nil {
		waitCtx := ctx
		if waitCtx == nil {
			waitCtx = l.ctx
		}
		if waitCtx == nil {
			waitCtx = context.Background()
		}
		// ① 先广播让路（在途轮最迟在下一个分页边界返回）
		syncScheduler.YieldInFlight()
		// ② 再摘指针（挡住新轮）。
		// **摘之前先把票取出来** —— 推迟换代要按原票重挂 worker，
		// 否则会退化成空票挂载，把「已在线」误降级为「需惰性换票」，
		// 让用户多等一轮甚至看到「未登录」（access token 只在内存，取完即丢）。
		rebindAccess := ""
		if w := workerPtr(); w != nil {
			rebindAccess = w.AccessToken()
		}
		detached := detachWorkerForSwap()
		// ③ 最后等它真正退出
		//
		// ⭐ v21.218（212 实机深度核查·**恢复 fail-safe，但保留首登豁免**）：
		//
		// v21.213/214 改成「超时但继续换代」（fail-open）。**该回退已定罪为错误**。
		// 持 exchangeMu 只保证「此刻没有换票在进行」，它**不**保证「在途轮不再触碰旧库」：
		// 在途轮拿的是 `scheduledRound` 开头 `w := workerPtr()` 取进**局部变量**的 worker，
		// 那一路 `w.RunOnce(ctx)` 直读旧代 DB（锚校验 / outbox / 落盘）。
		// 让路信号只在**分页循环边界**检查，轮首锚校验与轮尾落盘都无检查点
		// ⇒ 超时未收尾仍继续换代 ⇒ 换代侧 Close 旧库 ⇒ 在途轮触碰即
		// **`sql: database is closed`**（v21.169 已为此付出实机代价）。
		// 更糟：该错误写进**即将被关闭的日志** ⇒ 故障证据随日志一起丢失。
		//
		// 代价对比（有界 vs 不可恢复）：
		//   超时中止 = 本次换代推迟到下一轮，用户侧见「同步稍后自动重试」，**下一轮会自动重试**；
		//   超时继续 = 可能必现 database is closed + 证据丢失，并持续污染后续轮次。
		//   前者严格更优 —— 因为 `maybeAutoBootstrap` 的触发条件是「folders 为空 且 本轮未快照过」，
		//   推迟后下一轮条件依然成立 ⇒ **会自动重试，不是一次性放弃**。
		//
		// ⚠ 例外（不可省）：**首登待补数据态豁免**。此时 notes/folders 皆空，
		// 数据只能靠换代快照落地；若同样中止，用户会永远停在空白（这正是 210 实机
		// 「反复登录仍空白」的机制）。firstLoginPending 为真时维持 v21.214 的继续换代。
		if syncScheduler.WaitIdle(waitCtx, swapWaitBudget) {
			log.Printf("[bootstrap] 在途轮已让路收尾（worker 摘除=%v），安全进入换代窗口", detached)
		} else if l.firstLoginPending() {
			// 首登态：换代是**必需**动作（数据只能靠它落地），不放弃。
			// 此时已持 exchangeMu（换票不跨越换代），且在途轮已收到让路信号。
			log.Printf("[bootstrap] 首登待补数据且在途轮未在 %s 内收尾：已让路且换票锁在握，继续换代",
				swapWaitBudget)
		} else {
			// fail-safe：超时即中止本次换代。**不强关旧库**（那正是 database is closed 的成因）。
			// 下一轮 maybeAutoBootstrap 条件仍成立 ⇒ 会自动重试，不是永久放弃。
			log.Printf("[bootstrap] 在途同步轮未在 %s 内收尾，本次换代中止（不强关旧库，避免 database is closed；下一轮自动重试）",
				swapWaitBudget)
			// worker 指针已摘，必须先挂回去，否则调度器后续轮次会因无 worker 全程早退。
			// 带原票重挂（rebindAccess），避免把在线态降级成需惰性换票。
			mountWorker(sess, rebindAccess, l.uploadDir)
			return map[string]any{"error": "同步轮尚未结束，全量同步已推迟到下一轮，请稍后重试",
				"bootstrap_deferred": true}, errors.New("bootstrap deferred: scheduler round still in flight")
		}
	}
	// v21.165 修复（桌面首装同步显示缺陷，204 轮走查定罪·真机铁证）：
	//
	// 定罪：此前直接 `oldGen := database.GetDB()` 把**全局句柄**当搬运源传给
	// `bootstrap.Run`。而该句柄的连接池被刻意压到 `MaxOpenConns(2)`（§93 纪律），
	// 且**全进程 61 处生产代码共用它**——UI 每次 `ListNotes`、同步轮、账号卡片都在抢。
	// 登录瞬间的时序是：UI 渲染主视图发起列表查询（占 1 条连接）+ carry 发起
	// `oldDB.Query`（要第 2 条）⇒ 池瞬时耗尽 ⇒ carry 无限等待拿不到连接。
	// `busy_timeout(5000)` 只管 SQLite 文件锁，**管不到 database/sql 的连接池等待** ⇒
	// goroutine 静默卡死（真机日志止于 `step9.1 carryOverOutbox begin`，无 done 无失败），
	// 于是 Run 永不返回 → `database.SetDB(newDB)` 永不执行 → 前端继续读旧代句柄 →
	// 「库里明明有服务端同步来的笔记，UI 却恒显共 0 条笔记」。
	//
	// 修法：搬运源改为**独立打开的专用句柄**（自带独立连接池），与全局池彻底解耦，
	// 不再与 UI/同步轮抢连接。路径经 `localstore.Layout` 从 profile 直接推算
	// （**不依赖 database.DBFilePath()**——桌面换代链走 OpenGenDB 而非 database.Init，
	// 该路径在本进程内从未被设置，用它会静默回落、修复形同虚设）。
	profileDir := filepath.Dir(l.uploadDir)
	lay := localstore.Layout{ProfileDir: profileDir}
	carrySrc := database.GetDB() //nolint:staticcheck // 兜底：推不出路径时沿用全局句柄（旧行为）
	if cur, aerr := lay.Active(); aerr == nil && cur != "" {
		if own, oerr := localstore.OpenGenDB(lay.GenDB(cur)); oerr == nil {
			defer own.Close()
			carrySrc = own
		} else {
			log.Printf("[bootstrap] carry source reopen failed (%v), fallback to global handle", oerr)
		}
	}
	rn := &bootstrap.Runner{ProfileDir: profileDir, AttachDir: l.uploadDir}
	res, err := rn.RunCtx(ctx, sess, carrySrc)
	if err != nil {
		return map[string]any{"error": err.Error()}, err
	}
	// v21.201 A1：BootstrapNow 同时是【强制全量同步】的实现体 ⇒ **仅当由用户
	// 显式触发时**清掉熔断与冷却计数。自动重建路径（scheduledRound /
	// maybeAutoBootstrap）走的是 bootstrapNowInternal，不能在此清零——
	// 否则熔断器会被自己解除，等于没装（无限重建照旧）。
	if !autoBootstrapInternal.Load() {
		resetAutoBootstrap() // 用户显式处置：清熔断 + 清冷却
	}
	// 新代接管全局 + 重挂 worker（token 随迁，锚已由 bootstrap 写入新代）。
	newDB, oerr := localstore.OpenGenDB(filepath.Join(filepath.Dir(l.uploadDir),
		"generations", res.Generation, "notes.db"))
	if oerr != nil {
		return map[string]any{"error": oerr.Error(), "generation": res.Generation}, oerr
	}
	// v21.169（用户实机缺陷·首登「加载文件失败」弹窗）：原顺序是
	//「SetDB → old.Close() → setWorker(新)」，而 setWorker 之前那一瞬，
	//**已在途的同步轮仍握着即将被 Close 的旧库句柄** ⇒ 报 `sql: database is closed`
	//（真机日志：17:53:52 「generation g-000002 sealed+committed」与
	//   「后台同步轮次失败: sql: database is closed」**同秒**）。
	// pauseScheduler 只拦「新轮」，拦不住已开跑的那一轮。
	//
	// 修法（顺序即修复）：**先摘 worker 指针 → 再关旧库 → 最后挂新 worker**。
	// 摘指针后已在途轮的下一次 `workerPtr()` 读到 nil 会走 ErrScheduleNoWorker 早退，
	// 不再触碰已关闭的库；且 setWorker(nil) 已被本文件 L54-56 明确列为
	// 「只有需要确实置空时才允许」的场景，本处正是该场景。
	oldDB := database.SetDB(newDB)
	setWorker(nil) // 关键：先摘，旧库 Close 前不再有 worker 能取到它
	if oldDB != nil && oldDB != newDB {
		// ⭐ v21.216（211e 实机 `14:15:38 sql: database is closed`）：**延迟关旧库**。
		//
		// 原实现此处**立即** Close。让路信号只保证在途轮在**下一个分页边界**返回
		//（RunOnce 内），但一次弱网 Pull 往返本身可超 3s（实机：让路已发、换代
		// 3s 窗口耗尽继续接管，接管后旧轮才撞上已关闭的句柄）。该轮报错退出
		// 虽无害（返回值丢弃），但**连带效应**是本轮被记为失败（退避+1），
		// 且它在 waitFirstRound 期间的失败会被登录链路看到。
		//
		// ⭐⭐ v21.217c（212 实机复测：弹窗**仍在**）：**本进程内不关旧库**。
		//
		// 定罪升级：drain/定时修复只保护了**调度轮**，但弹窗的真正来源是
		// **前端绑定调用**（ListNotes/ListFolders 直读全局 database.DB，
		// 不经过 worker）——换代 8s+ 窗口内前端已登录跳转、loadFolders 反复
		// 调用，撞上 oldDB.Close() ⇒ 弹「sql: database is closed / 加载文件夹
		// 失败」。**任何"等多久再关"都有窗口**（前端调用无引用计数、随时
		// 可能进来）。唯一无窗口方案：不关。
		// 代价=一个空闲 SQLite 连接池（数 MB 内存、零 IO），换代低频；
		// 进程退出由 OS 回收，gracefulShutdown 照常关当前（新代）句柄。
		leftoverOldGen.Store(oldDB) // 关机时统一关闭（gracefulShutdown），测试 cleanup 亦可关
		log.Printf("[bootstrap] 旧代句柄保留不关（防前端绑定调用撞已关闭句柄；关机统一回收）")
	}
	setWorker(synclocal.NewWorker(newDB, synclocal.NewClient(sess.ServerURL, sess.AccessToken, sess.DeviceID)))
	workerPtr().AttachDir = l.uploadDir
	l.deviceID = sess.DeviceID
	l.sessionRestored = true
	// R-1 诊断（v21.153）：换库收口留痕（代际/搬运数）——与前端通知缺口取证共用时间线。
	log.Printf("[bootstrap] generation %s takeover: carried=%d carry_conflicts=%d worker remounted",
		res.Generation, res.Carried, res.CarryConflict)
	// v21.165（204 轮走查定罪的另一半）：换代完成后**推进换代代数**，
	// 让前端的 installSyncRefreshPolling 能感知「数据所在的库已换」。
	//
	// 此前该轮询只看 `rounds`（后台同步轮次），而 bootstrap 换代**不产生同步轮次**
	// ⇒ 前端在换代前渲染的空列表永远不会被刷新（真机症状：库里已有服务端同步来的笔记，
	// UI 恒显「共 0 条笔记」直到重启）。此处推进 `genEpoch` 后，轮询在同一个
	// SyncScheduleStatus 响应里多带一个 `gen_epoch` 字段，前端零新增绑定即可刷新。
	genEpoch.Add(1)
	// ⭐ v21.212：代数**落进新代库**，使它跨进程单调（见 genEpoch 的定罪）。
	// 必须在 setWorker 之后写——此时 database.DB 已是新代句柄。
	persistGenEpoch(database.DB)
	log.Printf("[bootstrap] 换代代数推进并持久化: gen_epoch=%d", genEpoch.Load())
	return map[string]any{"generation": res.Generation, "epoch": res.Epoch, "watermark": res.Watermark,
		"files": res.Files, "carried": res.Carried, "carry_conflicts": res.CarryConflict,
		"order_dropped": res.CarryDropped}, nil
}

// SyncStatus 数据中心同步区块（§82 子集）。
func (l *LocalService) SyncStatus() (map[string]any, error) {
	if err := synclocal.EnsureTables(database.DB); err != nil {
		return nil, err
	}
	st, err := synclocal.Status(database.DB)
	if err != nil {
		return nil, err
	}
	// ⭐ v21.218（212 实机深度核查定罪）：此处原注释写「与 AuthStatus 同口径」，
	// 实现却只判内存票（workerHasToken）—— **注释与实现矛盾，是漏改残留**。
	// 后者**重启后必为 false**（access token 只存内存，重启归零），于是同一进程内
	// 两个绑定对「是否已登录」给出相反答案：账号卡片读 AuthStatus 显示已连接，
	// 数据中心读 SyncStatus 显示未登录 ⇒ 212 实机「偶尔显示已登录、笔记仍未同步」。
	//
	// 修正为与 AuthStatus 同口径：online || recoverable。
	online := workerHasToken(workerPtr())
	recoverable := false
	if p, lerr := localauth.Load(database.DB); lerr == nil {
		if _, _, oerr := localauth.OpenSession(database.DB); oerr == nil {
			recoverable = len(p.RefreshSealed) > 0
		}
	}
	st["online"] = online
	st["session_recoverable"] = recoverable
	st["logged_in"] = online || recoverable
	st["relogin_required"] = !online && !recoverable
	return st, nil
}

// enqueueNote 本地变异成功后的 Outbox 入队（§25 Dirty 规则；未登录零副作用）。
func (l *LocalService) enqueueNote(noteID int64, operation string) {
	if workerPtr() == nil {
		return
	}
	if err := synclocal.EnqueueNoteOp(database.DB, noteID, operation); err != nil {
		// 入队失败不回滚业务写（P0-D 过渡语义；P2 Facade 收口为同事务）。
		log.Printf("[sync] enqueue %s note %d failed: %v", operation, noteID, err)
	}
}

// AttachmentStatus 数据中心可见性（P1 批次3）：待下载/待修复计数 + 上传账本待传数。
func (l *LocalService) AttachmentStatus() (map[string]any, error) {
	return dbReadGuard(func() (map[string]any, error) {
		pending, errored, err := synclocal.PendingAttachmentStats(database.DB)
		if err != nil {
			return nil, err
		}
		var toUpload int
		if qerr := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_attachment_journal WHERE status='LOCAL'`).Scan(&toUpload); qerr != nil {
			toUpload = 0
		}
		return map[string]any{"pending_download": pending, "errored": errored, "pending_upload": toUpload}, nil
	})
}

// RepairAttachments 篡改/缺件修复入口（T-09 人工触发面）：重校验 OK 行、坏件重排
// 下载队列并即刻跑一轮同步。返回重排队数。
func (l *LocalService) RepairAttachments() (int, error) {
	requeued, err := synclocal.ReverifyAttachments(database.DB, l.uploadDir)
	if err != nil {
		return requeued, err
	}
	if workerPtr() != nil {
		if _, rerr := workerPtr().RunOnce(context.Background()); rerr != nil {
			return requeued, rerr
		}
	}
	return requeued, nil
}

// ---- 冲突中心数据面（批次8-Alt 刀1，§137~§139 可见/处置面）----
//
// 三个绑定：ConflictList / OutboxList（只读）+ ConflictResolve（处置）。语义矩阵与允许组合
// 的唯一事实源在 synclocal.allowedResolutions，前端只渲染 allowed_resolutions 里的按钮，
// 服务端仍独立校验（不信任 UI）。红线 1/6：覆盖本机内容的处置必须 confirmed=true。

// ConflictPage 冲突分页结果（Items 按 id 倒序 = 最新在前）。
type ConflictPage struct {
	Items []synclocal.Conflict `json:"items"`
	Total int                  `json:"total"`
}

// OutboxPage 挂账 op 分页结果（Items 按 seq 倒序）+ 按状态全量计数（不受 statuses 过滤影响）。
type OutboxPage struct {
	Items  []synclocal.OutboxOp `json:"items"`
	Counts map[string]int       `json:"counts"`
	Total  int                  `json:"total"`
}

// conflictWorker 冲突处置用的 worker：未登录（worker 未挂载）时也要能处置——便携导入的档案
// 可能带着冲突记录离线打开（批次6 账在物在），此时处置只写本地库、不碰网络。
func (l *LocalService) conflictWorker() *synclocal.Worker {
	if workerPtr() != nil {
		return workerPtr()
	}
	w := synclocal.NewWorker(database.DB, synclocal.NewClient("", "", ""))
	w.AttachDir = l.uploadDir
	return w
}

// ConflictList 读冲突列表；onlyOpen=false 时含已处置项（审计/回顾用）。
func (l *LocalService) ConflictList(onlyOpen bool, limit, offset int) (*ConflictPage, error) {
	return dbReadGuard(func() (*ConflictPage, error) {
		if err := synclocal.EnsureTables(database.DB); err != nil {
			return nil, err
		}
		items, total, err := synclocal.ListConflicts(database.DB, onlyOpen, limit, offset)
		if err != nil {
			return nil, err
		}
		return &ConflictPage{Items: items, Total: total}, nil
	})
}

// OutboxList 读本机挂账列表；statuses 为空串表示不过滤，逗号分隔可多选
// （PENDING,SENDING,FAILED,CONFLICT,ACKED）。
func (l *LocalService) OutboxList(statuses string, limit, offset int) (*OutboxPage, error) {
	return dbReadGuard(func() (*OutboxPage, error) {
		if err := synclocal.EnsureTables(database.DB); err != nil {
			return nil, err
		}
		var list []string
		for _, s := range strings.Split(statuses, ",") {
			if s = strings.TrimSpace(s); s != "" {
				list = append(list, s)
			}
		}
		items, counts, total, err := synclocal.ListOutbox(database.DB, list, limit, offset)
		if err != nil {
			return nil, err
		}
		return &OutboxPage{Items: items, Counts: counts, Total: total}, nil
	})
}

// ConflictResolve 处置一条冲突。resolution ∈ keep-local | keep-remote | drop-local | recreate；
// keep-remote 会覆盖本机未同步修改，必须 confirmed=true，否则拒（错误串可直接上屏）。
func (l *LocalService) ConflictResolve(conflictID int64, resolution string, confirmed bool) (*synclocal.ResolveResult, error) {
	if err := synclocal.EnsureTables(database.DB); err != nil {
		return nil, err
	}
	res, err := l.conflictWorker().ResolveConflict(conflictID, resolution, confirmed)
	if err != nil {
		return nil, err
	}
	// 1.0.218：处置可能产生新的待推 op（重投 / 重建）——顺手敲一轮调度，兑现
	// "处置后自动同步"，而不是让用户等下一个空闲周期或再点一次"立即同步"。
	// 调度器未启动/未登录时 wakeScheduler 静默无害。
	wakeScheduler()
	return res, nil
}

// ConflictCopyResult 另存副本回执（1.0.218）。Note=本机新笔记；
// FolderName=落点文件夹名（""=我的笔记）；FolderFallback=原文件夹已不在本机、回落「我的笔记」。
type ConflictCopyResult struct {
	Note           *model.Note `json:"note"`
	FolderName     string      `json:"folder_name"`
	FolderFallback bool        `json:"folder_fallback"`
}

// ConflictSaveCopy 另存副本（1.0.218，取代原"前端直连 CreateNote(folderID=null)"实现）：
//
//	读冲突行（fail-closed：不存在/已处置/非笔记一律拒）→ 解析本机载荷（synclocal.
//	PlanConflictCopy，加密件/非笔记件拒绝）→ 解析 folder_sync_id 落回**原文件夹**
//	（本地查不到时回落「我的笔记」并如实标记 FolderFallback，不静默）→
//	复用 l.CreateNote 核心：新 sync_id 新建 + 正常入 outbox（红线 27："副本必须换身份"不变）。
func (l *LocalService) ConflictSaveCopy(conflictID int64) (*ConflictCopyResult, error) {
	if err := synclocal.EnsureTables(database.DB); err != nil {
		return nil, err
	}
	var (
		entityType, kind, payload, status, syncID string
	)
	err := database.DB.QueryRow(`SELECT entity_type, kind, local_payload, status, entity_sync_id
		FROM sync_conflicts WHERE id = ?`, conflictID).
		Scan(&entityType, &kind, &payload, &status, &syncID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, synclocal.ErrConflictNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "open" {
		return nil, synclocal.ErrConflictAlreadyResolved
	}
	if entityType == "" {
		entityType = synclocal.InferEntityType(database.DB, syncID) // 旧库遗留行补型（同 ListConflicts 口径）
	}
	if entityType != "NOTE" {
		return nil, errors.New("仅笔记支持另存副本（文件夹/排序冲突不适用）")
	}
	plan := synclocal.PlanConflictCopy(payload)
	if !plan.OK {
		return nil, errors.New(plan.Reason)
	}
	var (
		folderID   *int64
		folderName string
		fallback   bool
	)
	if plan.FolderSyncID != "" {
		var fid int64
		var name string
		if qerr := database.DB.QueryRow(`SELECT id, name FROM note_folders WHERE sync_id = ? AND user_id = ?`,
			plan.FolderSyncID, l.userID).Scan(&fid, &name); qerr == nil {
			folderID, folderName = &fid, name
		} else {
			// 原文件夹已不在本机（离线期被删等）：fail-soft 落「我的笔记」并标记，不静默。
			fallback = true
		}
	}
	n, err := l.CreateNote(plan.Title, plan.Content, plan.Tags, folderID)
	if err != nil {
		return nil, err
	}
	return &ConflictCopyResult{Note: n, FolderName: folderName, FolderFallback: fallback}, nil
}

// GetVersion 返回桌面构建版本号（注入链：-ldflags "-X main.Version=..."）。
func (l *LocalService) GetVersion() string {
	return Version
}

// ReportJSError 前端全局错误上报通道（v21.194 白屏根治轮）：window.onerror/
// unhandledrejection 钩子经此把 JS 异常写入 Go 日志——白屏类缺陷（页面活着但
// Vue 未挂载、无 console 通道）的永久取证面。摘要由前端裁剪（≤500 字符），仅落日志。
func (l *LocalService) ReportJSError(summary string) {
	s := strings.TrimSpace(summary)
	if s == "" {
		return
	}
	if len(s) > 500 {
		s = s[:500]
	}
	log.Printf("[jserr] %s", s)
}

// maybeAutoBootstrap E4-5 统一触发入口（登录与会话恢复两条路径都挂）：
// 本地 folders 为空 且 本轮会话未自动快照过（sync_state 标记防重复）→ 快照全量同步。
// 背景：服务端旧实体的 CREATE 事件超出 changes 保留窗口后，增量 pull 永远拿不到，
// 必须走快照（新设备 folders 永不落地的根因）。BootstrapNow 内部自带换票与调度暂停。
func (l *LocalService) maybeAutoBootstrap() {
	// v21.152：无可用会话（离线单机 / 凭据已失效）时不尝试——否则每次启动都白跑一次
	// 快照请求并把 ErrNeedRelogin 打成"自动快照失败"日志，噪声掩盖真实状态。
	if !workerHasToken(workerPtr()) && !sessionRestorable() {
		return
	}
	var ff int
	_ = database.DB.QueryRow(`SELECT COUNT(*) FROM note_folders`).Scan(&ff)
	if ff > 0 {
		return
	}
	var marked string
	_ = database.DB.QueryRow(`SELECT value FROM sync_state WHERE key='auto_bootstrapped'`).Scan(&marked)
	if marked == "1" {
		return
	}
	log.Printf("[bootstrap] 本地无文件夹且本轮未自动快照过：执行一次快照全量同步（增量 pull 拉不到窗口外历史实体）")
	// v21.201 A1：登录路径同样受熔断器约束（登录 → 本地空 → 自动快照，
	// 与调度器 auto-bootstrap 是同一类重建风暴的两个入口）。
	if allow, tripped := autoBootstrapAllow(time.Now()); !allow {
		if tripped {
			log.Printf("[bootstrap] 自动重建已熔断（登录后自动快照跳过）：请用【账号与设备 → 强制全量同步】手动恢复")
		} else {
			log.Printf("[bootstrap] 自动快照退避/冷却中（距上次自动重建 %s 内），跳过", autoBootstrapCooldown)
		}
		return
	}
	if _, berr := func() (any, error) {
		withInternalBootstrap()
		defer clearInternalBootstrap()
		// v21.203：进程 ctx —— 关机时这次自动快照能被取消。
		return l.bootstrapNowCtx(appCtx())
	}(); berr != nil {
		// ⭐ v21.218：失败**不写长冷却**，改走短退避（30s~2min 后自动重试）。
		// 原实现走 autoBootstrapDone(now,false) 会写 autoBootLast ⇒ 命中 5 分钟冷却
		// ⇒ 一次网络失败即让自动快照停摆 5 分钟（212 实机「失败一次就一直空白」的机制）。
		autoBootstrapFailed()
		recordNetworkFailure(berr) // P0-3
		log.Printf("[bootstrap] 自动快照失败（将自动重试，也可在账号与设备手动强制全量同步）: %v", berr)
		return
	}
	// A2：换代后自检（不一致才算循环证据）。
	autoBootstrapDone(time.Now(), l.verifyAnchorsAfterTakeover())
	_, _ = database.DB.Exec(`INSERT INTO sync_state(key,value) VALUES('auto_bootstrapped','1')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`)
}
