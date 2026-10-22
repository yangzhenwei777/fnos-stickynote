/**
 * SyncInitBanner · 换代/全量同步窗口提示（v21.166 计划项 2 / X5 延伸）。
 *
 * 定罪依据：首登 Bootstrap 换代实测 10~60s 波动，期间调度闸门关闭、列表暂空。
 * 此前只有「账号与设备」卡片有横幅，用户停在列表页只见空列表 → 易误判为
 * 205 刚根治的「首装列表恒空」复发并重复触发强制同步。本轮把该提示抽成
 * 可复用组件挂到列表页，并加"超时升级为可行动提示"。
 *
 * 覆盖：
 *  A1 闸门关 → 横幅可见，文案含"正在初始化全量同步"
 *  A2 闸门开 → 横幅隐藏（换代完成后自动消失）
 *  A3 超时 → type 升级 warning + 文案含"导出诊断包"（可行动出路）
 *  A4 桥不可用/抛错 → 静默隐藏，不打扰 Web 端用户
 *  A5 桥不可用（desktopBridgeAvailable=false）→ onMounted 不启动轮询
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const syncPaused = vi.fn()
const desktopBridgeAvailable = vi.fn(() => true)
const bootstrapStatus = vi.fn(() => null)

vi.mock('@/api/local/desktopApi', () => ({
  syncPaused: (...a) => syncPaused(...a),
  desktopBridgeAvailable: (...a) => desktopBridgeAvailable(...a),
  bootstrapStatus: (...a) => bootstrapStatus(...a)
}))

import SyncInitBanner from '../components/SyncInitBanner.vue'

const alertStub = {
  props: ['title', 'description', 'type'],
  template: `<div class="alert-stub" :data-type="type">{{ title }}|{{ description }}</div>`
}

function mountBanner (props = {}) {
  return mount(SyncInitBanner, {
    props,
    global: { stubs: { 'el-alert': alertStub } }
  })
}

describe('SyncInitBanner · 换代窗口提示', () => {
  beforeEach(() => {
    syncPaused.mockReset()
    desktopBridgeAvailable.mockReset()
    bootstrapStatus.mockReset()
    desktopBridgeAvailable.mockReturnValue(true)
    bootstrapStatus.mockReturnValue(null)
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('A1 闸门关闭时显示横幅且文案为「正在初始化全量同步」', async () => {
    syncPaused.mockResolvedValue(true)
    const w = mountBanner()
    await flushPromises()
    expect(w.find('.alert-stub').exists()).toBe(true)
    expect(w.find('.alert-stub').text()).toContain('正在初始化全量同步')
    expect(w.find('.alert-stub').attributes('data-type')).toBe('info')
  })

  it('A2 闸门打开后横幅隐藏（换代完成自动消失）', async () => {
    syncPaused.mockResolvedValueOnce(true).mockResolvedValue(false)
    const w = mountBanner()
    await flushPromises()
    expect(w.find('.alert-stub').exists()).toBe(true)

    // 推进一个轮询周期（默认 2000ms）
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(w.find('.alert-stub').exists()).toBe(false)
  })

  it('A3 超过阈值后升级为可行动提示（warning + 导出诊断包出路）', async () => {
    syncPaused.mockResolvedValue(true)
    const w = mountBanner({ thresholdMs: 1000 })
    await flushPromises()
    expect(w.find('.alert-stub').attributes('data-type')).toBe('info')

    vi.advanceTimersByTime(1500)
    await flushPromises()
    // 需再触发一次 poll 让 slow 计算随时间重算（slow 依赖 Date.now，渲染更新靠 poll 驱动）
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(w.find('.alert-stub').attributes('data-type')).toBe('warning')
    expect(w.find('.alert-stub').text()).toContain('导出诊断包')
  })

  it('A4 桥抛错时静默隐藏，不打扰用户', async () => {
    syncPaused.mockRejectedValue(new Error('bridge down'))
    const w = mountBanner()
    await flushPromises()
    expect(w.find('.alert-stub').exists()).toBe(false)
  })

  it('A5 桥不可用（Web 端）时不启动轮询', async () => {
    desktopBridgeAvailable.mockReturnValue(false)
    syncPaused.mockResolvedValue(true)
    const w = mountBanner()
    await flushPromises()
    expect(syncPaused).not.toHaveBeenCalled()
    expect(w.find('.alert-stub').exists()).toBe(false)
  })

  it('A6 onUnmounted 清理定时器，避免视图切换后泄漏', async () => {
    syncPaused.mockResolvedValue(true)
    const w = mountBanner()
    await flushPromises()
    const before = syncPaused.mock.calls.length
    w.unmount()
    vi.advanceTimersByTime(10000)
    await flushPromises()
    expect(syncPaused.mock.calls.length).toBe(before)
  })

  it('A7 窗口内有阶段数据 → 文案含阶段名与已等待秒数（2.0.3 P1-3）', async () => {
    syncPaused.mockResolvedValue(true)
    bootstrapStatus.mockResolvedValue({ phase: 'download', since_ms: Date.now() - 7000 })
    const w = mountBanner()
    await flushPromises()
    expect(w.find('.alert-stub').text()).toContain('正在下载快照')
    // 等待 5 秒后心跳/轮询驱动重算 → 出现「已等待」
    vi.advanceTimersByTime(5000)
    await flushPromises()
    expect(w.find('.alert-stub').text()).toMatch(/已等待 [1-9]\d* 秒/)
  })

  it('A8 旧桥无 BootstrapStatus 绑定（reject）→ 静默降级为纯窗口提示', async () => {
    syncPaused.mockResolvedValue(true)
    bootstrapStatus.mockRejectedValue(new Error('DESKTOP_BRIDGE_UNAVAILABLE'))
    const w = mountBanner()
    await flushPromises()
    const txt = w.find('.alert-stub').text()
    expect(txt).toContain('正在初始化全量同步')
    expect(txt).not.toContain('快照')
    expect(w.find('.alert-stub').exists()).toBe(true)
  })
})