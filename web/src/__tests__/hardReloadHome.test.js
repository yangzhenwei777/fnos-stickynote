// hardReloadHome 契约测试（v21.140）。
//
// 反向锁定缺陷：会话/库换代后曾用 window.location.reload() —— 它重载的是**当前 URL**，
// 在 /profile 这类深链接页会请求 /profile；桌面资源服务器未命中即 404，
// WebView2 弹"找不到 http://wails.localhost 页"。
// 现在必须**显式回到根路径**（且用 replace 而非 assign，不留历史条目）。
import { describe, it, expect, vi } from 'vitest'
import { hardReloadHome } from '@/api/local/desktopApi'

describe('hardReloadHome 会话换代后回干净起始页', () => {
  it('调到根路径 "/"（不是重载当前路由）', () => {
    const loc = { replace: vi.fn(), reload: vi.fn() }
    hardReloadHome(loc)
    expect(loc.replace).toHaveBeenCalledTimes(1)
    expect(loc.replace).toHaveBeenCalledWith('/')
    expect(loc.reload).not.toHaveBeenCalled()
  })

  it('location 缺失/无 replace 时不抛异常（防御性）', () => {
    expect(() => hardReloadHome(null)).not.toThrow()
    expect(() => hardReloadHome({})).not.toThrow()
  })
})
