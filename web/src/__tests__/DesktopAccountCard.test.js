/**
 * DesktopAccountCard 两步验证步骤测试（v21.151，生产 Bug2 回归）
 * 场景：2FA 账号登录 → 服务端 {totp_required, totp_token} → 卡片出验证码步骤 →
 *       RemoteLoginTOTP 换正式会话；验证码错误留步重试；临时票过期退回密码步骤。
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

vi.mock('@/api/local/desktopApi', () => ({
  authStatus: vi.fn().mockResolvedValue({ logged_in: false }),
  syncScheduleStatus: vi.fn().mockResolvedValue({}),
  remoteLogin: vi.fn(),
  remoteLoginTOTP: vi.fn(),
  logoutDesktop: vi.fn(),
  bootstrapNow: vi.fn(),
  forceFullResync: vi.fn(),
  // v21.158 · X5：换代窗口只读轮询
  syncPaused: vi.fn().mockResolvedValue(false),
  exportDiagnostics: vi.fn().mockResolvedValue(''),
  formatAuthError: vi.fn((e) => String((e && e.message) || e || '操作失败')),
  validateLoginInput: vi.fn(() => null),
  hardReloadHome: vi.fn(),
  installSyncRefreshPolling: vi.fn(() => vi.fn())
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ login: vi.fn().mockResolvedValue({}) }) }))
vi.mock('@/utils/message', () => ({ message: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock('element-plus', () => ({ ElMessageBox: { confirm: vi.fn() } }))

import DesktopAccountCard from '../components/DesktopAccountCard.vue'
import { remoteLogin, remoteLoginTOTP, formatAuthError } from '@/api/local/desktopApi'
import { message } from '@/utils/message'

function mountCard () {
  return mount(DesktopAccountCard, {
    global: {
      stubs: {
        'el-alert': { template: '<div class="ea"><slot /></div>' },
        'el-input': {
          props: ['modelValue', 'placeholder', 'disabled'],
          emits: ['update:modelValue'],
          template: '<input class="ei" :placeholder="placeholder" :disabled="disabled" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" @keyup.enter="$emit(\'keyup.enter\')" />'
        },
        'el-button': { emits: ['click'], template: '<button class="eb" @click="$emit(\'click\')"><slot /></button>' }
      }
    }
  })
}

// loginToTotpStep 走完第一步：填表 → 登录 → 服务端要求动态码。
async function loginToTotpStep (w) {
  remoteLogin.mockResolvedValueOnce({ totp_required: true, totp_token: 'T-1' })
  const inputs = w.findAll('.ei')
  await inputs[0].setValue('http://nas.local:13389')
  await inputs[1].setValue('alice')
  await inputs[2].setValue('pw-123456')
  await w.findAll('.eb').find(b => b.text() === '登录').trigger('click')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  remoteLogin.mockReset()
  remoteLoginTOTP.mockReset()
  formatAuthError.mockImplementation((e) => String((e && e.message) || e || '操作失败'))
})

describe('DesktopAccountCard 两步验证步骤（v21.151）', () => {
  it('服务端要求动态码 → 出验证码步骤（密码框隐藏），不误报错误', async () => {
    const w = mountCard()
    await flushPromises()
    await loginToTotpStep(w)
    const placeholders = w.findAll('.ei').map(i => String(i.attributes('placeholder')))
    expect(placeholders.some(p => p.includes('动态码'))).toBe(true)
    expect(placeholders).not.toContain('登录密码')
    expect(w.text()).toContain('验证并登录')
    expect(w.text()).toContain('返回重新登录')
    expect(message.info).toHaveBeenCalledWith(expect.stringContaining('两步验证'))
    expect(remoteLoginTOTP).not.toHaveBeenCalled()
  })

  it('提交验证码 → remoteLoginTOTP(baseURL, user, token, code) 且成功收口', async () => {
    const w = mountCard()
    await flushPromises()
    await loginToTotpStep(w)
    remoteLoginTOTP.mockResolvedValueOnce({ server_url: 'http://nas.local:13389', first_sync_done: true })
    await w.find('input[placeholder*="动态码"]').setValue('123456')
    await w.findAll('.eb').find(b => b.text() === '验证并登录').trigger('click')
    await flushPromises()
    expect(remoteLoginTOTP).toHaveBeenCalledWith('http://nas.local:13389', 'alice', 'T-1', '123456')
    expect(message.success).toHaveBeenCalledWith(expect.stringContaining('已登录'))
    expect(w.text()).not.toContain('验证并登录')
  })

  it('验证码错误 → 留在验证码步骤可重试（文案透传）', async () => {
    const w = mountCard()
    await flushPromises()
    await loginToTotpStep(w)
    remoteLoginTOTP.mockRejectedValueOnce(new Error('验证码错误'))
    await w.find('input[placeholder*="动态码"]').setValue('000000')
    await w.findAll('.eb').find(b => b.text() === '验证并登录').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('验证码错误')
    expect(w.find('input[placeholder*="动态码"]').exists()).toBe(true)
  })

  it('临时票过期 → 退回密码步骤重新登录', async () => {
    const w = mountCard()
    await flushPromises()
    await loginToTotpStep(w)
    remoteLoginTOTP.mockRejectedValueOnce(new Error('验证已过期，请重新登录'))
    await w.find('input[placeholder*="动态码"]').setValue('123456')
    await w.findAll('.eb').find(b => b.text() === '验证并登录').trigger('click')
    await flushPromises()
    expect(w.find('input[placeholder*="动态码"]').exists()).toBe(false)
    expect(w.findAll('.ei').map(i => String(i.attributes('placeholder')))).toContain('登录密码')
  })
})
