// desktop401.test.js —— ⭐ v21.218 桌面端 401 处置契约钉。
//
// 定罪（212 实机，4 次启动全部命中）：
//   Go 侧每次启动都 `[auth] device session restored`（DPAPI 解封成功，logged_in=true），
//   而 UI 显示未登录、笔记空白、反复重启最终仍未登录。
//
// 机制：request.js 的 401 拦截器执行「尝试 refresh → 失败 → 清 user → replace('/login')」。
//   桌面端 `/auth/refresh` 在 LocalTransport 里是**返回空对象的桩**（设备会话由 Go 侧
//   DPAPI 封存管理，桌面没有 cookie/refresh 语义）⇒ refresh **必然失败**
//   ⇒ **必然走到最后一步把用户踢回登录页**。
//   配合 router 对账的静默失败（桥未就绪即跳过 / catch{} 吞错 / 已对账标志永不复位）
//   ⇒ 整个进程永不自愈。**这就是近 50 版修不好的底层逻辑问题：Web 端鉴权语义被硬搬到了桌面。**
//
// v21.218 契约：桌面端 401 **不自动跳转、不静默清态**，只弹一次确认框，
//   用户确认后才清 Go 侧封存凭据并导航。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

const confirmCalls = []
let confirmResult = true
let logoutCalls = 0

// ⭐ IS_DESKTOP 由 `import.meta.env.VITE_DESKTOP === '1'` 决定（transport.js），
// 而 vitest 环境下默认加载的是 `.env`（无 desktop 键）⇒ 恒为 false。
// 故必须**在 import 被求值之前** stub 环境变量，否则 request.js 会走 Web 端分支。
vi.stubEnv('VITE_DESKTOP', '1')

// mock message：confirm 记录调用并返回可控结果
vi.mock('@/utils/message', () => ({
  message: {
    success: vi.fn(),
    error: vi.fn(),
    errorOnce: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
    confirm: vi.fn((msg, opts) => {
      confirmCalls.push({ msg, opts })
      return Promise.resolve(confirmResult)
    })
  }
}))

// mock transport：记录 Logout 调用（不翻转 IS_DESKTOP —— 那由 stubEnv 负责）
vi.mock('@/api/local/transport', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    clearDesktopCredentials: vi.fn(async () => { logoutCalls++ })
  }
})

const { default: api, __resetDesktop401GateForTest } = await import('@/api/request')
const { IS_DESKTOP } = await import('@/api/local/transport')
const { message } = await import('@/utils/message')

// 造一个必然 401 的请求：直接给 api 实例装一个抛 401 的 adapter，
// 这样走的是**真实响应拦截器**（响应拦截器在 adapter 之后执行）。
//
// ⚠ jsdom 的 window.location 是只读访问器但**可赋值**（现代 jsdom 允许整体替换），
// 不能用 defineProperty 逐字段改 —— 那会抛 "not implemented: navigation"。
// 若某环境的 jsdom 禁赋值，则退化为「只断言弹窗、不断言跳转」。
let locationSwappable = true
function setLocation (pathname, replaceSpy) {
  try {
    window.location = { pathname, replace: replaceSpy || vi.fn(), href: '' }
    locationSwappable = true
  } catch {
    locationSwappable = false
  }
  return locationSwappable
}

function apiWith401 () {
  const inst = api
  const orig = inst.defaults.adapter
  inst.defaults.adapter = async (config) => {
    throw Object.assign(new Error('unauthorized'), {
      config,
      response: { status: 401, data: { error: 'AUTH_REVOKED' }, statusText: '', headers: {}, config }
    })
  }
  return {
    fire: (url = '/notes') => inst.get(url),
    restore: () => { inst.defaults.adapter = orig }
  }
}

// 401 → 弹窗链完全跑完（含 await 链）
async function drain () {
  for (let i = 0; i < 5; i++) await new Promise(r => setTimeout(r, 10))
}

describe('桌面端 401 处置（v21.218）', () => {
  beforeEach(() => {
    confirmCalls.length = 0
    logoutCalls = 0
    confirmResult = true
    window.localStorage.clear()
    window.sessionStorage.clear()
    // ⭐ 模块级去重闸门必须复位，否则前一个用例置 true 后本用例弹窗被静默吞掉
    __resetDesktop401GateForTest()
    setLocation('/notes')
  })
  afterEach(() => {
    __resetDesktop401GateForTest()
    vi.clearAllMocks()
  })

  it('前提：测试环境确实是桌面构建（否则下面全部走 Web 端分支，钉不住任何东西）', () => {
    expect(IS_DESKTOP).toBe(true)
  })

  it('桌面端 401 弹确认框；确认前不清态、确认后才清态跳转', async () => {
    // ⭐ 精确控制时序：让 confirm 挂起，直到我们放行。
    // 这样「用户还没决定」这个中间态可被观测 —— 旧实现在这个中间态就已经
    // 把 user 清掉并 replace('/login') 了，用户根本没有选择的余地。
    let release = null
    const gate = new Promise(res => { release = res })
    message.confirm.mockImplementationOnce(async (m, o) => {
      confirmCalls.push({ msg: m, opts: o })
      await gate
      return confirmResult
    })

    const replaceSpy = vi.fn()
    setLocation('/notes', replaceSpy)
    window.localStorage.setItem('user', JSON.stringify({ id: 1, username: 'admin' }))

    const h = apiWith401()
    await expect(h.fire()).rejects.toBeTruthy()
    await vi.waitFor(() => expect(confirmCalls.length).toBe(1))

    // 关键回归：弹窗挂起期间（用户尚未决定）——user 仍在、未跳转、未清 Go 凭据
    expect(window.localStorage.getItem('user')).toBeTruthy()
    expect(replaceSpy).not.toHaveBeenCalled()
    expect(logoutCalls).toBe(0)

    // 放行「确认重登」
    confirmResult = true
    release()
    await vi.waitFor(() => expect(logoutCalls).toBe(1))
    await vi.waitFor(() => expect(replaceSpy).toHaveBeenCalledWith('/login'))
    h.restore()
    expect(window.localStorage.getItem('user')).toBe(null)
  })

  it('用户确认后才清凭据并跳转', async () => {
    const replaceSpy = vi.fn()
    setLocation('/notes', replaceSpy)
    window.localStorage.setItem('user', JSON.stringify({ id: 1, username: 'admin' }))

    const h = apiWith401()
    await expect(h.fire()).rejects.toBeTruthy()
    await vi.waitFor(() => expect(confirmCalls.length).toBe(1))
    await vi.waitFor(() => expect(logoutCalls).toBe(1))
    await vi.waitFor(() => expect(replaceSpy).toHaveBeenCalledWith('/login'))
    h.restore()

    expect(window.localStorage.getItem('user')).toBe(null)
  })

  it('用户选「稍后」则不清凭据、不跳转，允许进程内再自愈', async () => {
    confirmResult = false
    const replaceSpy = vi.fn()
    setLocation('/notes', replaceSpy)
    window.localStorage.setItem('user', JSON.stringify({ id: 1, username: 'admin' }))

    const h = apiWith401()
    await expect(h.fire()).rejects.toBeTruthy()
    await vi.waitFor(() => expect(confirmCalls.length).toBe(1))
    await drain()
    h.restore()

    expect(logoutCalls).toBe(0)
    expect(replaceSpy).not.toHaveBeenCalled()
    expect(window.localStorage.getItem('user')).toBeTruthy()
  })

  it('同窗 401 风暴只弹一次（弱网抖动不刷屏、不锁 UI）', async () => {
    setLocation('/notes')
    const h = apiWith401()
    const results = await Promise.allSettled([
      h.fire('/notes'), h.fire('/folders'), h.fire('/tags'), h.fire('/quota')
    ])
    h.restore()
    expect(results.every(r => r.status === 'rejected')).toBe(true)
    await vi.waitFor(() => expect(confirmCalls.length).toBe(1))
    // 无论多少并发 401，只弹一次
    expect(confirmCalls.length).toBe(1)
  })

  it('弹窗文案必须说清「数据不会丢失」（避免用户以为要重导）', async () => {
    setLocation('/notes')
    const h = apiWith401()
    await expect(h.fire()).rejects.toBeTruthy()
    await vi.waitFor(() => expect(confirmCalls.length).toBe(1))
    h.restore()
    const { msg, opts } = confirmCalls[0]
    expect(msg).toContain('数据不会丢失')
    expect(opts.title).toBe('需要重新登录')
  })

  it('非 401 错误一律不弹重登框（避免把网络抖动误报成掉登录）', async () => {
    setLocation('/notes')
    const inst = api
    const orig = inst.defaults.adapter
    inst.defaults.adapter = async (config) => {
      throw Object.assign(new Error('boom'), {
        config,
        response: { status: 500, data: {}, statusText: '', headers: {}, config }
      })
    }
    await expect(inst.get('/notes')).rejects.toBeTruthy()
    await drain()
    inst.defaults.adapter = orig
    expect(confirmCalls.length).toBe(0)
  })
})