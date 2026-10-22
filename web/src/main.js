import { createApp } from 'vue'
import { createPinia } from 'pinia'
// Element Plus 组件与样式由 unplugin 按需自动注入（见 vite.config.js），
// 此处仅保留暗色主题的 CSS 变量（按需注入不含主题变量）
import 'element-plus/theme-chalk/dark/css-vars.css'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'
import App from './App.vue'
import router from './router'
import './styles/global.css'
import { useTheme } from './composables/useTheme'
import { IS_DESKTOP } from './api/local/transport'
import { installDesktopResumeTrigger, installGlobalErrorHooks } from './api/local/desktopApi'
import { installSpaRedirectRestore } from './api/local/spaRedirect'
import { sanitizeLocalStorage } from './utils/storage'
import { clearBootGuard, showBootFailure } from './utils/bootGuard'

// v21.195 存量自愈（必须在任何 store/router 初始化之前）：
// 旧版本会把字面量 "undefined" 写进 localStorage.user（JSON.stringify(undefined) 的
// 退化输出），导致 auth store 初始化抛错 → 根组件渲染中断 → 整个窗口纯白。
// 这里先把确证为毒的键清掉，**老用户升级后无需手工清数据**。
try {
  const { removed, scanned } = sanitizeLocalStorage()
  if (removed.length) {
    console.warn(`[boot] 已清理损坏的本地存储键（扫描 ${scanned} 项）: ${removed.join(', ')}`)
  }
} catch (e) {
  console.warn('[boot] 本地存储自愈跳过:', e)
}

const app = createApp(App)

for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

app.use(createPinia())
app.use(router)

// Initialize theme
const { initTheme } = useTheme()
initTheme()

// v21.194 白屏根治：全局异常钩子——JS 层任何失败都不再表现为纯白
//（错误摘要经 ReportJSError 落 Go 日志 + 屏幕横幅给「重载」出口）。
// v21.195：**前置到 mount 之前**——194 装在 mount 之后，而 mount 自身抛错时
// 钩子根本没装、app 内横幅也渲染不出来，等于零防护。
installGlobalErrorHooks()

// v21.194/v21.195：启动占位三态。挂载成功才移除；挂载失败把占位转失败态
//（展示错误摘要 + 保留纯 HTML 的「重新加载」出口），杜绝「无任何提示的纯白」。
try {
  app.mount('#app')
  clearBootGuard()
} catch (err) {
  showBootFailure(err?.message || String(err))
  throw err
}

// 桌面壳：最小化/锁屏回来（页面重新可见或拿到焦点）敲一次后台同步调度器。
// 只是加速器，不改变正确性——Web 构建下 IS_DESKTOP=false，这里什么都不做。
if (IS_DESKTOP) installDesktopResumeTrigger()

// v21.194：登录后 15s 二次整页重载（sn_sync_refresh）已退役——数据拾取由
// NotesView/DesktopAccountCard 的 installSyncRefreshPolling 轮次通知承担（193 接线），
// 双重整页重载是白屏取证轮锁定的竞态温床（真机实证：重载窗口与 bootstrap 并发时 DOM 空壳）。
// 存量 sessionStorage 键在此一次性清理，防老版本残留误触发。
sessionStorage.removeItem('sn_sync_refresh')

// 桌面壳：还原被 302 兜下来的深链接（`/?_spa=%2Flogin` → 客户端导航回 /login）。
// 必须在 mount 之后调用（router 已就绪）。整页重载到真实深链接会丢 Wails 绑定桥，
// 所以这里坚持用 router 还原，不重载。Web 构建下 _spa 不存在，函数直接返回 null。
installSpaRedirectRestore(router)
