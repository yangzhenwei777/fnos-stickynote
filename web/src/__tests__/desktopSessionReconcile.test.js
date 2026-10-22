// desktopSessionReconcile.test.js —— 桌面开机对账回归钉。
//
// 背景：桌面壳前端本地存储的登录态与 Go 侧设备会话是两套状态。当前端残留登录态、
// Go 侧无可用会话时会跳过登录页直进空工作区（数据全无 + 账号与设备显示未登录 +
// 重建同步 NO_REFRESH）；反之 Go 会话有效而前端态被清时应免重登恢复展示态。
//
// ⭐ v21.218（212 实机深度核查）**重写本钉**：旧钉钉住的恰是病根本身。
// 旧契约（v21.152）：
//   · 恢复展示态走 `/auth/login` → LocalLogin() 桩绑定（存在桥二次可用性失败点）；
//   · Go 说已登录但 username 缺失时**静默回退 'local'**；
//   · 桩登录抛错**静默吞掉**，仍返回 'restored'（假装成功）。
// 这三条合起来 = 恢复路径上的三重静默失败，配合
// `desktopSessionChecked = true 永不复位` 与 `catch {}`，
//   ⇒ Go 侧 logged_in=true 而 UI 恒显未登录，整个进程永不自愈。
//   ⇒ 212 实机「反复重启最终仍显示未登录」的直接机制（近 50 版修不好的根）。
//
// 新契约（v21.218）：
//   · 恢复展示态**直接写 Go 随 AuthStatus 下发的 session_user**，零网络、零桥二次调用；
//   · Go 说已登录却没给 session_user = **契约破坏，抛错**（不静默、不假装成功）；
//   · 抛错由上层 attemptDesktopSessionReconcile 计数并**允许重试**（不置已对账标志）。
import { describe, it, expect, vi, beforeEach } from 'vitest'

// 只翻转 IS_DESKTOP，保留真实 transport（request.js 会 installLocalTransport(api)，缺导出即炸）
vi.mock('@/api/local/transport', async (importOriginal) => ({
  ...(await importOriginal()),
  IS_DESKTOP: true
}))

const { reconcileDesktopSession, desktopReconcileState } = await import('@/router')

function fakeAuth (authenticated) {
  return {
    isAuthenticated: authenticated,
    clearAuth: vi.fn(),
    setSessionUser: vi.fn(),
    login: vi.fn().mockResolvedValue({})
  }
}

describe('reconcileDesktopSession（桌面开机对账）', () => {
  beforeEach(() => vi.clearAllMocks())

  it('前端残留登录态 + Go 无会话 → 作废前端态（回登录页）', async () => {
    const auth = fakeAuth(true)
    const r = await reconcileDesktopSession(auth, async () => ({ logged_in: false, profile: false }))
    expect(r).toBe('cleared')
    expect(auth.clearAuth).toHaveBeenCalledTimes(1)
    expect(auth.setSessionUser).not.toHaveBeenCalled()
  })

  // ⭐ v21.218：恢复展示态改为直写 Go 下发的 session_user，不再走桩绑定
  it('Go 会话有效 + 前端态缺失 → 直接写 session_user（免重登，不走桩）', async () => {
    const auth = fakeAuth(false)
    const su = { id: 1, username: 'admin', role: 'user', status: 'active' }
    const r = await reconcileDesktopSession(auth, async () => ({ logged_in: true, username: 'admin', session_user: su }))
    expect(r).toBe('restored')
    expect(auth.setSessionUser).toHaveBeenCalledWith(su)
    // 关键回归：旧实现调 login()，这里必须不再调用
    expect(auth.login).not.toHaveBeenCalled()
    expect(auth.clearAuth).not.toHaveBeenCalled()
  })

  // ⭐ v21.218 新增钉：契约破坏必须暴露，不得静默伪装成功
  it('Go 已登录但缺 session_user → 抛错（不静默回退 local）', async () => {
    const auth = fakeAuth(false)
    await expect(reconcileDesktopSession(auth, async () => ({ logged_in: true }))).rejects.toThrow(/session_user/)
    expect(auth.setSessionUser).not.toHaveBeenCalled()
    expect(auth.login).not.toHaveBeenCalled()
  })

  it('两侧一致（都登录 / 都未登录）→ 不动前端态', async () => {
    const a1 = fakeAuth(true)
    expect(await reconcileDesktopSession(a1, async () => ({ logged_in: true }))).toBe('consistent')
    expect(a1.clearAuth).not.toHaveBeenCalled()
    expect(a1.setSessionUser).not.toHaveBeenCalled()

    const a2 = fakeAuth(false)
    expect(await reconcileDesktopSession(a2, async () => ({ logged_in: false }))).toBe('consistent')
    expect(a2.setSessionUser).not.toHaveBeenCalled()
  })

  // ⭐ v21.218 新增钉：失败必须可观测（静默吞错是旧病根之一）
  it('恢复失败时计数与 lastError 留痕，供上层重试判断', async () => {
    const before = desktopReconcileState.failures
    const auth = fakeAuth(false)
    await expect(reconcileDesktopSession(auth, async () => ({ logged_in: true }))).rejects.toThrow()
    expect(desktopReconcileState.failures).toBeGreaterThan(before)
    expect(desktopReconcileState.lastError).toBeTruthy()
  })

  // ⭐ v21.218 新增钉：statusFn 抛错（桥读取失败）同样不得静默
  it('statusFn 抛错（桥不可用）→ 向上传播，不静默', async () => {
    const auth = fakeAuth(false)
    await expect(reconcileDesktopSession(auth, async () => { throw new Error('DESKTOP_BRIDGE_UNAVAILABLE') })).rejects.toThrow(/BRIDGE/)
    expect(auth.setSessionUser).not.toHaveBeenCalled()
  })
})