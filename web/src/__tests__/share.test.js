import { describe, it, expect } from 'vitest'
import { shareLink, shareStatus, shareStatusLabel } from '@/utils/share'

describe('2.6.0 分享链接工具', () => {
  it('拼接分享链接（单段路由 + query token）', () => {
    expect(shareLink('abc', 'https://notes.example.com')).toBe('https://notes.example.com/share?t=abc')
  })

  it('状态判定：有效 / 过期 / 已撤销', () => {
    const future = new Date(Date.now() + 86400000).toISOString()
    const past = new Date(Date.now() - 86400000).toISOString()
    expect(shareStatus({ expires_at: future })).toBe('active')
    expect(shareStatus({ expires_at: past })).toBe('expired')
    expect(shareStatus({ expires_at: future, revoked_at: past })).toBe('revoked')
  })

  it('状态文案', () => {
    expect(shareStatusLabel('active')).toBe('有效')
    expect(shareStatusLabel('expired')).toBe('已过期')
    expect(shareStatusLabel('revoked')).toBe('已撤销')
  })
})
