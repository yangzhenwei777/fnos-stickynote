import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import api, { setTokens, updateAccessToken, getRefreshToken, clearTokens } from '../api/request'
import { IS_DESKTOP } from '../api/local/transport'
import { readJSON, writeJSON } from '../utils/storage'
import { clearGrants } from '../utils/folderGrants'

// 双通道认证（见 api/request.js）：cookie 为主、Authorization 头兜底。
// localStorage 中的 user / userId 为非敏感展示数据；token 存储遵循"记住我"语义。
// logged_out 标志：显式登出后阻止路由守卫/拦截器用残留 refresh cookie 自动复活会话；
// 显式登录/注册成功时清除。fnOS 反代与 iframe 环境下 Set-Cookie 清除未必生效，
// 仅靠清 cookie 无法保证登出（v18.1 后仍有真机复活案例），此标志为前端兜底。
export const LOGGED_OUT_KEY = 'logged_out'

export const useAuthStore = defineStore('auth', () => {
  // v21.195：readJSON 取代裸 JSON.parse——旧版本把「字面量字符串 "undefined"」写进
  // localStorage.user（JSON.stringify(undefined) 的退化输出），裸 parse 抛 SyntaxError
  // 会让本store 初始化中断 → 根组件渲染中断 → 纯白屏（2026-10-08 绿色版实证）。
  // readJSON 对毒值/非法 JSON 一律清键并回落 null，见 utils/storage.js。
  const user = ref(readJSON('user'))

  const isAuthenticated = computed(() => !!user.value)
  const isAdmin = computed(() => user.value?.role === 'admin')

  function setAuth(tokens, u, remember = false) {
    if (tokens?.access) {
      setTokens(tokens.access, tokens.refresh, remember)
    }
    // 显式建立会话（登录/注册/刷新恢复）即解除登出锁定
    localStorage.removeItem(LOGGED_OUT_KEY)
    user.value = u ?? null
    // v21.195：写侧守卫——u 为undefined 时**删键**而非落"undefined"字符串
    // （写侧是这次白屏事故的源头，读侧再加固只是第二道防线）。
    writeJSON('user', u)
    if (u?.id !== undefined && u?.id !== null) {
      localStorage.setItem('userId', String(u.id))
    } else {
      localStorage.removeItem('userId')
    }
  }

  function clearAuth() {
    user.value = null
    clearTokens()
    clearGrants() // v2.3.0：退出账户即恢复文件夹锁定
    localStorage.removeItem('user')
    localStorage.removeItem('userId')
  }

  // ⭐ v21.218（212 实机深度核查）：**仅写入展示态**，不碰 token、不改登出锁定。
  //
  // 用途：桌面端 Go 侧会话有效（AuthStatus.logged_in=true）但前端展示态被清时，
  // 由 Go 随 AuthStatus 直下发的 session_user 直接恢复 UI。
  //
  // 为什么必须独立于 setAuth：
  //   setAuth 会 clearTokens + 摘除 logged_out 锁定 —— 对「恢复已有会话」是错的
  //   （token 本来就在 Go 侧内存里，清前端 token 无意义；而登出锁定必须尊重）。
  // 为什么不再走 login(username,'')：
  //   旧实现依赖 /auth/login → LocalLogin() 桩绑定，存在桥二次可用性失败点，
  //   且桩返回写死 role/user，与真实档案可能不一致。
  function setSessionUser(u) {
    if (!u) return
    user.value = u
    writeJSON('user', u)
    if (u.id !== undefined && u.id !== null) {
      localStorage.setItem('userId', String(u.id))
    } else {
      localStorage.removeItem('userId')
    }
  }

  async function login(username, password, remember = false) {
    const { data } = await api.post('/auth/login', { username, password, remember })
    // v21.58 F-03：TOTP 首因子通过（totp_required）时不建立会话——
    // 避免 isAuthenticated 提前置真；正式会话在 verify-totp 成功后建立。
    if (data.totp_required) {
      return data
    }
    setAuth({ access: data.access_token, refresh: data.refresh_token }, data.user, remember)
    return data
  }

  async function register(payload) {
    const { data } = await api.post('/auth/register', payload)
    setAuth({ access: data.access_token, refresh: data.refresh_token }, data.user, false)
    return data
  }

  // 恢复会话（自动登录）：body 带 refresh_token（头通道），后端自动回退 cookie。
  // 成功返回 true 并写入用户信息与新 access。
  async function tryRefresh() {
    try {
      const body = getRefreshToken() ? { refresh_token: getRefreshToken() } : {}
      const { data } = await api.post('/auth/refresh', body)
      updateAccessToken(data.access_token)
      setAuth(null, data.user)
      return true
    } catch {
      return false
    }
  }

  async function logout() {
    // 先落登出标志：即使后端请求失败/被反代吞掉，也不会被残留 cookie 复活
    localStorage.setItem(LOGGED_OUT_KEY, '1')
    // 桌面壳：/auth/logout 在 LocalTransport 里是空映射，真正的登出在 Go 侧 Logout
    // （卸 worker + 清 DPAPI 封存的 refresh）。此前这里只清了本地态，用户以为退出了、
    // 凭据其实还躺在盘上 —— 刀 6 修。Go 登出失败时**不**继续清本地态并抛错，
    // 免得又造出一个"看起来登出了"的假状态。
    if (IS_DESKTOP) {
      const { logoutDesktop } = await import('../api/local/desktopApi')
      await logoutDesktop()
    }
    try {
      await api.post('/auth/logout')
    } catch {}
    clearAuth()
  }

  return {
    user,
    isAuthenticated,
    isAdmin,
    login,
    register,
    logout,
    tryRefresh,
    setAuth,
    setSessionUser,
    clearAuth
  }
})
