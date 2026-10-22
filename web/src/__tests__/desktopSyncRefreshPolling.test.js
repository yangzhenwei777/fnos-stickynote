/**
 * v21.153 R-1 通知接线回归：installSyncRefreshPolling 单元三格。
 * 背景：首登自动 Bootstrap 换库完成后前端零通知（192 真机实证列表/卡片停留空态），
 * 该轮询是「同步轮次递增 → 前端重载」的通用信号源；接线正确性由本测试 + L4 走查共同兜底。
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

describe('installSyncRefreshPolling（v21.153 接线）', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => {
    vi.useRealTimers()
    delete window.go
  })

  // ⭐ v21.220 更新（213 实机定罪）：本钉原断言「未递增不触发」，而那正是 213 的病根。
  //
  // 213 实机机制：换代**早于监听建立**完成 ⇒ 首拍 `lastGen` 即等于当前值
  // ⇒ 此后 gen_epoch 与 rounds 都不再变化 ⇒ 回调**永不触发**
  // ⇒ 账号卡片永久停在 `st={}` ⇒ 显示「未登录」而 Go 侧 48 笔记/凭据完好。
  //
  // 新契约：**事件驱动只做加速，无条件心跳（每 4 拍 = 20s）才是可用性保障**。
  it('rounds 递增立即触发；未递增也会心跳触发（20s 兜底，213 事故对策）', async () => {
    let rounds = 1
    window.go = { main: { LocalService: { SyncScheduleStatus: vi.fn(async () => ({ rounds })) } } }
    const { installSyncRefreshPolling } = await import('../api/local/desktopApi')
    const cb = vi.fn()
    const cancel = installSyncRefreshPolling(cb)
    // 第 1~3 拍（15s）：基线采样，未到心跳点，未递增 ⇒ 不触发
    await vi.advanceTimersByTimeAsync(15000)
    expect(cb).not.toHaveBeenCalled()
    // ⭐ 第 4 拍（20s）：rounds 递增 + 心跳点重合 ⇒ **至少触发一次**（可能是 2 次）
    rounds = 2
    await vi.advanceTimersByTimeAsync(5000)
    expect(cb.mock.calls.length).toBeGreaterThanOrEqual(1)
    const afterSpike = cb.mock.calls.length
    // 第 5~7 拍（15s）：无递增、未到心跳 ⇒ 不新增
    await vi.advanceTimersByTimeAsync(15000)
    expect(cb.mock.calls.length).toBe(afterSpike)
    // ⭐ 第 8 拍（40s）：心跳再次触发（213 根治点：状态不变也会刷新）
    await vi.advanceTimersByTimeAsync(5000)
    expect(cb.mock.calls.length).toBeGreaterThan(afterSpike)
    cancel()
    const before = cb.mock.calls.length
    rounds = 3
    await vi.advanceTimersByTimeAsync(20000)
    expect(cb.mock.calls.length).toBe(before) // cancel 后彻底停止
  })

  it('SyncScheduleStatus 抛错不外溢（静默容错）', async () => {
    window.go = { main: { LocalService: { SyncScheduleStatus: vi.fn(async () => { throw new Error('boom') }) } } }
    const { installSyncRefreshPolling } = await import('../api/local/desktopApi')
    const cancel = installSyncRefreshPolling(vi.fn())
    await vi.advanceTimersByTimeAsync(30000)
    cancel()
  })

  it('非桌面环境（无 window.go）安全返回', async () => {
    const { installSyncRefreshPolling } = await import('../api/local/desktopApi')
    const cancel = installSyncRefreshPolling(vi.fn())
    expect(cancel).toBeUndefined()
  })

  // v21.165 回归（204 轮桌面走查定罪）：首装登录走 bootstrap **换代**，换代**不产生
  // 同步轮次** ⇒ 只盯 rounds 的轮询永远看不到变化 ⇒ 前端在换代前渲染的空列表不会刷新
  // （真机：库里已有服务端同步来的笔记，UI 恒显「共 0 条笔记」直到重启）。
  // 本钉锁死「gen_epoch（换代代数）递增同样触发刷新」这一半。
  it('gen_epoch 递增触发回调（换代完成即刷新，不依赖 rounds）', async () => {
    let gen = 1
    const rounds = 5 // rounds 全程不变：模拟"换代不产生同步轮次"
    window.go = {
      main: {
        LocalService: {
          SyncScheduleStatus: vi.fn(async () => ({ rounds, gen_epoch: gen }))
        }
      }
    }
    const { installSyncRefreshPolling } = await import('../api/local/desktopApi')
    const cb = vi.fn()
    const cancel = installSyncRefreshPolling(cb)
    await vi.advanceTimersByTimeAsync(5000) // 首轮采样：基线 gen=1
    expect(cb).not.toHaveBeenCalled()
    gen = 2 // 换代完成 → Go 侧 genEpoch+1
    await vi.advanceTimersByTimeAsync(5000) // 换代代数递增 → 触发
    expect(cb).toHaveBeenCalledTimes(1)
    cancel()
  })

  // ⭐ v21.220：213 实机的**精确复现** —— gen_epoch 在监听建立**之前**就已到位。
  // 旧实现下这条路径永不触发回调；新实现靠 20s 心跳兜住。
  it('⭐213事故复现：gen_epoch 在监听建立前已到位 → 仍必须靠心跳兜住', async () => {
    // 关键：首拍采样到的 gen_epoch 就是最终值（换代早已完成）
    window.go = {
      main: {
        LocalService: {
          SyncScheduleStatus: vi.fn(async () => ({ rounds: 7, gen_epoch: 1 }))
        }
      }
    }
    const { installSyncRefreshPolling } = await import('../api/local/desktopApi')
    const cb = vi.fn()
    const cancel = installSyncRefreshPolling(cb)
    // gen_epoch 与 rounds 永不变 → 事件驱动永不触发
    await vi.advanceTimersByTimeAsync(15000)
    expect(cb).not.toHaveBeenCalled()
    // ⭐ 但心跳必须在 20s 处触发（否则 UI 永久卡死 = 213 实机症状）
    await vi.advanceTimersByTimeAsync(10000)
    expect(cb).toHaveBeenCalledTimes(1)
    cancel()
  })

  it('gen_epoch 缺失（旧版 Go 侧）不报错、不误触发', async () => {
    let rounds = 1
    window.go = {
      main: { LocalService: { SyncScheduleStatus: vi.fn(async () => ({ rounds })) } }
    }
    const { installSyncRefreshPolling } = await import('../api/local/desktopApi')
    const cb = vi.fn()
    const cancel = installSyncRefreshPolling(cb)
    await vi.advanceTimersByTimeAsync(5000)
    rounds = 2
    await vi.advanceTimersByTimeAsync(5000)
    expect(cb).toHaveBeenCalledTimes(1) // 仍按 rounds 正常工作（向后兼容）
    cancel()
  })
})
