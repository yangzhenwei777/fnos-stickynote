<script setup>
import { withBase } from '@/utils/appBase'
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
// v21.200：长耗时绑定调用统一超时上限（杜绝按钮永久 loading）
import { withTimeout, DEFAULT_TIMEOUT_MS } from '@/utils/withTimeout'

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
</script>

<template>
  <div class="acct">
    <p class="acct-desc">
      桌面端可以先离线记账，登录后再把改动同步到服务器。<strong>退出登录会清除本机保存的登录凭据</strong>，
      已记录的改动保留。
    </p>

    <el-alert
      v-if="loadError"
      class="acct-alert"
      type="error"
      :closable="false"
      show-icon
      title="账号状态读取失败"
      :description="loadError"
    />

    <el-alert
      v-if="paused"
      class="acct-alert"
      type="info"
      :closable="false"
      show-icon
      title="正在初始化全量同步"
      description="换代期间同步请求会排队（通常 10~30 秒），完成后自动恢复，无需操作。"
    />

    <!-- v21.202 P0-2 附带：档案在但本机 DPAPI 打不开（换机/复制目录）时，
         后端已从明文列回填 server_url。这里告诉用户"地址没变、只是本机解不开封存"。 -->
    <el-alert
      v-if="serverUrlRecovered && !loggedIn"
      class="acct-alert"
      type="warning"
      :closable="false"
      show-icon
      title="本机登录凭据无法解封"
      description="档案里的服务器地址已自动填回（地址本身没变）。但本机无法解密保存的登录凭据（常见于换机或复制了程序目录），请重新输入账号密码登录一次。"
    />

    <!-- v21.202 P0-3：最近一次网络/代理失败。后端把 TLS 握手超时 / DNS / 拒连
         翻成可执行中文；此前这类信息只在日志里，界面零提示（2026-10-10 实机
         表现为"登录后白屏 + 不知原因"）。 -->
    <el-alert
      v-if="lastNetFailure"
      class="acct-alert"
      type="error"
      :closable="false"
      show-icon
      title="无法连接服务器"
      :description="lastNetFailure + '（若使用代理软件，请把该服务器域名设为直连后重试）'"
    />

    <!-- v21.201 A1：自动重建熔断可见化。此前"反复重建"对用户完全不可见，
         表现为「要登录很多次 + 期间必须多次关窗口」，用户无从判断该做什么。 -->
    <el-alert
      v-if="autoBootTripped"
      class="acct-alert"
      type="warning"
      :closable="false"
      show-icon
      title="已停止自动重建同步"
      description="检测到同步反复重建（本地与服务端数据不一致且自动修复未生效），已停止自动重建以保护你的数据。请点下方【强制全量同步】手动恢复，或重启程序。你的笔记没有被删除。"
    />
    <el-alert
      v-else-if="autoBootCooldownSec > 0"
      class="acct-alert"
      type="info"
      :closable="false"
      show-icon
      title="自动同步处于冷却期"
      :description="`刚完成过一次全量同步，约 ${autoBootCooldownSec} 秒内不再重复重建。期间你的修改会正常排队，不会丢失。`"
    />

    <div class="acct-state">
      <span
        class="acct-dot"
        :class="stateLevel"
      />
      <div class="acct-state-text">
        <strong>{{ stateLine }}</strong>
        <small v-if="st.last_login_at">上次登录 {{ st.last_login_at }}</small>
        <small v-else-if="st.server_url">服务器 {{ st.server_url }}</small>
        <small v-if="st.revalidate_due === true">距上次联网重验已超期，建议尽快登录确认</small>
        <!-- ⭐ v21.218：恢复态明确告知「无需重新登录」，避免用户反复重登 -->
        <small v-if="recoverable && !online">本机凭据完好，联网后会自动继续同步，无需重新登录</small>
        <small v-if="st.relogin_required === true">本机凭据已失效，需要重新登录</small>
      </div>
      <!-- ⭐ v21.222：按钮组独占一行（此前与状态文字同一行，长 URL 会把它们挤出容器） -->
      <div class="acct-actions">
        <el-button
          size="small"
          :loading="loading"
          @click="refresh"
        >
          刷新
        </el-button>
        <!-- ⭐ v21.221：立即重试 —— 打破 5/15/30min 长退避，给用户一个显式出口。
           214 实机：调度器终态后退避，用户面对「恢复中」只能干等，无法使用。 -->
        <el-button
          v-if="reloginRequired || recoverable || needAttention"
          size="small"
          type="primary"
          plain
          :loading="busy === 'retry'"
          @click="onRetryNow"
        >
          立即重试
        </el-button>
        <el-button
          size="small"
          type="danger"
          plain
          :loading="busy === 'resync'"
          @click="onForceResync"
        >
          强制全量同步
        </el-button>
      </div>
    </div>

    <!-- ⭐ v21.221：第四态的明确引导 —— 凭据作废时明确告知「必须重新登录」，
         并说明稍后会自动重试（不是彻底停手），避免用户反复重登或无限干等。 -->
    <el-alert
      v-if="reloginRequired"
      class="acct-alert"
      type="error"
      :closable="false"
      show-icon
      title="本机登录凭据已被服务器作废"
      :description="`请用下方登录框重新登录一次${retryInSec > 0 ? `（若网络恢复，系统也会在约 ${retryInMin} 分钟后自动重试）` : ''}。本机数据不会丢失。`"
    />
    <el-alert
      v-else-if="needAttention && !online"
      class="acct-alert"
      type="warning"
      :closable="false"
      show-icon
      title="自动同步遇到问题，已进入退避重试"
      :description="`系统会每隔一段时间自动重试${retryInSec > 0 ? `（下次约 ${retryInMin} 分钟后）` : ''}，期间你的修改会正常保存在本机，不会丢失。也可点击上方【立即重试】马上再试一次。`"
    />

    <el-alert
      v-if="needAttention"
      class="acct-alert"
      type="warning"
      :closable="false"
      show-icon
      title="自动同步已暂停，需要你处理"
      :description="heldReason ? `原因：${heldReason}` : '服务器数据已换代或本机游标落后，重建同步可恢复'"
    >
      <el-button
        size="small"
        type="primary"
        style="margin-top: 8px"
        :loading="busy === 'bootstrap'"
        @click="onBootstrap"
      >
        重建同步
      </el-button>
      <el-button
        size="small"
        type="danger"
        plain
        style="margin-top: 8px"
        :loading="busy === 'resync'"
        @click="onForceResync"
      >
        强制全量同步
      </el-button>
    </el-alert>

    <section class="acct-block">
      <h4>排障协助</h4>
      <p class="acct-hint">
        导出诊断包（应用日志、内置运行组件来源、WebView2 实际版本、本机档案代数与实体计数）。
        <strong>不含笔记正文</strong>，可安全发给维护者。
      </p>
      <el-button
        size="small"
        plain
        style="margin-top: 8px"
        :loading="busy === 'diag'"
        @click="onExportDiagnostics"
      >
        导出诊断包
      </el-button>
      <p
        v-if="diagPath"
        class="acct-hint"
        style="margin-top: 8px; word-break: break-all;"
      >
        已导出：{{ diagPath }}
      </p>
    </section>

    <section class="acct-block">
      <!-- ⭐ v21.218：恢复态（凭据在、暂未联网）下标题改为「切换账号」，
           避免"恢复中"却提示"登录服务器"造成"我明明登过"的困惑。 -->
      <h4>{{ loggedIn ? '切换到其他服务器 / 账号' : (recoverable ? '切换到其他服务器 / 账号' : '登录服务器') }}</h4>
      <label class="acct-field">
        <span>服务器地址</span>
        <el-input
          v-model="form.baseURL"
          :disabled="totp.active"
          placeholder="https://note.example.com（推荐域名；内网 IP 需同网络且未被代理接管）"
        />
        <!-- v21.202 P1：地址误配即时提示（error 同步禁用登录按钮） -->
        <p
          v-if="urlCheck.hint"
          class="acct-url-hint"
          :class="urlCheck.level"
        >{{ urlCheck.hint }}</p>
      </label>
      <label class="acct-field">
        <span>用户名</span>
        <el-input
          v-model="form.username"
          :disabled="totp.active"
          autocomplete="off"
          placeholder="账号"
        />
      </label>
      <label
        v-if="!totp.active"
        class="acct-field"
      >
        <span>密码</span>
        <el-input
          v-model="form.password"
          type="password"
          show-password
          autocomplete="new-password"
          placeholder="登录密码"
          @keyup.enter="onLogin"
        />
      </label>
      <label
        v-if="totp.active"
        class="acct-field"
      >
        <span>两步验证码</span>
        <el-input
          v-model="totp.code"
          placeholder="6 位动态码，或一枚恢复码"
          autocomplete="one-time-code"
          @keyup.enter="onVerifyTotp"
        />
      </label>
      <p
        v-if="formError"
        class="acct-err"
      >
        {{ formError }}
      </p>
      <div class="acct-actions">
        <el-button
          v-if="!totp.active"
          type="primary"
          :loading="busy === 'login'"
          :disabled="urlCheckBlocked"
          @click="onLogin"
        >
          登录
        </el-button>
        <template v-else>
          <el-button
            type="primary"
            :loading="busy === 'totp'"
            @click="onVerifyTotp"
          >
            验证并登录
          </el-button>
          <el-button @click="cancelTotp">
            返回重新登录
          </el-button>
        </template>
        <el-button
          v-if="loggedIn || accounting"
          :loading="busy === 'logout'"
          @click="onLogout"
        >
          退出登录
        </el-button>
      </div>
      <p class="acct-tip">
        密码只在提交瞬间存在于内存，不写入本机存储；登录凭据以系统级加密（DPAPI）封存，仅本机本用户可解。
      </p>
    </section>
  </div>
</template>

<style scoped>
/* ⭐ v21.222：卡片容器加宽 + 顶部状态区改「文字/按钮纵向可换行」布局。
 *
 * 215 实机缺陷：`.acct-state` 是横向 flex，长文案（服务器 URL + 账号 + 多行 small）
 * 把三个按钮（刷新/立即重试/强制全量同步）**挤出容器**，
 * 表现为「部分按钮超出内容、状态文字被挤成一条窄柱」。
 *
 * 修法要点：
 *   ① `.acct` 去掉 620px 硬上限（设置页本就该给足宽度）；
 *   ② `.acct-state` 加 `flex-wrap: wrap` 且文字块 `min-width: 0`
 *      —— `min-width:0` 是 flex 子项能正确收缩/换行的**必要条件**，
 *      缺了它长 URL 会把整行撑爆（这正是 215 的现象）；
 *   ③ 按钮组独占一行（`width:100%` + `flex-wrap`），不再与文字抢同一行。
 */
.acct {
  width: 100%;
  max-width: 860px;
}
.acct-desc {
  font-size: 13px;
  color: var(--text-muted);
  line-height: 1.7;
  margin: 8px 0 16px;
}
.acct-url-hint {
  margin: 4px 0 0;
  font-size: 12px;
  line-height: 1.5;
}
.acct-url-hint.error { color: var(--el-color-danger); }
.acct-url-hint.warn { color: var(--el-color-warning); }

.acct-alert {
  margin-bottom: 12px;
}
.acct-state {
  display: flex;
  flex-wrap: wrap; /* ⭐ v21.222：窄屏时按钮自动换到下一行，不再被挤出容器 */
  align-items: flex-start;
  gap: 10px 12px;
  padding: 12px;
  border-radius: var(--radius-md);
  background: var(--bg-tertiary, rgba(128, 128, 128, .08));
  margin-bottom: 16px;
}
.acct-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  margin-top: 5px;
  flex: 0 0 auto;
  background: var(--el-color-info, #909399);
}
.acct-dot.on {
  background: var(--el-color-success, #67c23a);
}
.acct-dot.half {
  background: var(--el-color-warning, #e6a23c);
}
/* ⭐ v21.221：需重新登录（凭据作废）——用 danger 色，与「恢复中」的 warning 区分 */
.acct-dot.alert {
  background: var(--el-color-danger, #f56c6c);
}
.acct-state-text {
  flex: 1 1 260px; /* ⭐ v21.222：给一个合理的基准宽度，配合 min-width:0 才能正确换行 */
  min-width: 0; /* flex 子项收缩的前提：缺它长 URL 会撑爆整行（215 实机现象） */
  display: flex;
  flex-direction: column;
  gap: 2px;
  /* 长 URL / 长状态文案强制断行，不允许横向溢出 */
  overflow-wrap: anywhere;
  word-break: break-word;
}
.acct-state-text strong {
  font-size: 14px;
  overflow-wrap: anywhere;
  word-break: break-word;
}
/* ⭐ v21.222：按钮组独占一行（宽度 100% + 自动换行），不再与状态文字抢同一行 */
.acct-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  width: 100%;
  padding-top: 2px;
}
.acct-state-text small {
  font-size: 12px;
  color: var(--text-muted);
}
.acct-block h4 {
  margin: 0 0 12px;
  font-size: 15px;
}
.acct-field {
  display: block;
  margin-bottom: 12px;
}
.acct-field span {
  display: block;
  font-size: 13px;
  color: var(--text-muted);
  margin-bottom: 4px;
}
.acct-err {
  font-size: 13px;
  color: var(--el-color-danger, #f56c6c);
  margin: 0 0 10px;
}
.acct-actions {
  display: flex;
  gap: 12px;
}
.acct-tip {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.7;
  margin-top: 12px;
}
</style>
