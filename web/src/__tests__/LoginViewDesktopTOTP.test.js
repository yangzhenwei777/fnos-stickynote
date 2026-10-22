/**
 * LoginView 桌面二因子登录测试（v21.151，生产 Bug2 回归）
 *
 * 背景：桌面壳「登录并同步」走 LoginView.handleDesktopLogin → RemoteLogin 绑定；
 * 2FA 账号第一步返回 {totp_required, totp_token}（无 token）。旧流程把该响应静默当成功
 * （空票）→ 下游 401 → 误报「登录会话已失效（可能在别处登出或被吊销）」。
 * 本测试钉：① 弹验证码框 → RemoteLoginTOTP(url, user, token, code) → 本地会话建立；
 *          ② 取消弹窗不发第二步请求、不报错。
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))
vi.mock('element-plus', () => ({ ElMessageBox: { prompt: vi.fn(), confirm: vi.fn() } }))
vi.mock('@/utils/message', () => ({ message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }))
vi.mock('@/api/local/transport', () => ({ IS_DESKTOP: true }))
vi.mock('@/api/auth', () => ({
  getSetupStatus: vi.fn().mockResolvedValue({ data: { needs_setup: false } }),
  verifyTotp: vi.fn()
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    login: vi.fn().mockResolvedValue({ user: { username: 'alice' } }),
    setAuth: vi.fn(),
    clearAuth: vi.fn()
  })
}))
vi.mock('@/stores/config', () => ({
  useConfigStore: () => ({ siteTitle: 'Sticky note', allowRegister: true, fetchPublicConfig: vi.fn().mockResolvedValue({}) })
}))
vi.mock('@/api/local/desktopApi', () => ({
  remoteLogin: vi.fn(),
  remoteLoginTOTP: vi.fn(),
  validateLoginInput: vi.fn(() => null),
  formatAuthError: vi.fn((e) => String((e && e.message) || e || '操作失败')),
  hardReloadHome: vi.fn()
}))

import LoginView from '../views/LoginView.vue'
import { ElMessageBox } from 'element-plus'
import { remoteLogin, remoteLoginTOTP, hardReloadHome } from '@/api/local/desktopApi'
import { message } from '@/utils/message'

function mountView () {
  return mount(LoginView, {
    global: {
      stubs: {
        'el-alert': { template: '<div class="ea"><slot /></div>' },
        'el-input': {
          props: ['modelValue', 'placeholder'],
          emits: ['update:modelValue'],
          template: '<input class="ei" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
        },
        'el-button': { emits: ['click'], template: '<button class="eb" @click="$emit(\'click\')"><slot /></button>' },
        'el-icon': { template: '<i><slot /></i>' },
        'el-checkbox': { template: '<label><slot /></label>' },
        'router-link': { template: '<a><slot /></a>' }
      }
    }
  })
}

async function fillAndSubmit (w) {
  const byPh = (ph) => w.findAll('.ei').find(i => String(i.attributes('placeholder')).includes(ph))
  await byPh('服务器地址').setValue('http://nas.local:13389')
  await byPh('用户名').setValue('alice')
  await byPh('密码').setValue('pw')
  await w.find('.eb').trigger('click') // 桌面分支主按钮（登录并同步）
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  localStorage.setItem('sn_server_url', 'http://nas.local:13389')
  ElMessageBox.prompt.mockReset()
  remoteLogin.mockReset()
  remoteLoginTOTP.mockReset()
})

describe('LoginView 桌面两步验证（v21.151）', () => {
  it('2FA 账号：弹框收码 → RemoteLoginTOTP → 本地会话建立（不再误报「会话已失效」）', async () => {
    remoteLogin.mockResolvedValueOnce({ totp_required: true, totp_token: 'T-9' })
    remoteLoginTOTP.mockResolvedValueOnce({ server_url: 'http://nas.local:13389', first_sync_done: true })
    ElMessageBox.prompt.mockResolvedValueOnce({ value: '123456' })

    const w = mountView()
    await flushPromises()
    await fillAndSubmit(w)

    expect(ElMessageBox.prompt).toHaveBeenCalledWith(expect.stringContaining('两步验证'), '两步验证', expect.any(Object))
    expect(remoteLoginTOTP).toHaveBeenCalledWith('http://nas.local:13389', 'alice', 'T-9', '123456')
    expect(message.success).toHaveBeenCalledWith('登录成功')
    // ⭐ v21.216（211e 实机·首次登录白屏）**推翻 v21.151 本钉**：
    // 登录成功**不再整页重载**（hardReloadHome），改为 SPA 导航。
    // 推翻理由：v21.212 把登录改回「立即返回 + 异步换代」后，500ms 整页重载
    // 恰好撞进 6~60s 的换代窗口 —— v21.194 时代实证的「重载窗口与 bootstrap
    // 并发时 DOM 空壳」竞态被重新放出 ⇒ 纯白。数据拾取已由 installSyncRefreshPolling
    // （gen_epoch，v21.212 跨进程持久化）接管：换代完成即回调 loadNotes/loadFolders，
    // 整页重载的历史使命（v21.143 的"只刷卡片不刷列表"缺口）已被替代。
    await new Promise((r) => setTimeout(r, 600))
    expect(hardReloadHome).not.toHaveBeenCalled()
  })

  it('取消验证码弹窗 → 不发第二步请求、不报错', async () => {
    remoteLogin.mockResolvedValueOnce({ totp_required: true, totp_token: 'T-9' })
    ElMessageBox.prompt.mockRejectedValueOnce('cancel')

    const w = mountView()
    await flushPromises()
    await fillAndSubmit(w)

    expect(remoteLoginTOTP).not.toHaveBeenCalled()
    expect(message.error).not.toHaveBeenCalled()
  })

  it('非 2FA 账号：一步完成（不弹验证码框）', async () => {
    remoteLogin.mockResolvedValueOnce({ server_url: 'http://nas.local:13389', first_sync_done: true })

    const w = mountView()
    await flushPromises()
    await fillAndSubmit(w)

    expect(ElMessageBox.prompt).not.toHaveBeenCalled()
    expect(remoteLoginTOTP).not.toHaveBeenCalled()
    expect(message.success).toHaveBeenCalledWith('登录成功')
  })
})
