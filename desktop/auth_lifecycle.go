package main

// auth_lifecycle.go —— 批次 4 刀 3：设备会话生命周期绑定面。
//   ensureAccess —— 冷启动恢复/离线唤醒时的"换票挂工"：OpenSession(本地 DPAPI/NPD1 面)
//     → synclocal.Refresh（网络）→ SaveRefresh（一次一换即存）→ 挂 worker。
//     离线冷启动 = 网络失败仅记日志（废票暴露延后到下次触网），**不**吊销本地件。
//     服务端明确拒绝（ErrAuthRevoked/401 语义）= 登出级降级：清 refresh 封存件（废票），
//     device_secret/PIN 封套保留（重登沿用设备身份）。
//   30 天重验（N=30 用户拍板）：门控纯函数 + RevalidateNow 显式通道；worker 挂上后的
//     周期性由后续 UI 调度切片消费（本刀不动 worker 内部，保持批次纪律最小切口）。

import (
	"context"
	"errors"
	"log"
	"time"

	"stickynote-desktop/localauth"
	"stickynote-desktop/synclocal"
	"stickynote/database"
)

// revalidateWindow N=30 天（计划 §五-3 已拍板）。
const revalidateWindow = 30 * 24 * time.Hour

// ErrNeedRelogin 本机凭据已失效（封存 refresh 缺失/被吊销），换票不可能成功 —— UI 应引导重新登录。
// 文案直接上屏（v21.152：旧行为是把裸 "NO_REFRESH" 抛给用户，生产实测弹出该字样，
// 用户无从判断该做什么）。
var ErrNeedRelogin = errors.New("本机登录凭据已失效，请重新登录")

// exchangeMu 换票单飞（v21.152）：ensureAccess 可被调度轮 / 立即同步 / 重建同步 / 重验四个入口
// 并发调用，而服务端 refresh 是**一次一换 + 族重放吊销**——同一张封存票被并发花两次，
// 第二次必 401 并吊销整族，随后 DropRefreshSealed 又把并发中刚换到的新票清掉：
// 最终"已登录（档案在）但永久换不了票"（生产症状：重建同步恒 NO_REFRESH，只能重登）。
//
// ⭐ v21.212：由 `sync.Mutex` 改为**容量 1 的 channel 语义锁**（加 ctx 变体）。
// 原因：裸 Mutex 的 `Lock()` **不可取消**——一旦有 goroutine 持锁做 15s 换票往返，
// 其余调用者全部排队干等且无法响应 ctx 取消。这正是 211 实机
// 「关机后调度器仍未在预期时限内退出」的最后一个阻塞点：
// 即使 ctx 已广播，排队者仍要等持锁者跑完整个网络往返。
var exchangeMu = make(chan struct{}, 1)

// lockExchange 可取消地取换票锁。
// 返回 false = ctx 已取消（关机/换代路径），调用方应立即放弃而不是排队干等。
func lockExchange(ctx context.Context) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case exchangeMu <- struct{}{}: // 容量 1 = 拿到锁
		return true
	case <-ctx.Done():
		return false
	}
}

// unlockExchange 释放换票锁。
func unlockExchange() { <-exchangeMu }

// shouldRevalidate 纯函数门控（可测）：零值=从未重验视为到期；窗口内=不需要。
func shouldRevalidate(last time.Time, now time.Time) bool {
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= revalidateWindow
}

// ⭐⭐ v21.222（215 实机 + 用户质疑「为什么不能打开后立即联网」）：启动即换票。
//
// 215 实机症状：每次重启都停在「恢复中」，点【立即重试】也无效。
// 取证：内网 `/api/auth/refresh` **3ms** 返回 401（网络与服务端都正常），
// 而日志出现 `[shutdown] 换票仍在途（>16s）` ⇒ **不是网络问题，是没人去触发换票**。
//
// 根因：启动恢复路径（`restoreDeviceSession`）只**挂空票 worker** ——
// access token 只存内存，重启必为空；而换票是**惰性**的
// （只在调度器第一轮 / 用户手动 SyncNow / BootstrapNow / RevalidateNow 时才触发）。
// 于是"有凭据 + 网络好"却迟迟不换票，UI 一直显示「恢复中」。
//
// 本函数 = 启动后主动换一次票，让「已连接」在开窗后几秒内出现，
// 而不是等调度器那一轮（还要叠加退避/熔断计数）。
//
// 设计约束：
//
//	· **不阻塞起窗**（调用方放 goroutine）——换票最长 15s + 弱网重试；
//	· 网络类失败**不丢封存票**（ensureAccess 内部已保证），下一轮调度器会再试；
//	· 已经带票在位时直接返回，不做无谓往返。
func (l *LocalService) ensureAccessOnStartup() (bool, error) {
	// 已在位且带票 ⇒ 无需换票（重复启动/热重载场景）
	if workerHasToken(workerPtr()) {
		return true, nil
	}
	// 从未登录 / 无封存凭据 ⇒ 正常离线单机态，不是错误（不刷警告日志）
	if _, _, err := localauth.OpenSession(database.DB); errors.Is(err, localauth.ErrNoProfile) {
		return false, nil
	}
	return ensureAccess(l.uploadDir)
}

// ensureAccess 幂等保证 worker 在位（有有效 access）。返回是否已就绪。
// 设备会话语义（走查 4 实锤修正）：**档案可开即挂 worker**（哪怕换票失败）——否则断网期
// 的本地编辑因 syncWorker==nil 漏入队（§25 Dirty 规则旁路），联网后无账可推。
// 换票成功 → 带票挂载/为在位空票 worker 补票；网络类失败 → 空票挂载（后续 SyncNow 触网
// 自然失败，Outbox 持续记账）；服务端权威 401 → 清废票并卸挂载（档案作废，回离线单机语义）。
func ensureAccess(attachDir string) (bool, error) {
	return ensureAccessCtx(appCtx(), attachDir)
}

// ⭐ v21.211（210 实机：关机 72 秒不退出）：ctx 版换票闸。
//
// 为什么需要独立变体：`ensureAccess` 是**每个**同步轮的第一步，换票是不可取消的
// 15s 网络往返（`Refresh` 原为裸函数）。关机 `cancelShutdown()` 广播 appCtx 后，
// 走 ctx 版即可立即中断；原签名保留为薄封装（供 UI 同步调用点与测试使用）。
//
// ⭐ v21.213 语义收窄：ctx 现在**只用于「排队取锁」的可取消**，
// **不再用于中断换票本身**（见下方 RefreshCtx 处的完整定罪）。
// 换票是「一次一换」的不可撤销操作，中途取消会让服务端已吊销旧票、
// 而本地把作废票当有效票留着 ⇒ 下次启动必然 401。正确性优先于关机速度。
func ensureAccessCtx(ctx context.Context, attachDir string) (bool, error) {
	// ⭐ v21.212：可取消取锁。ctx 已取消（关机/换代）时立即放弃，
	// 不再排队等持锁者跑完整个换票往返（211 实机的最后一块关机阻塞）。
	// ⚠ v21.213：**已持锁者拿到的是独立的换票 ctx**（见下），故本次取消不会
	// 让一张"服务端已轮转、本地未落盘"的票悬空。
	if !lockExchange(ctx) {
		return false, ctx.Err()
	}
	// ⚠ v21.222：解锁标记。弱网重试链会**主动释放**锁并在锁外继续，
	//   此时 defer 不能再解一次（channel 锁多重解会 panic）。
	retryLocked := true
	defer func() {
		if retryLocked {
			unlockExchange()
		}
	}()
	sess, refreshToken, err := localauth.OpenSession(database.DB)
	if errors.Is(err, localauth.ErrNoProfile) {
		return workerPtr() != nil && workerHasToken(workerPtr()), nil // 从未登录：正常离线单机态
	}
	if err != nil {
		// 换机/篡改/非 Windows：本地件不可解。保留封存档位（可能是瞬时 DPAPI 故障），
		// 上抛让调用方降级为"需重登"。
		return false, err
	}
	mountIfabsent(sess, attachDir) // 档案可开即在位（空票占位也要挂上）
	if workerHasToken(workerPtr()) {
		return true, nil // 已带票在位
	}
	if refreshToken == "" {
		// 封存票缺失 = 主动登出残留或已被吊销；换票不可能成功，别让调度器空转到停手。
		return false, ErrNeedRelogin
	}
	if sess.ServerURL == "" {
		return false, errors.New("NO_SERVER")
	}
	// v21.203：换票前抓一份 CAS 令牌（当前封存件原始字节）。网络往返最长 15s，
	// 期间用户可能切账号/登出 ⇒ 回来的新票属于旧账号，必须丢弃而不是覆盖新档案。
	expectSealed := localauth.CurrentRefreshSealed(database.DB)
	// ⭐⭐ v21.213（211b 实机铁证·登录反复失效的**真正源头**）：换票**不可被关机 ctx 取消**。
	//
	// 定罪（211b 实机日志 12:10:04 + 12:11:25 同实例相邻两行）：
	//
	//	12:10:04 [sched] 后台同步轮次失败: 请求已取消: context canceled
	//	12:11:25 [sched] 后台同步轮次失败: AUTH_REVOKED     ← 下次启动首轮即吊销
	//
	// 服务端 refresh 是「**一次一换**」：`RefreshCtx` 的请求一旦抵达服务端，
	// 服务端**已经轮转并吊销了旧票**，此时即便客户端因 ctx 取消而丢弃响应，
	// 本地那张旧封存票也**已经作废**。而下一次启动仍拿它去换票 ⇒ 必然 401。
	// `AUTH_REVOKED` 分支再 `DropRefreshSealed` 把最后一张也清掉 ⇒
	// 本地彻底回到「从未登录」⇒ **用户每次重登都能登进去，但下次启动又掉**——
	// 这正是 211b 实机「关掉再打开登录失效 / 反复重登仍失败」的稳定复现机制。
	//
	// 结论：**换票是一个不可撤销的原子操作，中途取消比失败更糟**。
	// 故此处用与关机 ctx **无关**的独立超时窗（15s = Refresh 自身上限），
	// 让换票要么完整成功（票已落盘）、要么完整失败（票还在本地）。
	//
	// 关机时最多多花 15s——这是**正确性代价**：一个必然掉登录的缺陷，
	// 比一次 15s 的关机延迟严重得多。
	exchangeCtx, cancelExchange := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	access, newRefresh, rerr := synclocal.RefreshCtx(exchangeCtx, sess.ServerURL, refreshToken)
	cancelExchange()
	// ⭐ v21.217（配合服务端 grace period）：网络类失败**自动重试一次**。
	// 定罪：弱网下最常见形态是「服务端已轮转成功、响应未达客户端」（211e 实测
	// 换票常态 15s 超时）。客户端此时若直接放弃，下次启动用旧票——服务端命中
	// 已吊销 jti；**有了服务端 60s 宽限期，这次重试反而会被正常续签**。
	// 故网络类错误（超时/EOF/连接拒绝——非 401）等 1s 重试一次；401/凭据错误
	// 不重试（服务端已明确表态，重试无意义）。
	//
	// ⭐⭐ v21.222：**重试不再持锁**。
	//
	// 215 实机铁证：日志反复出现
	//	`[shutdown] 换票仍在途（>16s），放弃等待`，
	// 而用户点【立即重试】**也无效** —— 因为 `SyncNow` 同样要先抢 `exchangeMu`，
	// 而锁被这次「15s + sleep 1s + 15s = 31s」的重试链长期持有。
	// ⇒ 用户唯一的自救出口被自己人堵死了。
	//
	// 改为：首次失败即**释放锁**，重试放到锁外独立进行。
	// 正确性不受影响（重试用的仍是同一张封存票，CAS 语义不变），
	// 但换票锁的最坏持有时间从 31s 降到 15s。
	if rerr != nil && !errors.Is(rerr, synclocal.ErrAuthRevoked) && ctx.Err() == nil {
		log.Printf("[auth] 换票网络失败，释放换票锁后台重试一次（不阻塞其他入口）")
		unlockExchange()
		retryLocked = false
		defer func() {
			// 后台重试期间不再持锁；若它拿到了票，顺手补挂到 worker 上。
			time.Sleep(time.Second)
			retryCtx, cancelRetry := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			a2, r2, err2 := synclocal.RefreshCtx(retryCtx, sess.ServerURL, refreshToken)
			cancelRetry()
			if err2 == nil {
				if e := localauth.SaveRefreshCAS(database.DB, r2, expectSealed); e != nil {
					log.Printf("[auth] 换票后台重试成功但 CAS 落盘失败: %v", e)
					return
				}
				if w := workerPtr(); w != nil {
					w.SetToken(a2)
				}
				log.Printf("[auth] 换票后台重试成功（弱网自愈）")
			} else {
				log.Printf("[auth] 换票后台重试仍失败（保留本机凭据，等待下一轮）: %v", err2)
			}
		}()
	}
	if rerr != nil {
		if errors.Is(rerr, synclocal.ErrAuthRevoked) {
			// ⭐ v21.213：服务端权威拒绝。
			//
			// ⚠ **不再 DropRefreshSealed**（v21.203 旧行为）。理由（211b 实机）：
			// 一次 AUTH_REVOKED **不足以证明**本地封存票已死 —— 它极可能是
			// 「上一次换票被取消导致本地留了作废票」的**下游后果**，
			// 而那张票本可以用「重新登录」之外的方式修复。清掉它等于
			// 把「一次可恢复的降级」变成「本地凭据归零」，用户只能重登，
			// 而重登后又会因同样的取消窗口再次掉线 ⇒ 死循环。
			//
			// 现在：**保留封存件不动**（它可能是别的账号/别的 family 的票，
			// 本地无权判定其死活——服务端才是权威），只清内存票 + 置
			// `sessionRestored=false` 让 UI 明确引导「需要重新登录」。
			// 用户显式重登时 `SignIn` 会带新 family 覆盖它。
			//
			// 风险权衡：保留废票 = 重启时仍会走一次换票并再得 401（一次多余往返，
			// 有界且无副作用）；清废票 = 必然丢失设备会话。**前者显然更优**。
			if workerPtr() != nil {
				workerPtr().ClearToken()
			}
			if lsInstance != nil {
				lsInstance.sessionRestored = false
			}
			wakeScheduler() // 立刻走一轮确认状态（无票轮会被门挡住，属预期）
			log.Printf("[auth] 凭据被服务端拒绝：保留本地封存件（不作废），等待用户显式重新登录")
			return false, rerr
		}
		return false, rerr // 网络类错误：挂载（若有）保持空票记账，不动本地件。
		// ⭐ v21.217b（212 实机症状3）：**网络类失败不清 sessionRestored**。
		// 定罪：离线时 refresh `no such host`（网络真不通，P1 重试也失败）——
		// 原实现走到统一清标志的路径 ⇒ allowScheduledRound 的门被关 ⇒
		// 恢复网络后的轮次全被挡 ⇒ 用户必须重启应用才恢复登录态。
		// 正确语义：**离线 = 会话可恢复**（封存件在位、服务端未拒），只有
		// 401（服务端权威拒绝）才置 sessionRestored=false。本函数的网络类
		// 失败路径到此为止，不再触碰该标志（上面的 401 分支保持原语义）。
	}
	if newRefresh != "" {
		if serr := localauth.SaveRefreshCAS(database.DB, newRefresh, expectSealed); serr != nil {
			if errors.Is(serr, localauth.ErrProfileChanged) {
				// 档案在这 15s 里被换掉了：这张新票属于**另一个账号**的 family，
				// 写进去会让新账号一换票就 401。丢弃并明确拒绝。
				log.Printf("[auth] refresh rotated but profile changed concurrently "+
					"(account switched/logged out); discarding new token: %v", serr)
				if workerPtr() != nil {
					workerPtr().ClearToken()
				}
				return false, localauth.ErrProfileChanged
			}
			return false, serr // 轮转票未存成功：不持废票空转，但挂载继续记账
		}
	}
	if workerPtr() != nil {
		workerPtr().SetToken(access) // 在位补票
	} else {
		mountWorker(sess, access, attachDir)
	}
	// ⚠ v21.203：**不**在此复位 PIN 熔断。Refresh 成功是机器行为（可能发生在用户
	// 离线、后台调度轮期间），而熔断保护的是"人的暴力破解"。挂在后台换票上等于
	// 用户连错 9 次后一次后台换票就把计数清零，10 次上限形同虚设。
	// 复位只发生在两处用户显式动作：登录成功（finishRemoteLogin）/ 正确 PIN 解锁。
	return true, nil
}

// mountIfabsent 档案可开即确保挂载（空票占位）；返回"挂载且已带票"。
func mountIfabsent(sess *localauth.Session, attachDir string) bool {
	if workerPtr() == nil {
		mountWorker(sess, "", attachDir)
		return false
	}
	return true
}

// workerHasToken 在位 worker 是否已持票（绑定层早期挂载后判断是否需换票）。
func workerHasToken(w *synclocal.Worker) bool { return w != nil && w.HasToken() }

func mountWorker(sess *localauth.Session, access, attachDir string) {
	setWorker(synclocal.NewWorker(database.DB, synclocal.NewClient(sess.ServerURL, access, sess.DeviceID)))
	workerPtr().AttachDir = attachDir
	// 工一挂上就敲调度器：带票→立刻补一轮（终态停手也由此解除）。
	// v21.142：**空票也要敲** —— 冷启动恢复的设备会话是空票挂工，配合 allowScheduledRound 的
	// sessionRestorable 分支，这一敲会让调度器立刻跑一轮去惰性换票，从而恢复自动同步；
	// 此前"空票不敲 + 门恒 false"是双保险地把冷启动后的同步彻底锁死。
	wakeScheduler()
	if lsInstance != nil {
		lsInstance.deviceID = sess.DeviceID
		lsInstance.sessionRestored = true
	}
}

// RevalidateNow 显式重验（换票 + ping state 落重验时刻；服务端 401 走降级）。
func (l *LocalService) RevalidateNow() (map[string]any, error) {
	if !workerHasToken(workerPtr()) {
		if ok, err := ensureAccess(l.uploadDir); err != nil || !ok {
			return map[string]any{"revalidated": false}, err
		}
	}
	st, err := workerPtr().State()
	if err != nil {
		if errors.Is(err, synclocal.ErrAuthRevoked) {
			l.demoteToRelogin("state-401")
			return map[string]any{"revalidated": false, "relogin_required": true}, err
		}
		return map[string]any{"revalidated": false}, err
	}
	if terr := localauth.TouchRevalidate(database.DB, time.Now()); terr != nil {
		log.Printf("[auth] touch revalidate: %v", terr)
	}
	return map[string]any{"revalidated": true, "server_epoch": st.ServerEpoch}, nil
}

// demoteToRelogin 会话降级：**卸内存票 + 敲调度器重走换票链**，而不是把 worker 置 nil。
// ⚠ 不清 refresh 封存件——走查实锤：空票 worker 的 401 不代表档案票被服务端拒绝，
// 清封存会把"可恢复会话"误杀成死档。只有 ensureAccess 亲见 Refresh 401（权威拒绝）才清。
//
// ⭐ v21.203 语义修正（审计定罪，最隐蔽的一条）：
//
// 旧实现 `syncWorker = nil`。而 `allowScheduledRound` 的第一道门是
// `if syncWorker == nil { return false }` ⇒ **worker 一旦被置 nil，本进程内调度器
// 永远不再放行任何一轮**。降级的注释写的是"下次访问重走 ensureAccess 换票链"，
// 但调度轮恰恰是那个"下次访问"——它自己被关掉了，只有用户手点【立即同步】/【强制
// 全量同步】能复活（那两条路先调 ensureAccess，不经 allowScheduledRound）。
//
// 真实症状（2026-10-10 实机）：一次 state 401 降级之后，**自动同步永久停摆**，
// 用户界面显示"已连接"但数据永远不动，只能反复手点同步。且 `demoteToRelogin` 有
// 多个调用点（登录探测 401 / SyncNow 401 / 后台轮次 401 / 用户登出），任何一个偶发
// 401 都会造成同样的永久停摆。
//
// 现在：保留 worker 但**清空内存票**（HasToken()=false），`sessionRestored` 置位保持，
// 于是 `allowScheduledRound` 的 `sessionRestorable()` 分支放行 → 下一轮调度
// 自动走 `ensureAccess` 惰性换票 → 换票成功即自愈，失败走既有退避/停手机制。
// 真正需要"等重登"的语义由 `sessionRestored=false` 单独表达。
func (l *LocalService) demoteToRelogin(reason string) {
	if workerPtr() != nil {
		workerPtr().ClearToken() // 清内存票（worker 保留在位，调度门不关）
	} else {
		// worker 本就不在位（用户登出/首次未登录）：确保门是开着的，
		// 否则降级会把调度器永久关停。靠 mountWorker 的正规入口重建。
		if p, _, err := localauth.OpenSession(database.DB); err == nil {
			mountWorker(p, "", l.uploadDir)
		}
	}
	l.sessionRestored = false
	wakeScheduler() // 立刻走一轮：能换票就自愈，换不动由退避兜底
	log.Printf("[auth] session demoted to re-exchange (token cleared, worker kept, sealed kept): %s", reason)
}

// Logout 用户主动登出：卸工 + 清 refresh 封存件（用户意图=作废本机会话），
// device_secret/PIN 封套保留（重登沿用设备身份）。
func (l *LocalService) Logout() error {
	l.demoteToRelogin("user-logout")
	if err := localauth.DropRefreshSealed(database.DB); err != nil {
		log.Printf("[auth] logout drop sealed: %v", err)
		return err
	}
	return nil
}

// SetUnlockPIN / UnlockLocal / RemoveUnlockPIN：PIN 封套绑定面（NPD1 层，跨平台语义一致）。
func (l *LocalService) SetUnlockPIN(pin string) error {
	sess, _, err := localauth.OpenSession(database.DB)
	if err != nil {
		return err // 封存面不可解时无法取 device_secret——先重登
	}
	return localauth.SetPIN(database.DB, pin, sess.DeviceSecret)
}

func (l *LocalService) UnlockLocal(pin string) (bool, error) {
	secret, err := localauth.UnlockPIN(database.DB, pin)
	if err != nil {
		return false, err
	}
	l.unlockedSecret = secret
	l.sessionRestored = true
	return true, nil
}

func (l *LocalService) RemoveUnlockPIN(pin string) error {
	if _, err := localauth.UnlockPIN(database.DB, pin); err != nil {
		return err
	}
	return localauth.RemovePIN(database.DB)
}
