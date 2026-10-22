/**
 * installGlobalErrorHooks 全局异常钩子测试（v21.194 白屏根治轮）：
 * ①首条异常即经 ReportJSError 落 Go 日志；②异常累计 ≥2 条 → 屏幕出现错误横幅（含重载出口）；
 * ③unhandledrejection 同样计入、卸载后不再捕获；④ReportJSError 通道缺失不抛错。
 * 真机实证（2026-10-08）：JS 层失败此前表现为纯白屏、零取证通道。
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'

const reportSpy = vi.fn()

describe('installGlobalErrorHooks（v21.194）', () => {
  let hooks
  let cancel

  beforeEach(async () => {
    if (cancel) { cancel(); cancel = null }
    document.body.innerHTML = ''
    document.getElementById('sn-error-banner')?.remove()
    window.__snErrHooksInstalled = false
    reportSpy.mockClear()
    window.go = { main: { LocalService: { ReportJSError: reportSpy } } }
    hooks = await import('../api/local/desktopApi')
  })

  it('首条异常 → ReportJSError 上报，无横幅', () => {
    cancel = hooks.installGlobalErrorHooks()
    window.dispatchEvent(new ErrorEvent('error', { message: 'boom-1' }))
    expect(reportSpy).toHaveBeenCalledTimes(1)
    expect(reportSpy.mock.calls[0][0]).toContain('boom-1')
    expect(document.getElementById('sn-error-banner')).toBeNull()
  })

  it('异常累计 ≥2 → 屏幕出现错误横幅（重载出口）', () => {
    cancel = hooks.installGlobalErrorHooks()
    window.dispatchEvent(new ErrorEvent('error', { message: 'boom-1' }))
    window.dispatchEvent(new ErrorEvent('error', { message: 'boom-2' }))
    const bar = document.getElementById('sn-error-banner')
    expect(bar).not.toBeNull()
    expect(bar.textContent).toContain('重载')
    expect(reportSpy).toHaveBeenCalledTimes(2)
  })

  it('unhandledrejection 同样计入；卸载后不再捕获', () => {
    cancel = hooks.installGlobalErrorHooks()
    const stray = Promise.reject(new Error('rej-x'))
    stray.catch(() => { /* 构造 rejection 的伴生 catch，防泄漏 */ })
    window.dispatchEvent(new PromiseRejectionEvent('unhandledrejection', {
      promise: stray, reason: new Error('rej-1')
    }))
    expect(reportSpy).toHaveBeenCalledTimes(1)
    expect(reportSpy.mock.calls[0][0]).toContain('rej-1')
    cancel()
    cancel = null
    window.dispatchEvent(new ErrorEvent('error', { message: 'after-cancel' }))
    expect(reportSpy).toHaveBeenCalledTimes(1)
  })

  it('ReportJSError 通道缺失（Web 构建）不抛错', () => {
    delete window.go
    expect(() => hooks.installGlobalErrorHooks()).not.toThrow()
    window.dispatchEvent(new ErrorEvent('error', { message: 'web-err' }))
  })
})
