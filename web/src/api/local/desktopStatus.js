// desktopStatus.js —— ⭐ v21.220 桌面状态仓库（**治本**：状态所有权唯一化）
//
// ════════════════════════════════════════════════════════════════════════
// 为什么需要它（213 实机复测定罪）
// ════════════════════════════════════════════════════════════════════════
// 213 实机：Go 侧一切正常（换代成功、48 笔记/9 文件夹/290 changes 落地、
// gen_epoch=1 已持久化、refresh_sealed 完好、`localStorage.user` 已正确写入
// `{"id":1,"username":"testuser"}`），**但账号卡片恒显示「未登录：处于离线单机状态」**。
//
// 定罪（不是老问题复发，是上一轮修复自身的缺陷）：
//   · 卡片 `onMounted → refresh()` **只在组件挂载时拉一次**；
//   · 首屏恰好撞上 WebView2 初始化 + `restoreDeviceSession` + 首轮调度窗口
//     ⇒ `authStatus()` 失败 ⇒ `st = {}` ⇒ 四条文案分支全false ⇒ 显示"未登录"；
//   · 兜底 `installSyncRefreshPolling` **只在 gen_epoch 变大 / rounds 增长时**回调，
//     而换代在挂载完成**之前**就完成了（gen_epoch 已=1）⇒ 首拍即追平 ⇒ 此后永不触发；
//   · `catch` 里的 `failStreak>=2` 救不了：轮询本身成功（SyncScheduleStatus 正常返回），
//     根本没进 catch。
//   ⇒ **此后没有任何机制会再拉一次** ⇒ 永久停在空态，直到用户手动点「刷新」。
//
// ════════════════════════════════════════════════════════════════════════
// 解法：把「状态的取用权」从组件手里收归仓库（**单一数据源 + 启动即维护**）
// ════════════════════════════════════════════════════════════════════════
// 三条不变式（后续任何改动都不得破坏）：
//
//   ① **组件只读不算** —— UI 一律 `useDesktopStatus()` 拿响应式值，
//      不再自己 `refresh()`。这样"谁负责重试"这个问题**根本不存在**。
//
//   ② **仓库在应用入口即启动，且永不放弃** —— 桥未就绪不算失败，
//      只是「还没到能问的时候」，继续等（重试间隔递增，上限 4s，不设总次数上限）。
//
//   ③ **心跳无条件** —— 每 HEARTBEAT_MS(20s) 无条件重取一次，
//      **不依赖任何"变化了才通知"的机制**。
//      ⚠ 这条是 213 教训的直接对策：v21.212 的 `gen_epoch` 持久化、
//      v21.213 的"换代完成才刷"这类**事件驱动**方案，
//      在「事件发生在监听器建立之前」时会永久丢失。
//      事件驱动只能做**加速**，不能做**唯一路径**。
//
// 另有 `ensureFresh()`：换代/重登等显式动作后主动调用一次，不必等心跳。
import { ref, readonly } from 'vue'
import { IS_DESKTOP } from './transport'
import { authStatus, syncScheduleStatus, syncPaused } from './desktopApi'

/** 心跳间隔：兜底重取。20s —— 兼顾"卡死不可接受"与"不打扰用户/不打爆 Go 绑定"。 */
const HEARTBEAT_MS = 20000
/** 退避重试序列（ms）：用于「立即取一次」的失败重试。桥未就绪也在其中。 */
const RETRY_DELAYS = [0, 300, 800, 1500, 3000, 4000]

// ── 响应式状态（模块级单例，跨组件共享同一份）──────────────────────────
const auth = ref({})
const sched = ref({})
const paused = ref(false)
/** 最近一次取数失败的原因（空串=当前健康）。UI 可据此显示"正在重试…"而非"未登录"。 */
const lastError = ref('')
/** 是否已经成功取到过一次（false = 还没取到过，UI 不应断言"未登录"）。 */
const everLoaded = ref(false)
/** ⭐ Go 侧降级中（换代窗口 DB 句柄切换中，返回的是"暂不可信"状态）。
 *  与 everLoaded 区分：首次取数就撞降级 ⇒ everLoaded=false ⇒ UI 显示"正在读取"；
 *  取过一次后偶发降级 ⇒ everLoaded=true ⇒ UI 保留上次已知状态 + 提示正在恢复。 */
const degraded = ref(false)
/** 正在进行一轮取数（UI 可显示细微的"刷新中"，避免误以为卡死）。 */
const loading = ref(false)

let started = false
let timer = null
let retryTimer = null
let inFlight = false

/** 把桌面错误压成可展示的一句话（Go 侧原样透传，禁二次翻译丢信息）。 */
function errText (e) {
  const m = String((e && e.message) || e || '').trim()
  return m || '状态读取失败'
}

/**
 * 拉取一轮完整状态。
 *
 * ⚠ 幂等 + 并发保护：`inFlight` 保证同一时刻只有一轮在飞。
 *   213 日志里出现过「换代窗口内并发调用撞 database is closed」，
 *   心跳 + 显式 ensureFresh 可能撞车，必须挡住。
 */
async function fetchOnce () {
  if (inFlight) return
  inFlight = true
  loading.value = true
  try {
    // 两个绑定并发拉：任一失败即整体失败（保持 auth/sched 同一时刻点的自洽）
    const [a, s] = await Promise.all([
      authStatus(),
      syncScheduleStatus()
    ])
    auth.value = a && typeof a === 'object' ? a : {}
    sched.value = s && typeof s === 'object' ? s : {}
    // ⚠ Go 侧「降级返回」（换代窗口 DB 句柄切换中）时 status_degraded=true。
    //   此时拿到的 logged_in 不可信，**必须继续重试**，不能当成"确认未登录"。
    degraded.value = auth.value.status_degraded === true
    if (degraded.value) {
      lastError.value = String(auth.value.status_error || '状态读取降级（应用正在初始化）')
    } else {
      everLoaded.value = true
      lastError.value = ''
    }
  } catch (e) {
    // ⚠ 不清空既有值：一次失败不该把已知状态打回未知（那正是「永久空态」的成因）。
    //   仅记录错误，UI 靠 lastError/everLoaded 区分「降级展示」与「真未登录」。
    lastError.value = errText(e)
    degraded.value = false
  } finally {
    inFlight = false
    loading.value = false
  }
}

/** 带退避的重试：用于 `ensureFresh()` 显式触发后的立即取数。 */
async function fetchWithRetry () {
  for (const d of RETRY_DELAYS) {
    if (d > 0) await new Promise(r => setTimeout(r, d))
    await fetchOnce()
    if (!lastError.value) return true
  }
  return false
}

/**
 * 显式要求立刻刷新（换代完成 / 用户手动点刷新 / 重登后）。
 *
 * ⚠ 去重（v21.220 M7 钉）：并发调用**共享同一轮**。
 *   换代窗口内并发打 AuthStatus 正是 213 首屏失败的诱因之一（DB 句柄切换中 + 多路叠加）。
 *   注意：必须在**第一个 await 之前**同步占位，否则三个并发调用会各自跑完
 *   `fetchWithRetry` 的退避序列（每次都先 await 再 fetch ⇒ inFlight 有空隙 ⇒ 重复打）。
 */
let ensurePromise = null
export function ensureFresh () {
  if (!IS_DESKTOP) return Promise.resolve(false)
  if (ensurePromise) return ensurePromise
  if (retryTimer) { clearTimeout(retryTimer); retryTimer = null }
  // 同步建 promise（fetchWithRetry 内部首个 await 只是退避延时 0ms，
  // 但 ensurePromise 的赋值必须发生在任何 await 之前）
  ensurePromise = (async () => fetchWithRetry())().finally(() => { ensurePromise = null })
  return ensurePromise
}

/**
 * 启动仓库：立即取一次 + 启动无条件心跳。
 * 幂等（多次调用只启动一次）。
 */
export function startDesktopStatus () {
  if (!IS_DESKTOP || started) return
  started = true

  // ① 立即取一轮（失败不阻塞应用启动，仓库是后台维护的）
  fetchOnce()

  // ② 无条件心跳：这是「不会永久卡在错误状态」的**唯一保障**。
  //    不监听 gen_epoch/rounds 变化 —— 那是加速手段，不是可用性保障。
  timer = setInterval(fetchOnce, HEARTBEAT_MS)

  // ③ 页面从后台回到前台时立刻取一次（WebView2 挂起/恢复会丢状态）
  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') ensureFresh()
    })
  }
}

/** 停止（仅测试/卸载用）。 */
export function stopDesktopStatus () {
  if (timer) { clearInterval(timer); timer = null }
  if (retryTimer) { clearTimeout(retryTimer); retryTimer = null }
  started = false
  ensurePromise = null
}

/** 仅测试用：复位模块级状态。 */
export function __resetDesktopStatusForTest () {
  stopDesktopStatus()
  auth.value = {}
  sched.value = {}
  paused.value = false
  lastError.value = ''
  everLoaded.value = false
  degraded.value = false
  loading.value = false
  inFlight = false
}

// ── 对外只读接口 ──────────────────────────────────────────────────────
export function useDesktopStatus () {
  return {
    auth: readonly(auth),
    sched: readonly(sched),
    paused: readonly(paused),
    lastError: readonly(lastError),
    everLoaded: readonly(everLoaded),
    degraded: readonly(degraded),
    loading: readonly(loading),
    ensureFresh,
    start: startDesktopStatus
  }
}

/** 同步暂停态的独立拉取（换代窗口横幅用，低频即可）。 */
export async function refreshPaused () {
  if (!IS_DESKTOP) return
  try { paused.value = await syncPaused() } catch { /* 横幅可选，失败保持旧值 */ }
}