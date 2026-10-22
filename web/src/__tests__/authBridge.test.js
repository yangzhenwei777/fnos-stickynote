// 桌面登录入口契约测试（批次 8-Alt 刀 6）。
// 钉三件真缺陷的修复：① 登录不再"任意密码都成功"（必须走 RemoteLogin 且失败可读）；
// ② 桌面退出登录必须真的调用 Go Logout（此前 /auth/logout 是空映射，DPAPI 封存票留在盘上）；
// ③ 账密错与会话吊销必须分流（Go 已分哨兵，前端映射不得再混）。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import * as desktopApi from '@/api/local/desktopApi'

const F = () => window.go.main.LocalService

beforeEach(() => {
  window.go = { main: { LocalService: {
    AuthStatus: vi.fn().mockResolvedValue({ logged_in: true, server_url: 'http://nas:13389', username: 'admin' }),
    RemoteLogin: vi.fn().mockResolvedValue({ device_id: 'd1', server_url: 'http://nas:13389', username: 'admin' }),
    Logout: vi.fn().mockResolvedValue(undefined),
    BootstrapNow: vi.fn().mockResolvedValue({ generation: 'g-000002' })
  } } }
})
afterEach(() => { delete window.go })

describe('登录入参预校验（与 Go 侧同规则，双端各挡一次）', () => {
  it('空值/畸形地址给出可直接上屏的提示', () => {
    expect(desktopApi.validateLoginInput('', 'admin', 'pw123456')).toContain('都要填')
    expect(desktopApi.validateLoginInput('http://a:1', '', 'pw123456')).toContain('都要填')
    expect(desktopApi.validateLoginInput('http://a:1', 'admin', '')).toContain('都要填')
    expect(desktopApi.validateLoginInput('192.0.2.10:13389', 'admin', 'pw')).toContain('http://')
    expect(desktopApi.validateLoginInput('ftp://nas', 'admin', 'pw')).toContain('http://')
    expect(desktopApi.validateLoginInput('https://', 'admin', 'pw')).toBeTruthy()
  })
  it('合法入参放行', () => {
    expect(desktopApi.validateLoginInput('http://192.0.2.10:13389', 'admin', 'pw123456')).toBe(null)
    expect(desktopApi.validateLoginInput('https://dav.example.com', 'admin', 'pw123456')).toBe(null)
  })
})

describe('登录/登出绑定封装', () => {
  it('remoteLogin 按 (baseURL, username, password) 顺序透传并 trim 前后空格', async () => {
    await desktopApi.remoteLogin('  http://nas:13389  ', '  admin ', 'pw123456')
    expect(F().RemoteLogin).toHaveBeenCalledWith('http://nas:13389', 'admin', 'pw123456')
  })

  it('密码为空也照样透传给 Go（由 Go 兜底拒绝，前端不做"空密码悄悄成功"）', async () => {
    await desktopApi.remoteLogin('http://nas:13389', 'admin', '')
    expect(F().RemoteLogin).toHaveBeenCalledWith('http://nas:13389', 'admin', '')
  })

  it('authStatus / logoutDesktop / bootstrapNow 命中正确绑定名', async () => {
    await desktopApi.authStatus()
    await desktopApi.logoutDesktop()
    await desktopApi.bootstrapNow()
    expect(F().AuthStatus).toHaveBeenCalledTimes(1)
    expect(F().Logout).toHaveBeenCalledTimes(1)
    expect(F().BootstrapNow).toHaveBeenCalledTimes(1)
  })

  it('无桥时四个入口全部显式报错，绝不静默成功', async () => {
    delete window.go
    await expect(desktopApi.authStatus()).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
    await expect(desktopApi.remoteLogin('http://a', 'u', 'p')).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
    await expect(desktopApi.logoutDesktop()).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
    await expect(desktopApi.bootstrapNow()).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
  })
})

describe('登录错误文案分流（缺陷 3 的前端侧）', () => {
  it('账密错 / 会话吊销 / 锁定 / 连不上 各说各话', () => {
    const cases = [
      ['BAD_CREDENTIALS: {"error":"用户名或密码错误"}', '用户名或密码错误'],
      ['AUTH_REVOKED', '登录会话已失效'],
      ['sync http 429: 尝试次数过多', '登录尝试次数过多'],
      ['sync transport: Post "http://nas/api/auth/login": dial tcp 192.0.2.10:13389: connectex refused',
        '连不上这台服务器'],
      ['DESKTOP_BRIDGE_UNAVAILABLE', '桌面桥不可用']
    ]
    for (const [go, kw] of cases) {
      expect(desktopApi.formatAuthError(new Error(go)), go).toContain(kw)
    }
  })

  it('账密错绝不能被翻成"会话失效"（否则用户会去做无意义的重新登录）', () => {
    const msg = desktopApi.formatAuthError(new Error('BAD_CREDENTIALS: x'))
    expect(msg).not.toContain('吊销')
    expect(msg).not.toContain('失效')
  })

  it('v21.152：NO_REFRESH（凭据失效）必须给可行动文案，不得裸抛哨兵（生产实测弹裸 NO_REFRESH）', () => {
    expect(desktopApi.formatAuthError(new Error('NO_REFRESH'))).toContain('重新登录')
    expect(desktopApi.formatAuthError(new Error('同步引擎未挂载'))).not.toContain('NO_REFRESH')
    // Go 新文案（ErrNeedRelogin）原样可用
    expect(desktopApi.formatAuthError(new Error('本机登录凭据已失效，请重新登录'))).toContain('本机登录凭据已失效')
  })

  it('未知错误原样透传，禁吞错', () => {
    expect(desktopApi.formatAuthError(new Error('DPAPI 不可用：ERROR_BAD_KEY')))
      .toBe('DPAPI 不可用：ERROR_BAD_KEY')
    expect(desktopApi.formatAuthError(null)).toBe('操作失败')
  })
})
