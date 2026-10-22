/**
 * useTotpStatus 测试（v21.151，生产 Bug1 回归）
 * 背景：个人中心 TOTP 启用态此前从不加载（刷新后恒「未启用」）→ 用户误以为被关闭。
 * 钉：① 加载真实态 ② 已启用账号必须显示已启用 ③ 拉取失败回退登录缓存值（保守不误显示）
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useTotpStatus } from '@/composables/useTotpStatus'

beforeEach(() => vi.clearAllMocks())

describe('useTotpStatus（v21.151）', () => {
  it('loadTotpStatus 从 /auth/totp-status 拉取并反映真实启用态', async () => {
    const api = { get: vi.fn().mockResolvedValue({ data: { totp_enabled: true } }) }
    const { totpEnabled, loadTotpStatus } = useTotpStatus(api, false)
    expect(totpEnabled.value).toBe(false) // 初始为回退值
    await loadTotpStatus()
    expect(api.get).toHaveBeenCalledWith('/auth/totp-status')
    expect(totpEnabled.value).toBe(true) // Bug1 回归钉：已启用账号必须显示已启用
  })

  it('服务端回 false → 显示未启用（不误报已启用）', async () => {
    const api = { get: vi.fn().mockResolvedValue({ data: { totp_enabled: false } }) }
    const { totpEnabled, loadTotpStatus } = useTotpStatus(api, true)
    await loadTotpStatus()
    expect(totpEnabled.value).toBe(false)
  })

  it('拉取失败 → 保留回退值（登录缓存），宁保守不误显示', async () => {
    const api = { get: vi.fn().mockRejectedValue(new Error('network')) }
    const { totpEnabled, loadTotpStatus } = useTotpStatus(api, true)
    await loadTotpStatus()
    expect(totpEnabled.value).toBe(true)
  })
})
