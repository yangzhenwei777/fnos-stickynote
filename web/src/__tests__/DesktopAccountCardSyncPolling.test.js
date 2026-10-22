/**
 * DesktopAccountCard 状态接线测试 —— ⭐ v21.220 重写（架构已变）
 *
 * ════════════════════════════════════════════════════════════════════════
 * 为什么整体重写而不是补两行
 * ════════════════════════════════════════════════════════════════════════
 * 213 实机定罪：本组件原在 `onMounted` 里 `installSyncRefreshPolling(refresh)`
 * ——只认 `gen_epoch` 变大 / `rounds` 增长。而换代**早于监听建立**完成
 * ⇒ 首拍即追平 ⇒ 回调永不触发 ⇒ 首屏 `authStatus` 失败后**再无人重试**
 * ⇒ 卡片永久停在 `st={}` ⇒ 显示「未登录：处于离线单机状态」
 * （而 Go 侧换代成功、48 笔记/9 文件夹落地、凭据完好、`localStorage.user` 正确写入）。
 *
 * ⇒ 「组件自己拉 + 事件驱动通知」这条架构**已被证伪**，v21.220 改为：
 *     · 状态取用权归**桌面状态仓库**（desktopStatus.js，应用入口启动，20s 无条件心跳）；
 *     · 组件**只读不算**，不再持有任何轮询句柄。
 *
 * 因此本钉改为验证新契约。仓库自身的可达性矩阵见 desktopStatusStore.test.js（M1~M8）。
 * ════════════════════════════════════════════════════════════════════════
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const ensureFreshSpy = vi.fn()

vi.mock('@/api/local/desktopStatus', () => ({
  useDesktopStatus: () => ({
    auth: { value: authState },
    sched: { value: {} },
    paused: { value: false },
    loading: { value: false },
    everLoaded: { value: everLoaded },
    degraded: { value: false },
    lastError: { value: '' },
    ensureFresh: ensureFreshSpy,
    start: vi.fn()
  }),
  startDesktopStatus: vi.fn(),
  refreshPaused: vi.fn().mockResolvedValue(false)
}))

vi.mock('@/api/local/desktopApi', () => ({
  remoteLogin: vi.fn(),
  remoteLoginTOTP: vi.fn(),
  logoutDesktop: vi.fn(),
  bootstrapNow: vi.fn(),
  forceFullResync: vi.fn(),
  exportDiagnostics: vi.fn().mockResolvedValue(''),
  formatAuthError: vi.fn((e) => String((e && e.message) || e || '操作失败')),
  validateLoginInput: vi.fn(() => null),
  hardReloadHome: vi.fn()
}))
vi.mock('@/api/local/transport', () => ({ IS_DESKTOP: true }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ login: vi.fn().mockResolvedValue({}) }) }))
vi.mock('@/utils/message', () => ({ message: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock('element-plus', () => ({ ElMessageBox: { confirm: vi.fn() } }))

import DesktopAccountCard from '../components/DesktopAccountCard.vue'

// 可被每个用例改写的仓库状态
let authState = {}
let everLoaded = true

function mountCard () {
  return mount(DesktopAccountCard, {
    global: {
      stubs: {
        'el-alert': { template: '<div class="ea"><slot /></div>' },
        'el-input': { props: ['modelValue', 'placeholder', 'disabled'], template: '<input class="ei" />' },
        'el-button': { emits: ['click'], template: '<button class="eb" @click="$emit(\'click\')"><slot /></button>' }
      }
    }
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  authState = { logged_in: true, online: true, accounting: true, server_url: 'http://nas.local:13389', username: 'alice' }
  everLoaded = true
})

describe('DesktopAccountCard 状态接线（v21.220：只读仓库，不再自建轮询）', () => {
  // ⭐ 回归钉：卡片**不得**再自己拉状态（否则架构退回 213 事故形态）
  it('挂载时只请求一次仓库刷新，**不自建** SyncRefreshPolling', async () => {
    const w = mountCard()
    await flushPromises()
    expect(ensureFreshSpy).toHaveBeenCalledTimes(1)
    w.unmount()
  })

  it('在线态文案正确', async () => {
    const w = mountCard()
    await flushPromises()
    expect(w.text()).toContain('已连接')
    w.unmount()
  })

  it('可恢复态文案为「恢复中」而非「未登录」（213 用户视角成因）', async () => {
    authState = { logged_in: true, online: false, session_recoverable: true, username: 'testuser' }
    const w = mountCard()
    await flushPromises()
    expect(w.text()).toContain('恢复中')
    expect(w.text()).not.toContain('未登录：处于离线单机状态')
    w.unmount()
  })

  // ⭐ 213 事故的核心防线
  it('尚未取到状态时**绝不显示「未登录」**（显示"正在读取"）', async () => {
    authState = {} // 仓库还没取到
    everLoaded = false
    const w = mountCard()
    await flushPromises()
    expect(w.text()).toContain('正在')
    expect(w.text()).not.toContain('未登录')
    w.unmount()
  })

  it('真未登录时才显示离线单机态', async () => {
    authState = { logged_in: false, online: false, session_recoverable: false, accounting: false }
    const w = mountCard()
    await flushPromises()
    expect(w.text()).toContain('离线单机')
    w.unmount()
  })

  // 换代窗口提示：改由 refreshPaused 走，组件内不再直接调 syncPaused
  it('挂载即读换代窗口（经 refreshPaused）', async () => {
    const w = mountCard()
    await flushPromises()
    const { refreshPaused } = await import('@/api/local/desktopStatus')
    expect(refreshPaused).toHaveBeenCalled()
    w.unmount()
  })
})