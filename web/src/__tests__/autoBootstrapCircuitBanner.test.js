/**
 * A1 自动重建熔断/冷却提示可见性测试（v21.201）。
 *
 * 定罪依据：用户实机 19:01~19:05 反复 bootstrap（g-000002 → g-000003，
 * 两代 watermark 都是 253），而"反复重建"在 UI 完全不可见 → 用户只能靠
 * "多登录几次 + 多次关窗口"反复试。本轮把熔断/冷却状态接到既有
 * SyncScheduleStatus 结果里（零新增绑定），并断言文案含"数据未被删除"。
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const syncPaused = vi.fn()

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

// ⭐ v21.220：卡片不再自己拉状态，改读桌面状态仓库。
// 本钉仍以「调度状态 = 具体熔断/冷却字段」驱动渲染，故把仓库 mock 成透传这两个 fn。
vi.mock('@/api/local/desktopStatus', () => ({
  useDesktopStatus: () => ({
    auth: { value: { logged_in: true, online: true, accounting: true, profile: true, username: 'alice', server_url: 'http://nas:13389' } },
    sched: { value: schedState },
    // paused 是仓库里的只读 ref；本钉用它驱动换代横幅
    paused: { get value () { return pausedState } },
    loading: { value: false },
    everLoaded: { value: true },
    degraded: { value: false },
    lastError: { value: '' },
    ensureFresh: vi.fn().mockResolvedValue(true),
    start: vi.fn()
  }),
  startDesktopStatus: vi.fn(),
  refreshPaused: vi.fn().mockResolvedValue(false)
}))
vi.mock('@/api/local/transport', () => ({ IS_DESKTOP: true }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ login: vi.fn().mockResolvedValue({}) }) }))
vi.mock('@/utils/message', () => ({ message: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock('element-plus', () => ({ ElMessageBox: { confirm: vi.fn() } }))

import DesktopAccountCard from '../components/DesktopAccountCard.vue'

// 仓库内的调度状态与换代暂停态（用例可改写）
let schedState = {}
let pausedState = false

// el-alert 的 title/description 走 props，不进默认插槽；
// 这里渲染成 data-test 标记，便于断言可见性。
function mountCard () {
  return mount(DesktopAccountCard, {
    global: {
      stubs: {
        'el-alert': {
          props: ['title', 'description', 'type'],
          template: '<div class="ea" :data-title="title">{{ description }}</div>'
        },
        'router-link': true
      }
    }
  })
}

const titles = (w) => w.findAll('.ea').map((n) => n.attributes('data-title'))
const descOf = (w, title) => {
  const n = w.findAll('.ea').find((x) => x.attributes('data-title') === title)
  return n ? n.text() : ''
}

beforeEach(() => {
  vi.clearAllMocks()
  // v21.220：仓库 mock 驱动，用例改 schedState / pausedState 即可
  schedState = { enabled: true, rounds: 3 }
  pausedState = false
  syncPaused.mockResolvedValue(false)
})

describe('A1 自动重建熔断/冷却可见化', () => {
  it('熔断时显示「已停止自动重建同步」并说明数据未被删除', async () => {
    schedState = { enabled: true, rounds: 9, auto_bootstrap_tripped: true }
    const w = mountCard()
    await flushPromises()
    expect(titles(w)).toContain('已停止自动重建同步')
    // 关键：必须明确告知数据安全，否则用户不敢点任何按钮
    expect(descOf(w, '已停止自动重建同步')).toContain('笔记没有被删除')
    expect(descOf(w, '已停止自动重建同步')).toContain('强制全量同步')
  })

  it('熔断时不显示冷却提示（两者互斥，避免误导）', async () => {
    schedState = {
      enabled: true, auto_bootstrap_tripped: true, auto_bootstrap_cooldown_sec: 120
    }
    const w = mountCard()
    await flushPromises()
    const t = titles(w)
    expect(t).toContain('已停止自动重建同步')
    expect(t).not.toContain('自动同步处于冷却期')
  })

  it('冷却期显示剩余秒数', async () => {
    schedState = { enabled: true, auto_bootstrap_cooldown_sec: 180 }
    const w = mountCard()
    await flushPromises()
    expect(titles(w)).toContain('自动同步处于冷却期')
    expect(descOf(w, '自动同步处于冷却期')).toContain('180')
  })

  it('正常态（无熔断无冷却）不显示任何提示', async () => {
    const w = mountCard()
    await flushPromises()
    const t = titles(w)
    expect(t).not.toContain('已停止自动重建同步')
    expect(t).not.toContain('自动同步处于冷却期')
  })

  it('cooldown_sec 为非数值时不当成冷却（避免 NaN 秒显示）', async () => {
    schedState = { enabled: true, auto_bootstrap_cooldown_sec: 'abc' }
    const w = mountCard()
    await flushPromises()
    expect(titles(w)).not.toContain('自动同步处于冷却期')
  })

  it('调度器未启动（enabled=false）时熔断状态仍要可见', async () => {
    schedState = {
      enabled: false, reason: '调度器未启动', auto_bootstrap_tripped: true
    }
    const w = mountCard()
    await flushPromises()
    expect(titles(w)).toContain('已停止自动重建同步')
  })

  it('换代暂停横幅与熔断提示是两条独立逻辑（paused=true 时熔断仍可显示）', async () => {
    pausedState = true
    schedState = { enabled: true, auto_bootstrap_tripped: true }
    const w = mountCard()
    await flushPromises()
    const t = titles(w)
    expect(t).toContain('正在初始化全量同步')
    expect(t).toContain('已停止自动重建同步')
  })
})