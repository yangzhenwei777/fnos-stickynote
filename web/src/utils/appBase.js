/**
 * 应用挂载基址推导（v2.1.2 · 移动端启动卡死根治）。
 *
 * ## 背景
 * 本应用有两条加载链路：
 *   1. fnOS Web 桌面：`ui/config` 的 `type: url` + `port` → 直连 `http://<host>:13389/`，**站点根**。
 *   2. fnOS 手机 App / 远程访问：无法直连任意端口，经网关以**子路径**加载，
 *      形如 `/app/<appname>/…`。
 *
 * 2.1.1 及之前，`index.html` 的资源与 axios 的 baseURL 全写成根绝对路径
 * （`/assets/…`、`/api`）。子路径下浏览器把 `/assets/` 解析到**宿主根**，
 * 请求落到飞牛主站 → 404 + `text/html` → `<script type="module">` 因 MIME
 * 不符被拒绝执行 → Vue 永不挂载 → 静态启动占位永久停留（且兜底代码同在
 * 该 bundle 内，故连「重新加载」都不出现）。
 *
 * ## 推导优先级
 * 1. **桌面壳**（`IS_DESKTOP`）：恒返回 `''`。Wails assetserver 就在根，
 *    保持与历史完全一致的路径语义（**桌面端零行为变化**）。
 * 2. 服务端注入的 `<meta name="sn-app-base" content="/app/stickynote">`：
 *    服务端能从请求路径推算前缀时注入（见 `server/router/router.go`）。
 * 3. `<base href>` 元素：同上，服务端注入。
 * 4. 兜底：当前路径以 `/` 结尾时取其目录部分（`/app/stickynote/` → `/app/stickynote`），
 *    否则返回 `''`（多级深路由且无注入时保守回根，不猜错）。
 *
 * ## 返回值约定
 * - 根路径 → `''`（调用方拼成 `/api`、`/assets`，与历史一致）
 * - 子路径 → `'/app/stickynote'`（**不带尾斜杠**）
 */

import { IS_DESKTOP } from '@/api/local/transport'

/** 服务端注入点的 meta 名，前后端共用契约 */
export const APP_BASE_META = 'sn-app-base'

/** 规范化：去掉尾斜杠；`/` 归一为 `''` */
export function normalizeBase (raw) {
  if (!raw) return ''
  let v = String(raw).trim()
  if (!v || v === '/') return ''
  // 只保留路径部分（防御服务端误传完整 URL）
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) {
    try {
      v = new URL(v).pathname
    } catch {
      return ''
    }
  }
  // 去掉查询/哈希
  v = v.split('?')[0].split('#')[0]
  // 必须以 / 开头
  if (!v.startsWith('/')) v = '/' + v
  // 去掉重复与尾斜杠
  v = v.replace(/\/{2,}/g, '/').replace(/\/+$/, '')
  // 拒绝路径穿越（安全兜底）
  if (v.split('/').some((seg) => seg === '..' || seg === '.')) return ''
  return v
}

/**
 * 计算当前应用的挂载基址（不带尾斜杠）。
 * @returns {string} `''` 表示站点根
 */
export function detectBase () {
  // 桌面壳：恒根，保持历史语义
  if (IS_DESKTOP) return ''

  if (typeof document === 'undefined' || typeof window === 'undefined') return ''

  // ① 服务端注入的 meta（最可靠）
  try {
    const meta = document.querySelector(`meta[name="${APP_BASE_META}"]`)
    const content = meta?.getAttribute('content')
    if (content != null && content !== '') return normalizeBase(content)
    if (content === '') return '' // 显式声明「根」
  } catch {
    /* 忽略：继续兜底 */
  }

  // ② <base href>
  try {
    const baseEl = document.querySelector('base[href]')
    if (baseEl) {
      const p = new URL(baseEl.href, window.location.href).pathname
      return normalizeBase(p)
    }
  } catch {
    /* 忽略 */
  }

  // ③ 路径兜底：仅当以 `/` 结尾（目录形态）才认为是挂载点
  try {
    const p = window.location.pathname || '/'
    if (p.endsWith('/')) return normalizeBase(p)
  } catch {
    /* 忽略 */
  }

  return ''
}

/** 拼出 API 基址（`''` → `/api`，`/app/x` → `/app/x/api`） */
export function apiBase () {
  return `${detectBase()}/api`
}

/**
 * 把应用内绝对路径挂到当前挂载点下。
 *
 * **整页跳转必须走它**：`window.location.replace('/login')` 在子路径挂载下会跳到
 * 宿主根（飞牛主站），把用户直接甩出应用——实测就是靠这一条把 URL 从
 * `/app/stickynote/login` 变成 `/login`，进而 base 自举读到错误路径。
 * vue-router 的 next('/login') 不需要（router 自己带 base）。
 *
 * @param {string} path 以 `/` 开头的应用内路径
 * @returns {string}
 */
export function withBase (path) {
  const base = detectBase()
  if (!base) return path
  const p = path.startsWith('/') ? path : `/${path}`
  return base + p
}

/** 拼出静态资源基址（供 SW 注册等使用） */
export function assetBase () {
  return detectBase()
}
