// spaRedirect.js —— 桌面壳深链接还原（配合 desktop/main.go 的 desktopSPAFallback）。
//
// 为什么需要它：Wails v2 **只在请求路径以 "/" 或 "/index.html" 结尾时**才注入 runtime
// （`window.go` 绑定桥的唯一来源，见 wails/v2 `pkg/assetserver/assetserver.go` 的
// `isRuntimeInjectionMatch` + `processIndexHTML`）。
// 所以桌面侧对未命中的深链接（`/login`、`/profile` …）**不能**直接吐 index.html ——
// 那样页面能渲染但**没有桥**，一切绑定调用都报「桌面桥不可用」。
// Go 侧改为 302 跳到 `/?_spa=<原路径>`（根路径必然被注入），本模块负责把路由还原回来。

/**
 * readSpaRedirect 从 query string 里读出待还原的路由。
 * 只接受站内绝对路径（以单个 "/" 开头），拒绝 "//host" 与任何非路径值 —— 防开放重定向。
 * @returns {string|null}
 */
export function readSpaRedirect (search) {
  let raw
  try {
    raw = new URLSearchParams(search || '').get('_spa')
  } catch {
    return null
  }
  if (!raw) return null
  if (!raw.startsWith('/') || raw.startsWith('//')) return null
  return raw
}

/**
 * installSpaRedirectRestore 在应用启动时还原被 302 兜下来的深链接。
 *
 * 关键：**用客户端导航还原，不触发文档重载** —— 一旦整页重载到真实路径，路径又不满足
 * Wails 的注入条件，桥会再次消失（这正是本轮修的那个坑）。因此这里
 * `history.replaceState` 把地址栏改成真实路径（不加载文档），再 `router.replace` 走前端路由。
 *
 * @param {import('vue-router').Router} router
 * @param {Location} [loc] 仅测试注入，默认 window.location
 * @returns {string|null} 已还原的路由；无 _spa 时返回 null
 */
export function installSpaRedirectRestore (router, loc) {
  const l = loc || (typeof window !== 'undefined' ? window.location : null)
  if (!router || !l) return null
  const target = readSpaRedirect(l.search)
  if (!target) return null
  try {
    // 清掉 _spa query，避免刷新后又被兜一次（清 URL 不触发重载）
    if (typeof window !== 'undefined' && window.history && window.history.replaceState) {
      window.history.replaceState(null, '', target)
    }
  } catch {
    // replaceState 失败不影响功能（地址栏留 query，路由仍被还原）
  }
  router.replace(target)
  return target
}
