import axios from 'axios'
import { message } from '@/utils/message'
import { apiBase, withBase } from '@/utils/appBase'
import { IS_DESKTOP, installLocalTransport, clearDesktopCredentials } from './local/transport'

// 双通道认证：
// - httpOnly cookie 为主通道（浏览器环境，XSS 无法读取）
// - Authorization 头为兜底通道（cookie 不可用的环境，如部分内嵌 webview），
//   后端中间件本就"头优先、cookie 兜底"，双通道零冲突。
// token 存储遵循"记住我"语义：勾选→localStorage（最长 30 天）；
// 未勾选→sessionStorage（关闭标签页即清），暴露面最小化。
const ACCESS_KEY = 'access_token'
const REFRESH_KEY = 'refresh_token'
// 与 stores/auth.js 的 LOGGED_OUT_KEY 同键（此处不便引 store 防循环依赖）：
// 显式登出后，401 拦截器不得再用残留 refresh cookie 尝试复活会话。
const LOGGED_OUT_KEY = 'logged_out'

export function setTokens(access, refresh, remember) {
  clearTokens()
  const store = remember ? localStorage : sessionStorage
  if (access) store.setItem(ACCESS_KEY, access)
  if (refresh) store.setItem(REFRESH_KEY, refresh)
}

// 仅换新 access（refresh 不动）：remember 状态由 refresh 所在存储推断
export function updateAccessToken(access) {
  if (!access) return
  const store = localStorage.getItem(REFRESH_KEY) ? localStorage : sessionStorage
  store.setItem(ACCESS_KEY, access)
}

// v21.90 N-06：refresh token 轮换后同步更新本地存储（remember 语义由 refresh 所在存储推断）
export function updateRefreshToken(refresh) {
  if (!refresh) return
  const store = localStorage.getItem(REFRESH_KEY) ? localStorage : sessionStorage
  store.setItem(REFRESH_KEY, refresh)
}

export function getAccessToken() {
  return localStorage.getItem(ACCESS_KEY) || sessionStorage.getItem(ACCESS_KEY)
}

export function getRefreshToken() {
  return localStorage.getItem(REFRESH_KEY) || sessionStorage.getItem(REFRESH_KEY)
}

export function clearTokens() {
  localStorage.removeItem(ACCESS_KEY)
  localStorage.removeItem(REFRESH_KEY)
  sessionStorage.removeItem(ACCESS_KEY)
  sessionStorage.removeItem(REFRESH_KEY)
}

const api = axios.create({
  // v2.1.2：API 基址跟随挂载点。根路径 → '/api'（与原口径一致）；
  // 子路径（fnOS 网关 /app/<appname>/）→ '/app/<appname>/api'，
  // 否则请求会落到宿主根、被飞牛主站接管。桌面壳恒为 '/api'，行为不变。
  baseURL: apiBase()
})

// 桌面构建（VITE_DESKTOP=1）：/api 全部改走 Wails Binding（LocalTransport）。
// Web 构建下 IS_DESKTOP 恒为 false，此分支构建期即消除。
if (IS_DESKTOP) installLocalTransport(api)

// ⭐ v21.218：桌面端 401 弹窗的同窗去重闸门。见下方响应拦截器注释。
// 弱网抖动会让一批并发请求同时 401，若每个都弹窗会瞬间刷屏并锁住 UI。
let activeDesktop401Shown = false

// ⭐ v21.218：仅供单测复位模块级去重闸门（生产代码不得调用）。
// 存在理由：该闸门是模块级状态，跨测试用例不重置会让后续用例被静默吞掉，
// 表现为「弹窗 0 次」这种极难定位的失败。测试必须能显式复位。
export function __resetDesktop401GateForTest () {
  activeDesktop401Shown = false
}

// 请求拦截：本地有 token 就带 Authorization 头（cookie 环境同样无害，后端头优先）
api.interceptors.request.use(config => {
  const token = getAccessToken()
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 双 token 机制：access token（2h）过期后，业务请求收到 401 时
// 先用 refresh 换取新 access，再透明重试原请求；
// refresh 也失效时才清理本地状态并跳转登录。
// 并发请求共享同一次刷新调用，避免惊群。
let refreshPromise = null

// v21.108 P1-12：跨标签页刷新单飞。remember=true 时 token 存 localStorage 多标签共享，
// 两个标签同时用同一旧 refresh 换票会触发后端一次性轮转的重放检测 → 整个 family 被撤、
// 两标签先后掉线（审查 P1-12）。用 localStorage 锁做跨标签单飞：抢到锁的标签负责刷新，
// 未抢到的等待对方完成（轮询 access token 变化/锁释放），超时则兜底自刷。
const REFRESH_LOCK_KEY = 'refresh_inflight'
const REFRESH_LOCK_TTL = 6000

function tryAcquireRefreshLock() {
  const now = Date.now()
  const raw = Number(localStorage.getItem(REFRESH_LOCK_KEY) || 0)
  if (raw && now - raw < REFRESH_LOCK_TTL) return false
  localStorage.setItem(REFRESH_LOCK_KEY, String(now))
  return true
}

function releaseRefreshLock() {
  localStorage.removeItem(REFRESH_LOCK_KEY)
}

// 等待其他标签刷新完成：access token 相对快照发生变化即视为新票就绪；锁被释放也重试一次
function waitForPeerRefresh(oldAccess, timeoutMs = 5000) {
  return new Promise((resolve) => {
    const start = Date.now()
    const timer = setInterval(() => {
      const cur = getAccessToken()
      if ((cur && cur !== oldAccess) || !localStorage.getItem(REFRESH_LOCK_KEY) || Date.now() - start > timeoutMs) {
        clearInterval(timer)
        resolve(!!(cur && cur !== oldAccess))
      }
    }, 150)
  })
}

function refreshSession() {
  // 显式登出后禁止复活：直接拒绝，走下方登出清理（清 token + 跳登录页）
  if (localStorage.getItem(LOGGED_OUT_KEY)) {
    return Promise.reject(new Error('logged out'))
  }
  if (!refreshPromise) {
    const doRefresh = () => {
      // 用裸 axios 调用，绕开本实例的拦截器，防止刷新请求自身触发 401 循环。
      // body 带 refresh_token（头通道）；为空对象时后端自动回退读 cookie（cookie 通道）。
      const body = getRefreshToken() ? { refresh_token: getRefreshToken() } : {}
      // v2.1.2：刷新端点同样跟随挂载点（裸 axios 不读实例 baseURL）。
      return axios.post(`${apiBase()}/auth/refresh`, body)
        .then(res => {
          // 刷新成功：本地 access 同步换新（cookie 也已由后端同步下发）
          updateAccessToken(res.data?.access_token)
          // v21.90 N-06：refresh token 轮换——后端每次刷新签发新 refresh，本地同步替换
          updateRefreshToken(res.data?.refresh_token)
          return res
        })
    }
    if (tryAcquireRefreshLock()) {
      // 本标签为刷新者：完成后释放锁
      refreshPromise = doRefresh().finally(() => { refreshPromise = null; releaseRefreshLock() })
    } else {
      // 其他标签在刷：等它完成，直接借用 localStorage 里的新票（getAccessToken 每次实时读取，
      // 重试请求会自动带上）；等待超时/失败则兜底自刷（退化为旧行为，不至于更糟）
      const oldAccess = getAccessToken()
      refreshPromise = waitForPeerRefresh(oldAccess).then(ok => {
        if (ok) return { data: { access_token: getAccessToken(), refresh_token: getRefreshToken() } }
        return doRefresh()
      }).finally(() => { refreshPromise = null })
    }
  }
  return refreshPromise
}

// 这些端点的 401 不代表"access 过期"，不触发刷新（登录失败、刷新失败等）
const NO_REFRESH_PATHS = [
  '/auth/login',
  '/auth/register',
  '/auth/refresh',
  '/auth/forgot-password',
  '/auth/verify-answer'
]

api.interceptors.response.use(
  res => res,
  async err => {
    const status = err.response?.status
    const url = err.config?.url || ''
    const skipRefresh = NO_REFRESH_PATHS.some(p => url.includes(p))

    // ⭐ v21.218：**桌面端不参与 refresh 流程**。
//
// 定罪：原实现 `!skipRefresh` 只按 URL 名单判断，于是桌面端的业务请求 401 会先走
// `refreshSession()` —— 而桌面端 refresh 是 LocalTransport 里的**空对象桩**
// （`POST /auth/refresh` → `{}`），`updateAccessToken(undefined)` 不写任何东西、
// Promise 却 resolve ⇒ 拦截器误判「刷新成功」⇒ `return api(err.config)` **重试原请求**
// ⇒ 再次 401（`__retried` 已置位）⇒ 落到下方登出清理 ⇒ `replace('/login')`。
//
// 也就是说：桌面端 401 不是「刷新失败被踢」，而是「刷新假成功后无限重试再被踢」，
// 全程还白跑一次无用请求。这与 v21.144 给 `/auth/refresh` 补桩的初衷**完全相反**
// （那处补桩是为了让 `auth.tryRefresh()` 无害成功，与拦截器无关）。
//
// 修法：桌面端直接跳过刷新 —— 设备会话的换票是 **Go 侧** ensureAccess 的职责
// （DPAPI 封存 → /api/auth/refresh 由 Go 执行），前端无权也无票可刷。
if (status === 401 && !skipRefresh && !IS_DESKTOP && !err.config.__retried) {
      try {
        await refreshSession()
        err.config.__retried = true
        return api(err.config) // 刷新成功，重试原请求（请求拦截会自动带新 token）
      } catch {
        // refresh 失效，走下方登出清理
      }
    }

    // v21.107 P1-6：429 统一兜底提示（同文案去重，透传调用点弹相同文案不会双弹）；
    // 登录/注册/找回等自带文案的端点已在 skipRefresh 名单内跳过，由调用点展示。
    if (status === 429 && !skipRefresh) {
      message.errorOnce(err.response?.data?.error || '请求过于频繁，请稍后再试', 3000)
    }

    // ⭐ v21.218（212 实机深度核查定罪）：**桌面端 401 绝不能静默跳登录页**。
    //
    // 定罪：桌面端 `/auth/refresh` 在 LocalTransport 里是**返回空对象的桩**（设备会话由
    // Go 侧 DPAPI 封存管理，桌面没有 cookie/refresh 语义）。于是下方 Web 端那套
    // 「401 → 尝试刷新 → 刷新必失败 → 清 user → replace('/login')」在桌面端**必然走到
    // 最后一步**，把用户踢回登录页；配合 router 对账的三重静默失败（桥未就绪即跳过 /
    // catch 吞错 / 已对账标志永不复位）⇒ 整个进程永不自愈。
    // 这就是 212 实机「反复重启最终仍显示未登录」的机制，也是近 50 版修不好的根因：
    // **Web 端鉴权语义被硬搬到了桌面**。
    //
    // 桌面端正确处置：**不自动跳转、不静默清态**。只弹一次确认框（同一窗口内仅弹一次，
    // 避免弱网抖动引发 401 风暴时弹窗刷屏），用户确认后才调 Go 侧清凭据并导航。
    // 这样「凭据在、等网络」与「凭据真的死了」两种情形在 UI 上可区分——后者才需要重登。
    if (status === 401 && IS_DESKTOP) {
      if (activeDesktop401Shown) {
        return Promise.reject(err)
      }
      activeDesktop401Shown = true
      // 交还 Promise：弹窗是异步交互，不能在拦截器里阻塞业务调用方
      Promise.resolve()
        .then(async () => {
          const reason = err.response?.data?.error || '登录会话已失效'
          const goLogin = await message.confirm(
            `${reason}。\n\n本机数据不会丢失，重新登录后将自动恢复同步。是否现在重新登录？`,
            { title: '需要重新登录', confirmButtonText: '重新登录', cancelButtonText: '稍后再说' }
          )
          if (!goLogin) {
            // 用户选择稍后：允许在同一进程内再弹一次（网络恢复后可能自愈）
            activeDesktop401Shown = false
            return
          }
          // 确认后由 Go 侧清封存凭据（幂等），再回登录页
          try { await clearDesktopCredentials() } catch { /* 绑定不可用时仍允许继续 */ }
          clearTokens()
          localStorage.removeItem('user')
          localStorage.removeItem('userId')
          if (window.location.pathname !== withBase('/login')) {
            window.location.replace(withBase('/login'))
          }
        })
        .catch(() => { activeDesktop401Shown = false })
      return Promise.reject(err)
    }

    if (status === 401) {
      // v21.108 P1-10：被动失效（会话过期/他端改密/被管理员处置）跳登录前给出原因，
      // 不再「页面突然就回登录页」零解释。主动登出（logged_out 在位）不弹——调用点已提示。
      const activeLogout = localStorage.getItem(LOGGED_OUT_KEY)
      if (!activeLogout && window.location.pathname !== withBase('/login')) {
        message.errorOnce(err.response?.data?.error || '登录状态已失效，请重新登录', 2000)
      }
      clearTokens()
      localStorage.removeItem('user')
      localStorage.removeItem('userId')
      if (window.location.pathname !== withBase('/login')) {
        // v21.107 P2-20：replace 不留历史条目，减轻登出/被踢后浏览器后退看到残留页
        window.location.replace(withBase('/login'))
      }
    }
    return Promise.reject(err)
  }
)

export default api
