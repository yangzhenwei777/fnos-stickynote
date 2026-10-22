// desktopStatusStore.test.js —— ⭐ v21.220 状态可达性矩阵回归钉
//
// ══════════════════════════════════════════════════════════════════════════
// 本文件的定位（回答「为什么非要真机才能测出来」）
// ══════════════════════════════════════════════════════════════════════════
// 213 实机事故的完整机制：
//   Go 侧一切正常（换代成功 / 48 笔记 / 凭据完好 / localStorage.user 正确写入），
//   但账号卡片恒显示「未登录：处于离线单机状态」。
//
//   根因链：
//     ① 卡片 onMounted 调 authStatus() → 首屏恰逢换代窗口 → 抛错
//     ② st 停在 {} → 四条文案分支全 false → 显示"未登录"
//     ③ 兜底 installSyncRefreshPolling 只在 gen_epoch 变大 / rounds 增长时回调
//        而换代**早于监听建立**完成 ⇒ 首拍即追平 ⇒ 此后永不触发
//     ④ catch 里的 failStreak>=2 救不了：轮询本身成功（SyncScheduleStatus 正常返回）
//     ⇒ **此后无任何重试路径** ⇒ 永久空态，直到用户手动点刷新
//
// 213 实机的具体数据：`installSyncRefreshPolling` 首拍 lastGen=1（因换代已完成），
// 此后 gen_epoch 恒为 1、rounds 也不再增长 ⇒ onSyncDone 永不调用。
//
// ══════════════════════════════════════════════════════════════════════════
// 本钉防的就是这件事：**状态可达性**（state reachability）
// ——「无论经历多少失败，UI 最终**必须**能到达正确状态」是产品硬需求，
//    不是靠运气。本文件把「失败序列 → 最终状态」列成矩阵逐格断言。
// ══════════════════════════════════════════════════════════════════════════
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.stubEnv('VITE_DESKTOP', '1')

// 用可变 handler 模拟 Go 侧绑定，测试中随时改变其行为
const handlers = {
  authStatus: vi.fn(),
  syncScheduleStatus: vi.fn(),
  syncPaused: vi.fn()
}

vi.mock('@/api/local/desktopApi', () => ({
  authStatus: (...a) => handlers.authStatus(...a),
  syncScheduleStatus: (...a) => handlers.syncScheduleStatus(...a),
  syncPaused: (...a) => handlers.syncPaused(...a)
}))

const {
  startDesktopStatus, useDesktopStatus,
  ensureFresh, __resetDesktopStatusForTest
} = await import('@/api/local/desktopStatus')

// Go 侧健康响应
const OK_AUTH = {
  logged_in: true, online: true, accounting: true,
  profile: true, server_url: 'https://x.example', username: 'testuser'
}
const OK_SCHED = { enabled: true, started: true, rounds: 3, gen_epoch: 1 }

beforeEach(() => {
  vi.useFakeTimers()
  __resetDesktopStatusForTest()
  handlers.authStatus.mockReset().mockResolvedValue(OK_AUTH)
  handlers.syncScheduleStatus.mockReset().mockResolvedValue(OK_SCHED)
  handlers.syncPaused.mockReset().mockResolvedValue(false)
})

afterEach(() => {
  __resetDesktopStatusForTest()
  vi.useRealTimers()
})

/** 推进 fake timer 并放行微任务队列，让 Promise 链跑完。 */
async function advance (ms) {
  await vi.advanceTimersByTimeAsync(ms)
}

describe('桌面状态仓库 · 状态可达性矩阵', () => {
  it('M1 健康路径：启动即取到状态，everLoaded=true', async () => {
    startDesktopStatus()
    await advance(0)
    const s = useDesktopStatus()
    expect(s.auth.value.logged_in).toBe(true)
    expect(s.everLoaded.value).toBe(true)
    expect(s.lastError.value).toBe('')
  })

  // ⭐ 213 实机的精确复现：首屏失败 + 换代早于监听 + 之后 Go 侧恢复正常
  it('M2 ⭐213事故复现：首屏失败后必须靠心跳自愈（不得永久停在未登录）', async () => {
    // 首屏抛错（换代窗口 database is closed）
    handlers.authStatus.mockRejectedValueOnce(new Error('sql: database is closed'))
    startDesktopStatus()
    await advance(0)

    // 此刻应处于"未知"，且**绝不能**被当成"确认未登录"
    let s = useDesktopStatus()
    expect(s.everLoaded.value).toBe(false) // 还没成功取到过

    // Go 侧恢复正常（换代完成）
    handlers.authStatus.mockResolvedValue(OK_AUTH)
    handlers.syncScheduleStatus.mockResolvedValue({ ...OK_SCHED, gen_epoch: 2 })

    // ⭐ 关键：**gen_epoch 从 1 变 2 但绝不会触发任何回调**（事件驱动已废），
    //    唯一出路是 20s 心跳。推进 20s 必须自愈。
    await advance(20000)

    s = useDesktopStatus()
    expect(s.everLoaded.value).toBe(true)
    expect(s.auth.value.logged_in).toBe(true)
    expect(s.lastError.value).toBe('')
  })

  it('M3 连续失败 N 次后仍能自愈（退避不吞掉最终成功）', async () => {
    handlers.authStatus.mockRejectedValue(new Error('bridge not ready'))
    startDesktopStatus()
    await advance(0)

    const s = useDesktopStatus()
    expect(s.everLoaded.value).toBe(false)
    // ⭐ 关键不变式：**失败期间不清空已知状态**（此处本就无已知状态，故保持 false 而非误报已登录）
    expect(s.auth.value.logged_in).not.toBe(true)

    handlers.authStatus.mockResolvedValue(OK_AUTH)
    await advance(20000)
    expect(useDesktopStatus().everLoaded.value).toBe(true)
  })

  // ⭐ Go 侧降级返回（我新加的 status_degraded）
  it('M4 Go 降级返回时不得断言"未登录"，且降级解除后要恢复', async () => {
    // 换代窗口：Go 返回 degraded 而不是抛错
    handlers.authStatus.mockResolvedValue({
      status_degraded: true, status_error: 'Status failed: sql: database is closed',
      profile: false, online: false, accounting: true,
      logged_in: false, relogin_required: false
    })
    startDesktopStatus()
    await advance(0)

    let s = useDesktopStatus()
    // ⭐ 降级 ≠ 未登录：everLoaded 必须仍为 false，UI 才能显示"正在读取"
    expect(s.everLoaded.value).toBe(false)
    expect(s.degraded.value).toBe(true)
    expect(s.lastError.value).toContain('database is closed')

    // 降级解除
    handlers.authStatus.mockResolvedValue(OK_AUTH)
    await advance(20000)
    s = useDesktopStatus()
    expect(s.everLoaded.value).toBe(true)
    expect(s.auth.value.logged_in).toBe(true)
  })

  it('M5 已取到过后偶发降级：保留上次已知状态，不打回未知', async () => {
    startDesktopStatus()
    await advance(0)
    expect(useDesktopStatus().auth.value.logged_in).toBe(true)

    // 偶发降级
    handlers.authStatus.mockResolvedValue({
      status_degraded: true, status_error: 'EnsureTables failed',
      profile: false, online: false, logged_in: false, accounting: false
    })
    await advance(20000)

    const s = useDesktopStatus()
    // ⭐ 核心不变式：一次降级**不得**把已知状态清成"未登录"
    expect(s.everLoaded.value).toBe(true)
    expect(s.lastError.value).toContain('EnsureTables')
  })

  it('M6 心跳无条件：即使 gen_epoch 与 rounds 永远不变也必须刷新', async () => {
    startDesktopStatus()
    await advance(0)

    // 状态变了，但 gen_epoch/rounds 完全不变（事件驱动永远抓不到）
    handlers.authStatus.mockResolvedValue({ ...OK_AUTH, username: 'changed' })
    const before = handlers.authStatus.mock.calls.length
    await advance(20000)
    const after = handlers.authStatus.mock.calls.length

    expect(after).toBeGreaterThan(before) // ⭐ 心跳确实重取了
    expect(useDesktopStatus().auth.value.username).toBe('changed')
  })

  it('M7 并发保护：多路 ensureFresh 只打一轮（换代窗口最怕并发）', async () => {
    startDesktopStatus()
    await advance(0)
    const n = handlers.authStatus.mock.calls.length
    // 同时发起多个 ensureFresh（心跳 + 手动 + 可见性变化可能撞在一起）
    ensureFresh(); ensureFresh(); ensureFresh()
    await advance(0)
    // ⭐ 只允许增加 1 轮（去重生效），而非 3 轮
    expect(handlers.authStatus.mock.calls.length - n).toBeLessThanOrEqual(1)
  })

  it('M8 ensureFresh 显式刷新可立即拿到最新（不必等 20s 心跳）', async () => {
    startDesktopStatus()
    await advance(0)
    handlers.authStatus.mockResolvedValue({ ...OK_AUTH, username: 'fresh' })
    await ensureFresh()
    expect(useDesktopStatus().auth.value.username).toBe('fresh')
  })
})