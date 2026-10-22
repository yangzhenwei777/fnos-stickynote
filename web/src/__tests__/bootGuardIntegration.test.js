/**
 * 启动失败兜底集成测试（v21.195）。
 *
 * 覆盖 194 的真实缺口：app.mount() 自身抛错时，占位被撤、错误钩子未装、app 内横幅
 * 渲染不出来 → 纯白零提示。此处让App.vue 在 setup 阶段抛错，断言 main.js 走
 * catch 分支：占位保留 + 标题转「启动失败」+ 展示错误摘要 + 重载出口可见。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'

vi.mock('@/App.vue', () => ({
  default: {
    name: 'BoomApp',
    setup () { throw new Error('boom-mount-195') }
  }
}))

describe('main.js 挂载失败兜底（v21.195）', () => {
  beforeEach(() => {
    vi.resetModules()
    localStorage.clear()
    document.body.innerHTML =
      '<div id="boot-guard">' +
      '<div class="boot-title">正在启动 Sticky note…</div>' +
      '<div class="boot-hint">若长时间停留在此页面，请按 Ctrl+R 重新加载。</div>' +
      '<a id="boot-reload" href="/" style="display:none;">重新加载 Sticky note</a>' +
      '</div><div id="app"></div>'
    // jsdom 无 matchMedia（useTheme 依赖）
    window.matchMedia = window.matchMedia || ((q) => ({
      matches: false, media: q, onchange: null,
      addEventListener () {}, removeEventListener () {}, addListener () {}, removeListener () {}, dispatchEvent: () => false
    }))
    window.scrollTo = window.scrollTo || (() => {})
  })

  it('mount 抛错 → 不再纯白：占位转失败态并显示错误摘要与重载出口',async () => {
    // 毒化键必须仍在（验证「即使存储有毒，兜底依然生效」这一极端组合）
    localStorage.setItem('user', 'undefined')
    await expect(import('@/main.js')).rejects.toThrow('boom-mount-195')

    const g = document.getElementById('boot-guard')
    expect(g).not.toBeNull()
    expect(g.querySelector('.boot-title').textContent).toContain('启动失败')
    const hint = g.querySelector('.boot-hint').textContent
    expect(hint).toContain('boom-mount-195')
    expect(hint).toContain('重新加载')     // 文案含自救指引
    expect(document.getElementById('boot-reload').style.display).toBe('inline-block')
  })

  it('main.js 在 App 求值前先做存量自愈（毒键已被清除，正常键保留）', async () => {
    localStorage.setItem('user', 'undefined')
    localStorage.setItem('login_remember', '{oops')
    localStorage.setItem('sn_theme', 'dark')
    await expect(import('@/main.js')).rejects.toThrow()
    expect(localStorage.getItem('user')).toBeNull()
    expect(localStorage.getItem('login_remember')).toBeNull()
    expect(localStorage.getItem('sn_theme')).toBe('dark')
  })
})
