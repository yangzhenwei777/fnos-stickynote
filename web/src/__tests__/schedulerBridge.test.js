// 后台同步调度器的前后端接口语义（批次 8-Alt 刀 3）——ADR-005 §二.4 触发器集合的契约面。
// 钉住：回前台/拿到焦点各敲一次唤醒、页面隐藏时**不敲**（最小化不该产生同步流量）、
// 重复安装不叠加监听、桥不可用时静默（它只是加速器，不是正确性依赖）。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import * as desktopApi from '@/api/local/desktopApi'

const tick = () => new Promise((r) => setTimeout(r, 0))

let cleanup = null // 每个用例装完把卸载函数交回，afterEach 统一摘（失败中途也不污染下一个用例）

beforeEach(() => {
  window.go = { main: { LocalService: {
    NotifyResume: vi.fn().mockResolvedValue({ ok: true }),
    SyncScheduleStatus: vi.fn().mockResolvedValue({ enabled: true, rounds: 3, offline: false })
  } } }
  Object.defineProperty(document, 'visibilityState', { configurable: true, writable: true, value: 'visible' })
})
afterEach(() => {
  if (cleanup) { cleanup(); cleanup = null }
  delete window.go
})

describe('调度器前端接口', () => {
  it('syncScheduleStatus / notifyResume 透传绑定', async () => {
    const st = await desktopApi.syncScheduleStatus()
    expect(st.rounds).toBe(3)
    expect(window.go.main.LocalService.SyncScheduleStatus).toHaveBeenCalledTimes(1)
    await desktopApi.notifyResume()
    expect(window.go.main.LocalService.NotifyResume).toHaveBeenCalledTimes(1)
  })

  it('无桥时两个入口都明确报 DESKTOP_BRIDGE_UNAVAILABLE（禁静默假成功）', async () => {
    delete window.go
    await expect(desktopApi.syncScheduleStatus()).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
    await expect(desktopApi.notifyResume()).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
  })

  it('可见→敲一次；隐藏→不敲；重复安装不叠加监听', async () => {
    const un = desktopApi.installDesktopResumeTrigger()
    cleanup = un
    expect(desktopApi.desktopResumeTriggerInstalled()).toBe(true)
    desktopApi.installDesktopResumeTrigger() // 第二次必须是空操作

    document.visibilityState = 'visible'
    document.dispatchEvent(new Event('visibilitychange'))
    await tick()
    expect(window.go.main.LocalService.NotifyResume).toHaveBeenCalledTimes(1)

    document.visibilityState = 'hidden'
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('focus'))
    await tick()
    expect(window.go.main.LocalService.NotifyResume).toHaveBeenCalledTimes(1) // 隐藏期一律不敲

    un()
    expect(desktopApi.desktopResumeTriggerInstalled()).toBe(false)
    document.visibilityState = 'visible'
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('focus'))
    await tick()
    expect(window.go.main.LocalService.NotifyResume).toHaveBeenCalledTimes(1) // 卸载后彻底安静
  })

  it('桥抛错不得冒泡成用户可见错误（唤醒失败下一轮定时器会补）', async () => {
    window.go.main.LocalService.NotifyResume = vi.fn().mockRejectedValue(new Error('boom'))
    const un = desktopApi.installDesktopResumeTrigger()
    cleanup = un
    const errs = []
    const onRej = (e) => errs.push(e)
    window.addEventListener('unhandledrejection', onRej)
    document.visibilityState = 'visible'
    window.dispatchEvent(new Event('focus'))
    await tick(); await tick()
    window.removeEventListener('unhandledrejection', onRej)
    un()
    expect(errs).toHaveLength(0)
  })
})
