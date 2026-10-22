import { describe, it, expect, beforeEach } from 'vitest'
import { detectBase, normalizeBase, apiBase, withBase, APP_BASE_META } from '@/utils/appBase'
import { toDisplayUrl, isUploadUrl } from '@/utils/uploadUrl'

// v2.1.2：子路径挂载的基址推导。
// 这组用例锁的是「应用既能挂站点根、也能挂 /app/<appname>/」的不变量——
// 2.1.1 之前只支持根路径，导致手机 App 经网关加载时资源全落空、永久卡启动页。

function setPath(path) {
  window.history.replaceState({}, '', path)
}

function setHead(html) {
  document.head.innerHTML = html
}

describe('normalizeBase', () => {
  it('根与空值归一为空串', () => {
    expect(normalizeBase('')).toBe('')
    expect(normalizeBase('/')).toBe('')
    expect(normalizeBase(null)).toBe('')
    expect(normalizeBase(undefined)).toBe('')
  })

  it('去掉尾斜杠与重复斜杠', () => {
    expect(normalizeBase('/app/stickynote/')).toBe('/app/stickynote')
    expect(normalizeBase('app/stickynote')).toBe('/app/stickynote')
    expect(normalizeBase('//app//stickynote//')).toBe('/app/stickynote')
  })

  it('剥离 query/hash 与完整 URL', () => {
    expect(normalizeBase('/app/x?a=1')).toBe('/app/x')
    expect(normalizeBase('/app/x#h')).toBe('/app/x')
    expect(normalizeBase('http://host:5666/app/stickynote/')).toBe('/app/stickynote')
  })

  it('拒绝路径穿越', () => {
    expect(normalizeBase('/app/../etc')).toBe('')
    expect(normalizeBase('/app/./x')).toBe('')
  })
})

describe('detectBase', () => {
  beforeEach(() => {
    setHead('')
    setPath('/')
  })

  it('根路径返回空串（与历史行为等价）', () => {
    setPath('/')
    expect(detectBase()).toBe('')
    expect(apiBase()).toBe('/api')
  })

  it('优先读服务端注入的 meta', () => {
    setHead(`<meta name="${APP_BASE_META}" content="/app/stickynote">`)
    setPath('/app/stickynote/login')
    expect(detectBase()).toBe('/app/stickynote')
    expect(apiBase()).toBe('/app/stickynote/api')
  })

  it('meta 显式声明根时返回空串', () => {
    setHead(`<meta name="${APP_BASE_META}" content="">`)
    setPath('/login')
    expect(detectBase()).toBe('')
  })

  it('无 meta 时回落到 <base href>', () => {
    setHead('<base href="/app/stickynote/">')
    setPath('/app/stickynote/notes/1')
    expect(detectBase()).toBe('/app/stickynote')
  })

  it('两者都没有时，目录形态路径可推断（网关首页/剥前缀场景）', () => {
    setPath('/app/stickynote/')
    expect(detectBase()).toBe('/app/stickynote')
  })

  it('两者都没有且路径非目录形态时保守回根（不猜错）', () => {
    setPath('/app/stickynote/login')
    expect(detectBase()).toBe('')
  })
})

describe('withBase', () => {
  beforeEach(() => {
    setHead('')
    setPath('/')
  })

  it('根路径原样返回（零行为变化）', () => {
    expect(withBase('/login')).toBe('/login')
    expect(withBase('/notes-list')).toBe('/notes-list')
  })

  it('子路径下挂到挂载点', () => {
    setHead(`<meta name="${APP_BASE_META}" content="/app/stickynote">`)
    expect(withBase('/login')).toBe('/app/stickynote/login')
    expect(withBase('login')).toBe('/app/stickynote/login')
  })

  it('整页跳转与防重判据必须用同一口径', () => {
    // 401 拦截器用 `pathname !== withBase('/login')` 防重复跳转；
    // 若一边拼前缀一边不拼，子路径下会无限重载（实测踩坑）。
    setHead(`<meta name="${APP_BASE_META}" content="/app/stickynote">`)
    setPath('/app/stickynote/login')
    expect(window.location.pathname).toBe(withBase('/login'))
  })
})

describe('uploadUrl', () => {
  beforeEach(() => {
    setHead('')
    setPath('/')
  })

  it('根路径下原样返回（零行为变化）', () => {
    setPath('/')
    expect(toDisplayUrl('/uploads/a.png')).toBe('/uploads/a.png')
    expect(isUploadUrl('/uploads/a.png')).toBe(true)
  })

  it('子路径下补挂载前缀', () => {
    setHead(`<meta name="${APP_BASE_META}" content="/app/stickynote">`)
    expect(toDisplayUrl('/uploads/a.png')).toBe('/app/stickynote/uploads/a.png')
    expect(isUploadUrl('/uploads/a.png')).toBe(true)
  })

  it('兼容已带前缀的展示态（幂等，不重复叠加）', () => {
    setHead(`<meta name="${APP_BASE_META}" content="/app/stickynote">`)
    expect(isUploadUrl('/app/stickynote/uploads/a.png')).toBe(true)
    expect(toDisplayUrl('/app/stickynote/uploads/a.png')).toBe('/app/stickynote/uploads/a.png')
  })

  it('外链一律不认（消毒白名单不可被绕过）', () => {
    setHead(`<meta name="${APP_BASE_META}" content="/app/stickynote">`)
    expect(isUploadUrl('https://evil.example/x.png')).toBe(false)
    expect(isUploadUrl('data:image/png;base64,AAA')).toBe(false)
    expect(isUploadUrl('/app/other/uploads/x.png')).toBe(false)
    expect(toDisplayUrl('https://evil.example/x.png')).toBe('https://evil.example/x.png')
  })
})
