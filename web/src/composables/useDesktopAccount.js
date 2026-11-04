// useDesktopAccount —— DesktopAccountCard 的全部业务逻辑（2026-11-03 巨型 Vue 拆分 刀2）。
// 抽取原则：零行为变化，模板与样式不动；本文件只装逻辑，卡片只做呈现。
//
// DesktopAccountCard —— 批次 8-Alt 刀 6：桌面远端登录入口（账号与设备）。
//
// 为什么必须存在：桌面壳的登录此前是假的——登录页打的 /auth/login 被 transport 映射到
// P0-A 桩 LocalLogin（丢弃账密、恒成功），"退出登录"打的 /auth/logout 是空映射，
// Go 侧 Logout（清 DPAPI 封存票 + 卸工）从没被调用过。本卡片把真绑定接到 UI 上，
// 并让"登出"真的清掉本机凭据。
//
// 三条硬规矩：
//   1. 密码只在提交那一刻存在于内存，不落任何存储、不回显、不进日志；
//   2. 登录/登出后整页 reload：Go 侧会话与 worker 已换状态，前端所有内存缓存立即作废
//      （与便携导入同一收口口径）；
//   3. 错误一律走 formatAuthError 上屏，禁静默吞错——用户必须知道是"密码错"还是"连不上"。
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessageBox } from 'element-plus'
import { message } from '@/utils/message'
import { useAuthStore } from '@/stores/auth'
import {
  remoteLogin, remoteLoginTOTP, logoutDesktop, bootstrapNow,
  formatAuthError, validateLoginInput, hardReloadHome, forceFullResync,
  syncNow, exportDiagnostics
} from '@/api/local/desktopApi'
// ⭐ v21.220：状态取用权收归桌面状态仓库（组件只读不算）。
// 见 desktopStatus.js 顶部的 213 实机归因与三条不变式。
import { useDesktopStatus, refreshPaused } from '@/api/local/desktopStatus'
import { IS_DESKTOP } from '@/api/local/transport'
import { inspectServerURL } from '@/utils/serverUrlInspector'
import { withBase } from '@/utils/appBase'
// v21.200：长耗时绑定调用统一超时上限（杜绝按钮永久 loading）
import { withTimeout, DEFAULT_TIMEOUT_MS } from '@/utils/withTimeout'

export function useDesktopAccount () {
  const router = useRouter()
  const auth = useAuthStore()
  const busy = ref('')
  // v21.196 O2：诊断包导出后的落盘路径（就地展示，便于用户直接去发）
  const diagPath = ref('')

  // ⭐ 状态来自仓库（响应式）。**不再自建 st/sched/refresh/退避链** ——
  // 213 实机证明「组件自己拉一次 + 依赖事件通知」这条路必然卡死在空态。
  const { auth: st, sched, paused, loading, everLoaded, lastError: loadError, ensureFresh } = useDesktopStatus()

  const form = ref({ baseURL: '', username: '', password: '' })
  const formError = ref('')
  // v21.151 两步验证第二步状态：active=服务端要求动态码；token=2 分钟临时票（第一步登录响应携带）。
  const totp = ref({ active: false, token: '', code: '' })

  const loggedIn = computed(() => st.value.logged_in === true)
  const accounting = computed(() => st.value.accounting === true)
  const needAttention = computed(() => sched.value.need_attention === true)
  const heldReason = computed(() => String(sched.value.last_error || ''))
  // v21.201 A1：自动重建熔断 / 冷却可见化（后端 SyncScheduleStatus 已带这两个字段，
  // **零新增绑定** —— 复用既有轮询结果，避免绑定数 50→51）。
  const autoBootTripped = computed(() => sched.value.auto_bootstrap_tripped === true)
  const autoBootCooldownSec = computed(() => {
    const n = Number(sched.value.auto_bootstrap_cooldown_sec || 0)
    return Number.isFinite(n) && n > 0 ? n : 0
  })
  // v21.202 P0-3：最近一次网络/代理失败（后端 recordNetworkFailure 提炼的可执行中文）。
  // 定罪依据：用户实机 2026-10-10 只见日志 `TLS handshake timeout`，界面零提示
  // ⇒ 表现为"登录后白屏 + 不知原因"。
  const lastNetFailure = computed(() => String(sched.value.last_network_failure || ''))
  // v21.202 P0-2 附带：档案在但本机 DPAPI 打不开时，后端从明文列回填 server_url。
  // 此前该分支不回填 ⇒ 登录框回落 placeholder，用户以为"服务器地址被改了"。
  const serverUrlRecovered = computed(() => st.value.server_url_recovered === true)

  // v21.202 P1：服务器地址误配诊断（内网 IP / 缺协议 / 不可解析）。
  // error 级**禁用登录按钮**（必然失败，别让用户白等 15s）；warn 级只提示不阻断
  // （同一内网时确实可用，不能替用户做决定）。
  const urlCheck = computed(() => inspectServerURL(form.value.baseURL))
  const urlCheckBlocked = computed(() => urlCheck.value.level === 'error')

  // v21.218（212 实机深度核查）：卡片状态**三态化**。
  //
  // 定罪：原实现只有「已连接 / 未登录」两态，而 Go 侧 AuthStatus 实际能区分三种情形：
  //   ① online      = 内存有 access 票 ⇒ 正在实时同步
  //   ② recoverable = 无内存票但**封存凭据在位** ⇒ 网络恢复后可自动续上，**并未掉登录**
  //   ③ 真的未登录  = 封存凭据为空（从未登录 / 主动登出 / 已被服务端吊销）
  //
  // 旧 UI 把 ② 归入「未登录」⇒ 用户看到"未登录"就反复重登，而反复重登在弱网下必然又失败
  // ⇒ 「反复重启最终仍未登录」。**这正是 212 实机症状的用户视角成因。**
  // 现把 ② 显式呈现为「恢复中」，并说明"凭据在位、无需重登"。
  const online = computed(() => st.value.online === true)
  const recoverable = computed(() => st.value.session_recoverable === true)
  // ⭐ v21.221：第四态 —— 凭据被服务端作废（401 权威拒绝），**必须重新登录**。
  // 214 实机：此前只有「恢复中」一态，用户对着它干等，笔记列表一直空、无法使用。
  const reloginRequired = computed(() => sched.value.relogin_required === true)
  // 长退避剩余秒数（5/15/30min 阶梯）：让 UI 能说清"什么时候会自己再试"
  const retryInSec = computed(() => {
    const n = Number(sched.value.retry_in_sec || 0)
    return Number.isFinite(n) && n > 0 ? Math.ceil(n) : 0
  })
  const retryInMin = computed(() => Math.ceil(retryInSec.value / 60))

  const stateLine = computed(() => {
    // ⭐ v21.220（213 实机教训的 UI 侧对策）：**还没成功取到过一次状态时，
    // 绝不显示「未登录」** —— 那会把"我还没问出结果"误报成"你没登录"，
    // 用户据此反复重登（213 实机：Go 侧凭据完好、数据齐全，UI 却一直说未登录）。
    if (!everLoaded.value) {
      return loading.value
        ? '正在读取本机登录状态…'
        : '正在重新读取本机登录状态，请稍候（不会影响已保存的数据）'
    }
    if (online.value) {
      return `已连接：${st.value.server_url || '未知服务器'} · 账号 ${st.value.username || '—'}`
    }
    // ⭐ v21.221 第四态：凭据作废，必须重登 —— 与「恢复中等网络」严格区分。
    //   区分理由：前者本地等再久也不会好（唯一出路是重新登录），
    //   后者只需网络恢复。把两者混为一谈会让用户反复重登或无限干等。
    if (reloginRequired.value) {
      return '需要重新登录：本机凭据已被服务器作废，请重新登录后继续同步'
    }
    if (recoverable.value) {
      // 凭据在位但当前无票：绝大多数是网络/代理问题，不是掉登录。
      const tail = retryInSec.value > 0
        ? `（${retryInMin.value > 0 ? `约 ${retryInMin.value} 分钟` : `${retryInSec.value} 秒`}后自动重试）`
        : ''
      return `恢复中：凭据已保存，正在等待网络恢复后自动继续同步（账号 ${st.value.username || '—'}）${tail}`
    }
    if (accounting.value) return '未登录：本机改动只在本地记账，联网登录后才会上传'
    return '未登录：处于离线单机状态，数据只存本机'
  })

  // 指示灯：全连接 / 半连接（恢复中）/ 警示（需重登）/ 未连接
  const stateLevel = computed(() => {
    // 未取到过 ⇒ 用中性色（灰），不用"未连接"的警示语义
    if (!everLoaded.value) return ''
    if (online.value) return 'on'
    if (reloginRequired.value) return 'alert'
    if (recoverable.value) return 'half'
    if (accounting.value) return 'half'
    return 'off'
  })

  /**
   * 手动刷新 = 请求仓库立刻重取一轮（带退避重试）。
   *
   * ⭐ v21.220：**本组件不再自行拉取状态**，因此不存在"拉失败后没人再拉"的问题。
   * 仓库有 20s 无条件心跳兜底，本函数只是让用户点按钮时能马上看到结果。
   */
  async function refresh () {
    await ensureFresh()
    if (!form.value.baseURL && st.value.server_url) form.value.baseURL = st.value.server_url
  }

  async function onLogin () {
    formError.value = validateLoginInput(form.value.baseURL, form.value.username, form.value.password) || ''
    if (formError.value) return
    busy.value = 'login'
    try {
      // v21.200：加超时上限——Go 侧登录链含Bootstrap 换代（最长 pollTimeout 90s），
      // 此前 await 期间按钮永久 loading（用户实机「登录按钮一直转圈圈」）。
      const res = await withTimeout(
        remoteLogin(form.value.baseURL, form.value.username, form.value.password),
        DEFAULT_TIMEOUT_MS,
        '登录'
      )
      form.value.password = '' // 立刻从内存擦掉
      // v21.151（生产 Bug2）：2FA 账号——服务端要求动态码，进入第二步。
      // 旧实现把该响应静默当成功（空票），下游 401 被误报「登录会话已失效（被吊销）」。
      if (res && res.totp_required) {
        totp.value = { active: true, token: String(res.totp_token || ''), code: '' }
        formError.value = ''
        message.info('该账号已开启两步验证，请输入验证器 App 的 6 位动态码（或一枚恢复码）')
        return
      }
      await finishLoginSuccess(res)
    } catch (e) {
      form.value.password = ''
      formError.value = formatAuthError(e)
    } finally {
      busy.value = ''
    }
  }

  // onVerifyTotp 两步验证第二步：临时票 + 动态码（或恢复码）→ 换正式会话（v21.151）。
  async function onVerifyTotp () {
    const code = String(totp.value.code || '').trim()
    if (!code) {
      formError.value = '请输入验证码（或恢复码）'
      return
    }
    busy.value = 'totp'
    try {
      const res = await remoteLoginTOTP(form.value.baseURL, form.value.username, totp.value.token, code)
      totp.value = { active: false, token: '', code: '' }
      await finishLoginSuccess(res)
    } catch (e) {
      formError.value = formatAuthError(e)
      // 临时票超时（2 分钟）：退回密码步骤重新登录；验证码错/锁定等留在本步可重试
      if (String(formError.value).includes('验证已过期')) {
        totp.value = { active: false, token: '', code: '' }
      }
    } finally {
      busy.value = ''
    }
  }

  function cancelTotp () {
    totp.value = { active: false, token: '', code: '' }
    formError.value = ''
  }

  // finishLoginSuccess 登录成功（含二因子通过）的公共收口：提示 + auth store 同步 + 整页重载。
  async function finishLoginSuccess (res) {
    message.success(`已登录：${(res && res.server_url) || form.value.baseURL}`)
    // Go 侧已"登录即等首轮同步"（有界）。未完成时如实提示，不让用户误以为数据丢了一部分。
    if (res && res.first_sync_done === false && res.first_sync_note) {
      message.info(String(res.first_sync_note))
    }
    // v21.143 **修正 182 的过度收缩**：182 把这里的整页重载换成了就地 `refresh()`，
    // 结果只刷新了本卡片，**笔记列表/文件夹视图不会重新挂载取数** → 真机表现"登录后数据全没了"
    // （实测本机库 4 篇笔记全部存活、0 墓碑 —— 是"没刷新"而不是"丢了"）。
    // 恢复整页重载：它让所有视图以干净状态重新挂载并取数。
    // 之所以现在可以安全重载：v21.142 已修好同步门死锁（空票 + 会话可恢复 → 放行一轮惰性换票），
    // 重载丢掉的内存 access 会在一次调度轮内自动换回，不会再"永久离线"。
    // v21.147：同步前端 auth store 的用户名（否则侧边栏仍显示旧值 "local"）
    try { await auth.login(form.value.username, '', false) } catch { /* LocalLogin 桩恒成功 */ }
    message.info('已连接，正在同步数据…')
    // ⭐ v21.216（211e 实机·首次登录白屏）：退役此处 500ms 整页重载，与 LoginView 同罪同修。
    // 定罪见 LoginView.afterDesktopLogin 的完整注释：整页重载撞进换代窗口 ⇒ DOM 空壳纯白。
    // 数据拾取由 installSyncRefreshPolling（gen_epoch，v21.212 持久化）承担；
    // 「重载丢内存缓存」的场景由 gen_epoch 回调覆盖。
    // 本卡片自身维持 refresh() 就地刷新（上方已调用），无需整页。
    router.push('/').catch(() => {}) // 已在 / 时 Vue Router 抛冗余导航，吞掉即可
  }

  async function onForceResync () {
    busy.value = 'resync'
    try {
      // v21.200：加超时上限（同 onLogin）。
      await withTimeout(forceFullResync(), DEFAULT_TIMEOUT_MS, '强制全量同步')
      message.success('已重置同步游标，后台将重新拉取全部数据（约 10~30 秒）')
      // ⭐ v21.213（211b 实机：强制全量同步后整页全白）：
      //
      // 原实现是 `setTimeout(hardReloadHome, 3000)` —— **定时盲重载**。定罪：
      // 重载是「3 秒后无条件刷新」，而换代（快照下载 + 双代激活 + SetDB 切句柄）
      // 耗时**不确定**（弱网 30s+，实机日志换代本身就要 6s）。于是：
      //   换代还在跑 ⇒ `hardReloadHome` 把页面重载到「**半接管**」状态 ——
      //   前端重新挂载时读 `database.DB`，而它可能正指向刚 `SetDB` 但
      //   尚未 `setWorker` / 尚未 SealCommitted 的中间态 ⇒ 列表全空 + 登录态读不到
      //   ⇒ 用户看到「跳到登录页 → 登录后整页空白」。
      //   若换代恰好在 3s 后仍在跑，重载还会**打断**它（前端重载不影响 Go，
      //   但用户看到的是空白且无法自愈）。
      //
      // 修法：**重载时机交给已有的换代感知机制**，不做定时猜测。
      // `installSyncRefreshPolling` 已经在盯 `gen_epoch`（v21.165起）——
      // 换代成功时 Go 侧 `genEpoch.Add(1)` 且（v21.212）已跨进程持久化，
      // 轮询必然感知到并触发刷新。只需把它的回调指向整页重载即可，
      // **既精确（换代真完成才刷）又不重复（沿用同一个 5s 轮询）**。
      // 这里仅作为兜底：若 90s 内换代仍未发生（例如被熔断/冷却跳过），
      // 给一次明确提示而不是静默停在旧页面。
      setTimeout(() => {
        message.info('若数据长时间未更新，请查看上方同步状态；必要时再次点击【强制全量同步】')
      }, 90000)
    } catch (e) {
      // v21.152：失败后立即回读状态——凭据失效（NO_REFRESH 等）要让「需要重新登录」在卡片上亮出来
      message.error(formatAuthError(e) || '强制同步失败')
      refresh()
    } finally {
      busy.value = ''
    }
  }

  // ⭐ v21.221：立即重试（打破长退避）。
  //
  // 214 实机：调度器进入终态后按 5/15/30min 阶梯退避（v21.221 修），
  // 但用户面对「恢复中」只能干等 ⇒ 提供一个显式出口。
  // `SyncNow` 会唤醒调度器立刻跑一轮（走同一 ensureAccess→RunOnce 链路），
  // 并顺带换票，因此弱网恢复 / 凭据轮转后**点一下就好**。
  async function onRetryNow () {
    busy.value = 'retry'
    try {
      await withTimeout(syncNow(), DEFAULT_TIMEOUT_MS, '立即同步')
      message.success('已触发一次同步，请稍候查看上方同步状态')
    } catch (e) {
      // 失败也要说清是什么失败（凭据作废 / 连不上 / 协议错误），别只留一个 toast。
      message.error(formatAuthError(e) || '同步失败')
    } finally {
      busy.value = ''
      refresh()
    }
  }

  async function onLogout () {
    try {
      await ElMessageBox.confirm(
        '退出后本机会清除已封存的登录凭据并停止自动同步；本机已记录的改动保留，'
        + '下次登录后继续上传。需要输入密码才能重新登录。',
        '退出登录',
        { confirmButtonText: '确认退出', cancelButtonText: '取消', type: 'warning' }
      )
    } catch { return }
    busy.value = 'logout'
    try {
      await logoutDesktop() // 先真登出（清 DPAPI 封存票 + 卸工），失败就不清本地态，避免"以为登出了其实没有"
      await auth.logout()
      // v21.146：设置登出标志，确保路由守卫跳到登录页而不尝试恢复会话
      localStorage.setItem('sn_logged_out', '1')
      message.success('已退出登录')
      setTimeout(() => { window.location.href = withBase('/login') }, 500)
    } catch (e) {
      message.error(formatAuthError(e))
    } finally {
      busy.value = ''
    }
  }

  async function onBootstrap () {
    try {
      await ElMessageBox.confirm(
        '重建同步会从服务器完整拉取一次数据快照并接管本机同步游标；本机未推送的改动会被保留并重新排队。',
        '重建同步',
        { confirmButtonText: '开始重建', cancelButtonText: '取消', type: 'warning' }
      )
    } catch { return }
    busy.value = 'bootstrap'
    try {
      const res = await bootstrapNow()
      if (res && res.error) {
        message.error(String(res.error))
        refresh() // v21.152：失败（含凭据失效）后立即回读状态
      } else {
        message.success('重建同步完成，正在刷新')
        setTimeout(hardReloadHome, 600)
      }
    } catch (e) {
      // v21.152：同 onForceResync —— 重建同步失败（含凭据失效）后立即回读状态
      message.error(formatAuthError(e))
      refresh()
    } finally {
      busy.value = ''
    }
  }

  // v21.196 O2：一键导出诊断包。产物落<档案>\diagnostics\，**不含笔记正文**。
  async function onExportDiagnostics () {
    busy.value = 'diag'
    try {
      // v21.200：加超时上限——排障动作**绝不能**让界面卡死，否则用户既拿不到包
      // 也看不到任何提示（用户实机「导出诊断包按钮一直转圈圈」）。
      diagPath.value = await withTimeout(exportDiagnostics(), DEFAULT_TIMEOUT_MS, '导出诊断包')
      message.success('诊断包已导出，可把该文件发给维护者')
    } catch (e) {
      message.error(String((e && e.message) || e))
    } finally {
      busy.value = ''
    }
  }

  // v21.153（R-1 通知接线）：后台同步每完成一轮 → 回读卡片状态。此前状态只在挂载/
  // 手动刷新/操作失败回读时更新——首登自动 Bootstrap 换库完成后卡片停留旧态直到重启
  //（192 真机实证「同进程内卡片偶发未登录」的可见性缺口）。
  //
  // ⭐ v21.220：原 `stopSyncRefresh = installSyncRefreshPolling(refresh)` 已**整体移除**。
  // 它只在 gen_epoch/rounds 变化时回调，而换代常早于监听建立 ⇒ 事件永久丢失
  // （213 实机：卡片永久停在 st={}，显示"未登录"而 Go 侧 48 笔记/凭据完好）。
  // 该职责现由桌面状态仓库的 **20s 无条件心跳**承担（desktopStatus.js），
  // 组件不再持有任何轮询句柄。

  // v21.158 · X5：换代窗口可见性。Bootstrap 换代（约 10~30s）与 Portable 导入双代激活
  // 期间调度闸门关闭，用户此时点「立即同步」会排队/延后——此前 UI 完全无感知，
  // 表现为「同步坏了」。这里 2s 轮询只读闸门状态，窗口期显示常驻提示，结束自动消失。
  // 纯可见性：不改任何同步语义，桥不可用（非桌面）时静默降级。
  //
  // ⭐ v21.220：状态改读仓库的 paused，**删除组件本地 paused ref**（避免两份真相源）。
  let stopPausedPoll = null

  async function pollPaused () {
    await refreshPaused()
  }

  onMounted(() => {
    // ⭐ v21.220：只做两件事 —— 请求一次刷新、启动换代暂停轮询。
    // **不再 installSyncRefreshPolling**：状态由仓库的 20s 无条件心跳维护，
    // 组件自己再插一套「只在 gen_epoch/rounds 变化时」的事件驱动监听，
    // 只会重演 213 事故（事件早于监听建立 → 永久丢失 → 卡死空态）。
    refresh()
    if (IS_DESKTOP) {
      pollPaused()
      stopPausedPoll = setInterval(pollPaused, 2000)
    }
  })

  onUnmounted(() => {
    if (stopPausedPoll) { clearInterval(stopPausedPoll); stopPausedPoll = null }
    // ⚠ 仓库是模块级单例，**故意不在此停** —— 它的生命周期 = 应用生命周期，
    // 不随这个卡片 unmount 而停（ProfileView 切走再切回来时状态应保持新鲜）。
  })

  return {
    busy, diagPath, st, sched, paused, loading, everLoaded, loadError,
    form, formError, totp,
    loggedIn, accounting, needAttention, heldReason,
    autoBootTripped, autoBootCooldownSec, lastNetFailure, serverUrlRecovered,
    urlCheck, urlCheckBlocked,
    online, recoverable, reloginRequired, retryInSec, retryInMin,
    stateLine, stateLevel,
    refresh, onLogin, onVerifyTotp, cancelTotp,
    onForceResync, onRetryNow, onLogout, onBootstrap, onExportDiagnostics
  }
}
