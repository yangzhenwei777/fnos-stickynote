/**
 * localStorage JSON 加固 +存量自愈测试（v21.195 白屏根修轮）。
 *
 * 事故背景（2026-10-08 绿色版白屏实证）：旧setAuth 以 JSON.stringify(u) 直接
 * setItem('user')，u 为 undefined 时落盘字面量 "undefined"；下次启动 auth store
 * 裸 JSON.parse 抛错 → 根组件渲染中断 → 纯白。二分矩阵：仅删Local Storage 即恢复
 * （dom 3 → 25），删缓存/Preferences/Session Storage 均无效。
 *
 * 覆盖：①readJSON 毒值/坏 JSON 清键回落 ②writeJSON 写侧守卫 ③sanitizeLocalStorage
 * 存量自愈 ④auth store 对毒值不再抛且自愈 ⑤setAuth(undefined) 不落脏值。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('@/api/request', () => ({
  default: { post: vi.fn(), get: vi.fn() },
  setTokens: vi.fn(),
  updateAccessToken: vi.fn(),
  getRefreshToken: vi.fn(() => null),
  clearTokens: vi.fn()
}))

import { readJSON, writeJSON, sanitizeLocalStorage } from '@/utils/storage'
import { useAuthStore } from '@/stores/auth'

describe('utils/storage（v21.195）', () => {
  beforeEach(() => { localStorage.clear() })

  it('readJSON：字面量 "undefined" 视为毒值 → 清键并回落 null（不抛）', () => {
    localStorage.setItem('user', 'undefined')
    expect(readJSON('user')).toBeNull()
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('readJSON：null/NaN/空串同样按毒值处理', () => {
    for (const v of ['null', 'NaN', '']) {
      localStorage.setItem('probe', v)
      expect(readJSON('probe', 'FB')).toBe('FB')
      expect(localStorage.getItem('probe')).toBeNull()
    }
  })

  it('readJSON：非法 JSON → 清键并回落 fallback', () => {
    localStorage.setItem('user', '{broken')
    expect(readJSON('user', { role: 'guest' })).toEqual({ role: 'guest' })
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('readJSON：正常对象原样返回且不删键', () => {
    localStorage.setItem('user', JSON.stringify({ id: 7, username: 'admin' }))
    expect(readJSON('user')).toEqual({ id: 7, username: 'admin' })
    expect(localStorage.getItem('user')).not.toBeNull()
  })

  it('writeJSON：undefined / 不可序列化值 → 删键而非落脏值', () => {
    localStorage.setItem('user', '{"id":1}')
    expect(writeJSON('user', undefined)).toBe(false)
    expect(localStorage.getItem('user')).toBeNull()
    localStorage.setItem('user', '{"id":1}')
    const circular = {}
    circular.self = circular
    expect(writeJSON('user', circular)).toBe(false)
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('writeJSON：正常对象写入成功', () => {
    expect(writeJSON('user', { id: 3 })).toBe(true)
    expect(JSON.parse(localStorage.getItem('user'))).toEqual({ id: 3 })
  })

  it('sanitizeLocalStorage：只清毒值/坏 JSON，正常键与普通字符串键保留', () => {
    localStorage.setItem('user', 'undefined')          // 毒值（真事故形态）
    localStorage.setItem('login_remember', '{oops')     // 已知 JSON 键但坏了
    localStorage.setItem('app_sidebar_width', '280')    // 普通字符串键，合法
    localStorage.setItem('sn_theme', 'dark')            // 普通字符串键，合法
    localStorage.setItem('note_x', 'null')              // 毒值
    const r = sanitizeLocalStorage()
    expect(r.scanned).toBe(5)
    expect(r.removed.sort()).toEqual(['login_remember', 'note_x', 'user'])
    expect(localStorage.getItem('app_sidebar_width')).toBe('280')
    expect(localStorage.getItem('sn_theme')).toBe('dark')
  })
})

describe('stores/auth 抗毒化（v21.195）', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('localStorage.user="undefined" 时 store 初始化不抛、user=null 且键被自愈', () => {
    localStorage.setItem('user', 'undefined')
    let store = null
    expect(() => { store = useAuthStore() }).not.toThrow()
    expect(store.user).toBeNull()
    expect(store.isAuthenticated).toBe(false)
    expect(localStorage.getItem('user')).toBeNull()
  })

  it('setAuth 收到 undefined 时不落脏值（写侧守卫）', () => {
    const store = useAuthStore()
    store.setAuth(null, undefined)
    expect(localStorage.getItem('user')).toBeNull()
    expect(localStorage.getItem('userId')).toBeNull()
    expect(store.user).toBeNull()
  })

  it('setAuth 正常入参：user/userId 正常落盘并可被再次读出', () => {
    const store = useAuthStore()
    store.setAuth({ access: 'a1', refresh: 'r1' }, { id: 9, username: 'admin', role: 'admin' }, true)
    expect(JSON.parse(localStorage.getItem('user'))).toEqual({ id: 9, username: 'admin', role: 'admin' })
    expect(localStorage.getItem('userId')).toBe('9')
    setActivePinia(createPinia())
    expect(useAuthStore().isAdmin).toBe(true)
  })
})
