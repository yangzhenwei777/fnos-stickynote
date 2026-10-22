// spaRedirect 契约测试（v21.141）。
//
// 反向锁定本次真实回归：桌面侧深链接不能整页重载到真实路径 —— Wails 只在路径以 "/" 或
// "/index.html" 结尾时注入 runtime（window.go 桥的唯一来源），整页加载 /login 会得到
// **没有桥**的页面 → 一切绑定调用报「桌面桥不可用」→ 退出登录后无法再登录。
// 因此 Go 侧 302 到 /?_spa=...，本模块必须用**客户端导航**还原，绝不触发文档重载。
import { describe, it, expect, vi } from 'vitest'
import { readSpaRedirect, installSpaRedirectRestore } from '@/api/local/spaRedirect'

describe('readSpaRedirect 解析与防开放重定向', () => {
  it('合法站内路径 → 原样返回', () => {
    expect(readSpaRedirect('?_spa=%2Flogin')).toBe('/login')
    expect(readSpaRedirect('?a=1&_spa=%2Fadmin-backup')).toBe('/admin-backup')
  })

  it('缺参数/非法值 → null（含 "//host" 与外域，防开放重定向）', () => {
    for (const s of ['', '?x=1', '?_spa=', '?_spa=login', '?_spa=//evil.com', '?_spa=https%3A%2F%2Fevil.com']) {
      expect(readSpaRedirect(s), `search=${s}`).toBeNull()
    }
  })
})

describe('installSpaRedirectRestore 用客户端导航还原（绝不整页重载）', () => {
  it('有 _spa → replaceState 清 query + router.replace 还原', () => {
    const router = { replace: vi.fn() }
    const history = { replaceState: vi.fn() }
    vi.stubGlobal('window', { history })
    const loc = { search: '?_spa=%2Fprofile' }

    const got = installSpaRedirectRestore(router, loc)

    expect(got).toBe('/profile')
    expect(history.replaceState).toHaveBeenCalledWith(null, '', '/profile')
    expect(router.replace).toHaveBeenCalledWith('/profile')
    vi.unstubAllGlobals()
  })

  it('无 _spa → 什么都不做（不碰 history / router）', () => {
    const router = { replace: vi.fn() }
    const history = { replaceState: vi.fn() }
    vi.stubGlobal('window', { history })

    expect(installSpaRedirectRestore(router, { search: '' })).toBeNull()
    expect(router.replace).not.toHaveBeenCalled()
    expect(history.replaceState).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })

  it('router/location 缺失时不抛异常', () => {
    expect(() => installSpaRedirectRestore(null, null)).not.toThrow()
  })
})
