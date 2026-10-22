import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore, LOGGED_OUT_KEY } from '@/stores/auth'
import { getSetupStatus } from '@/api/auth'
import { message } from '@/utils/message'
import { IS_DESKTOP } from '@/api/local/transport'
import { detectBase } from '@/utils/appBase'
import { authStatus, desktopBridgeAvailable } from '@/api/local/desktopApi'

const routes = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/LoginView.vue')
  },
  {
    path: '/register',
    name: 'Register',
    component: () => import('@/views/RegisterView.vue')
  },
  {
    path: '/forgot-password',
    name: 'ForgotPassword',
    component: () => import('@/views/ForgotPasswordView.vue')
  },
  {
    path: '/',
    component: () => import('@/layouts/MainLayout.vue'),
    meta: { requiresAuth: true },
    children: [
      {
        path: '',
        name: 'Notes',
        component: () => import('@/views/NotesView.vue')
      },
      {
        path: 'notes-list',
        name: 'NotesList',
        component: () => import('@/views/NotesListView.vue')
      },
      {
        path: 'trash',
        name: 'Trash',
        component: () => import('@/views/TrashView.vue')
      },
      {
        path: 'profile',
        name: 'Profile',
        component: () => import('@/views/ProfileView.vue')
      },
      // ⭐ v2.1.3：管理后台路由**一律单段**（admin-users 而非 admin/users）。
      //
      // 动因（2.1.2 遗留的已知限制）：fnOS 手机 App 经网关 /app/<appname>/ 加载，
      // 且网关可能**剥掉**前缀转发。此时服务端算不出挂载点，浏览器只能按地址栏
      // 解析 `./assets/…` 这样的相对引用——
      //   单段路由 /app/stickynote/admin-users → 解析到 /app/stickynote/assets/…  ✅
      //   多段路由 /app/stickynote/admin/users → 解析到 /app/stickynote/admin/assets/… ❌
      // 深路由 URL 里的**路由段越多，相对基址就被多吃掉一层**。
      // 把所有路由压成单段后，无论前缀剥不剥、路由多深、是否整页刷新，
      // 相对引用都稳定落在挂载点根上，这条限制从"已知"变成"不存在"。
      //
      // 兼容：旧的 /admin/xxx 由下方 legacyAdminRedirects 永久重定向（保留 query 与 hash）。
      {
        path: 'admin-users',
        name: 'AdminUsers',
        component: () => import('@/views/admin/UsersView.vue'),
        meta: { requiresAdmin: true }
      },
      {
        path: 'admin-logs',
        name: 'AdminLogs',
        component: () => import('@/views/AdminLogsView.vue'),
        meta: { requiresAdmin: true }
      },
      {
        path: 'admin-configs',
        name: 'AdminConfigs',
        component: () => import('@/views/admin/ConfigView.vue'),
        meta: { requiresAdmin: true }
      },
      {
        path: 'admin-limits',
        name: 'AdminLimits',
        component: () => import('@/views/admin/LimitsView.vue'),
        meta: { requiresAdmin: true }
      },
      {
        path: 'admin-backup',
        name: 'AdminBackup',
        component: () => import('@/views/admin/BackupView.vue'),
        meta: { requiresAdmin: true }
      }
    ]
  }
]

// v2.1.3 兼容层：2.1.2 及更早版本的 /admin/xxx 深链接仍然可用。
// 放在顶层（不进 MainLayout children）：redirect 在路由匹配阶段就解析成新路径，
// 守卫随后按**新路由**的 meta 判定，权限语义与原来完全一致。
// 用函数式 redirect 保 query 与 hash——备份页带 ?id=… 这类参数时不会丢。
const legacyAdminRedirects = [
  ['/admin/users', '/admin-users'],
  ['/admin/logs', '/admin-logs'],
  ['/admin/configs', '/admin-configs'],
  ['/admin/limits', '/admin-limits'],
  ['/admin/backup', '/admin-backup']
].map(([from, to]) => ({
  path: from,
  redirect: (route) => ({ path: to, query: route.query, hash: route.hash })
}))

routes.push(...legacyAdminRedirects)

const router = createRouter({
  // v2.1.2 移动端子路径根治：history 基址跟随实际挂载点。
  // 根路径（fnOS Web 桌面直连）→ ''；子路径（/app/<appname>/ 网关）→ '/app/<appname>'。
  // 桌面壳恒为 ''，行为与历史完全一致。
  history: createWebHistory(detectBase()),
  routes
})

// 自动登录恢复每次应用加载只尝试一次（避免每次导航都请求 refresh）
let refreshAttempted = false

// v21.152：桌面壳以 Go 侧会话为准做开机对账。
// 生产实锤：前端本地存储残留的登录态在 Go 侧无可用会话时，
// 应用会跳过登录页直进空工作区——数据全无、账号与设备显示未登录、重建同步报 NO_REFRESH。
// 反向也补：Go 会话有效而前端态被清（WebView2 存储丢失等）→ 用 Go 直下发的展示态恢复，免重登。
//
// ⭐ v21.218（212 实机深度核查定罪）：对账改为**重试 + 可观测**。
//
// 212 实机定罪（三重静默失败 = 整个进程永不自愈）：
//   ① `desktopBridgeAvailable()` 为假即**整体跳过**对账（首启桥未就绪的窗口真实存在）；
//   ② `catch {}` 把失败**静默吞掉**，不留任何日志；
//   ③ `desktopSessionChecked = true` **永不复位**，错过即永久错过。
// 三者叠加 ⇒ Go 侧 logged_in=true 而 UI 恒显未登录。
//
// 修法：① 失败不置已对账标志（下次导航重试）；② 失败上 console + 保留可观测计数；
//      ③ 在应用挂载后另起一个**独立轮询**做兜底对账，不依赖路由守卫是否被触发。
let desktopSessionChecked = false

// 对账失败的可观测计数（供排障与单测断言；不进 UI 避免噪音）
export const desktopReconcileState = { attempts: 0, failures: 0, lastError: null }

// reconcileDesktopSession 对账实现（独立导出：jsdom 里直接驱动完整守卫成本过高，见 __tests__）。
// 返回 'cleared' | 'restored' | 'consistent'。statusFn 抛错由调用方兜底（不阻断导航）。
//
// ⭐ v21.218：恢复展示态**不再走 /auth/login 桩绑定**，改为直接写 Go 下发的 session_user。
// 桩绑定是旧实现里的第二个失败点（桥二次可用性），移除后恢复路径只剩「读一次 AuthStatus」。
export async function reconcileDesktopSession (auth, statusFn) {
  const st = await statusFn()
  const goLoggedIn = !!(st && st.logged_in === true)
  desktopReconcileState.attempts++
  if (auth.isAuthenticated && !goLoggedIn) {
    auth.clearAuth()
    return 'cleared'
  }
  if (!auth.isAuthenticated && goLoggedIn) {
    const su = st.session_user
    if (!su) {
      // Go 说已登录却没给展示态 = 契约破坏。不静默吞，留痕让上层重试。
      const err = new Error('AuthStatus.logged_in=true but session_user missing')
      desktopReconcileState.failures++
      desktopReconcileState.lastError = err
      throw err
    }
    auth.setSessionUser(su)
    return 'restored'
  }
  return 'consistent'
}

// ⭐ v21.218：守卫内的一次性对账 + 失败可重试。
// 关键：**只有对账成功（或确认一致/已清）才置 desktopSessionChecked**；
// 失败时不置 ⇒ 下次导航会再试，进程内终将自愈。
async function attemptDesktopSessionReconcile (auth, statusFn) {
  if (!IS_DESKTOP) return
  if (desktopSessionChecked) return
  if (!desktopBridgeAvailable()) {
    // 桥未就绪：不是错误（首启窗口），不置标志也不计数，等下一次导航/轮询兜底。
    return
  }
  try {
    await reconcileDesktopSession(auth, statusFn)
    desktopSessionChecked = true
    desktopReconcileState.lastError = null
  } catch (e) {
    desktopReconcileState.failures++
    desktopReconcileState.lastError = e
    console.error('[desktop] session reconcile failed, will retry:', e)
    // 有意不置 desktopSessionChecked —— 允许重试
  }
}

router.beforeEach(async (to, from, next) => {
  const auth = useAuthStore()

  if (IS_DESKTOP) {
    await attemptDesktopSessionReconcile(auth, authStatus)
  }

  // 无本地会话状态时，尝试用 refresh cookie 恢复会话（自动登录）；
  // 显式登出后（logged_out 标志）跳过——fnOS 反代/iframe 下残留 cookie 可能未清，
  // 放任 tryRefresh 会把刚登出的会话复活（v18.1 真机退出失效的根因）。
  if (!auth.isAuthenticated && !refreshAttempted && !localStorage.getItem(LOGGED_OUT_KEY)) {
    refreshAttempted = true
    await auth.tryRefresh()
  }

  // 首次访问检查：如果没有用户，跳转到管理员注册
  if (to.path === '/login' || to.path === '/') {
    try {
      const { data } = await getSetupStatus()
      if (data.needs_setup && to.path !== '/register') {
        next('/register?setup=true')
        return
      }
    } catch (e) {
      // 错误分类：服务异常/断网给出明确反馈，其余情况不阻塞流程
      if (e.response?.status === 503) {
        message.error('服务暂时不可用，请稍后再试')
      } else if (e.code === 'ERR_NETWORK') {
        message.error('无法连接到服务器，请确认应用正在运行')
      } else {
        console.error('Setup status check failed:', e)
      }
    }
  }

  if (to.meta.requiresAuth && !auth.isAuthenticated) {
    next('/login')
  } else if (to.meta.requiresAdmin && !auth.isAdmin) {
    // v21.107 P2-14：非管理员进后台不再静默弹回首页（此前表现为"点了没反应"）
    message.error('该页面需要管理员权限')
    next('/')
  } else if ((to.path === '/login' || to.path === '/register') && auth.isAuthenticated) {
    next('/')
  } else {
    next()
  }
})

// v21.195 白屏兜底：动态 import（路由懒加载 chunk）失败在 Vue Router 里被吞成
// 导航错误，router-view 保持空白 → 又一次纯白屏且无任何提示。
// 处置：识别 chunk/动态导入类错误 → 整页重载一次；同一会话内不重复重载（防循环）。
const CHUNK_RELOAD_FLAG = 'sn_chunk_reload'
const CHUNK_ERR_RE = /Failed to fetch dynamically imported module|Importing a module script failed|dynamically imported module|ChunkLoadError|Loading chunk/i

router.onError((err, to) => {
  const msg = String((err && err.message) || err || '')
  if (!CHUNK_ERR_RE.test(msg)) return
  try {
    if (sessionStorage.getItem(CHUNK_RELOAD_FLAG) === '1') return
    sessionStorage.setItem(CHUNK_RELOAD_FLAG, '1')
  } catch { /* 隐私模式降级：仍重载一次 */ }
  console.warn('[router] 路由组件加载失败，整页重载一次:', msg, to?.fullPath || '')
  window.location.reload()
})

router.afterEach(() => {
  // 成功导航即解除「已重载过」标记：后续若再遇chunk 失败仍可自愈一次
  try { sessionStorage.removeItem(CHUNK_RELOAD_FLAG) } catch { /* 忽略 */ }
})

// ⭐ v21.218：桌面端会话对账的**兜底轮询**（守卫之外的最后一道保险）。
//
// 为什么需要它：守卫内的对账只在**发生导航**时才跑。若应用启动后用户停在同一个路由
// （212 实机即如此：启动 → 对账恰好撞上桥未就绪 → 守卫再无第二次导航），
// 守卫侧的重试永远不会发生 ⇒ 又回到「Go 已登录 / UI 未登录」的分裂态。
//
// 语义：递增间隔重试，成功即停；总时长有界（避免长会话空转）。
// 只在桌面端跑，Web 端零开销（构建期由 IS_DESKTOP 常量消除）。
const DESKTOP_RECONCILE_DELAYS = [300, 800, 1500, 3000, 5000]

function installDesktopSessionReconcilePolling () {
  if (!IS_DESKTOP) return
  let i = 0
  const tick = async () => {
    if (desktopSessionChecked) return // 已确认一致/已清，停
    if (i >= DESKTOP_RECONCILE_DELAYS.length) {
      console.warn('[desktop] session reconcile polling exhausted; UI 登录态可能与 Go 侧不一致')
      return
    }
    await new Promise(resolve => setTimeout(resolve, DESKTOP_RECONCILE_DELAYS[i++]))
    if (desktopSessionChecked) return
    try {
      const auth = useAuthStore()
      await attemptDesktopSessionReconcile(auth, authStatus)
    } catch (e) {
      console.error('[desktop] session reconcile poll error:', e)
    }
    if (!desktopSessionChecked) tick()
  }
  tick()
}

installDesktopSessionReconcilePolling()

export default router
