/**
 * withTimeout 单元测试（v21.200 · 症状③「登录按钮一直转圈圈」的修复钉）
 *
 * 背景：Go 侧 remoteLogin/bootstrapNow 是同步阻塞调用，前端 await 期间按钮
 * 永久 loading。withTimeout 给这些调用加硬上限，保证 UI 一定解除等待。
 *
 * 三条硬要求：
 *  ① 正常路径（Promise 在上限内resolve）必须原样透传值；
 *  ② 超时必须 reject 且文案说明「后台可能仍在继续」；
 *  ③ 定时器必须清理（不泄漏，否则长跑会累积定时器）。
 */
import { describe, it, expect, vi } from 'vitest'
import { withTimeout, DEFAULT_TIMEOUT_MS } from '@/utils/withTimeout'

describe('withTimeout（v21.200 长耗时绑定调用上限）', () => {
  it('默认上限为 120s（= Go 侧 pollTimeout 90s + 30s 余量）', () => {
    expect(DEFAULT_TIMEOUT_MS).toBe(120000)
  })

  it('正常路径原样透传值', async () => {
    const v = await withTimeout(Promise.resolve('ok'), 1000, '登录')
    expect(v).toBe('ok')
  })

  it('正常路径（含异步 resolve）同样透传', async () => {
    const v = await withTimeout(
      new Promise(res => setTimeout(() => res('async-ok'), 20)),
      1000, '登录'
    )
    expect(v).toBe('async-ok')
  })

  it('超时会 reject，且文案含动作名与「不要重复点击」提示', async () => {
    const never = new Promise(() => {}) // 永不 settle
    let err = null
    try {
      await withTimeout(never, 60, '登录')
    } catch (e) { err = e }
    expect(err).not.toBeNull()
    expect(err.code).toBe('DESKTOP_CALL_TIMEOUT')
    expect(err.message).toContain('登录')
    expect(err.message).toContain('不要重复点击')
    expect(err.message).toContain('后台可能仍在继续')
  })

  it('超时上限正确生效（60ms 上限不会真的等 60s）', async () => {
    const t0 = Date.now()
    await expect(withTimeout(new Promise(() => {}), 60, '导出诊断包')).rejects.toThrow()
    expect(Date.now() - t0).toBeLessThan(3000)
  })

  it('原 Promise 失败时原样透传错误（不被误包装成超时）', async () => {
    let err = null
    try {
      await withTimeout(Promise.reject(new Error('账号或密码错误')), 1000, '登录')
    } catch (e) { err = e }
    expect(err.message).toBe('账号或密码错误')
    expect(err.code).toBeUndefined()
  })

  it('定时器被清理（不泄漏）', async () => {
    const spy = vi.spyOn(globalThis, 'clearTimeout')
    await withTimeout(Promise.resolve('x'), 1000, '登录')
    expect(spy).toHaveBeenCalled()
    spy.mockRestore()
  })
})