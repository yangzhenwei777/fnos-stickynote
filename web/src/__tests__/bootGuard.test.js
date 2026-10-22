/**
 * 启动占位三态测试（v21.195 白屏兜底轮）。
 *
 * 194 的缺口：installGlobalErrorHooks 装在 mount 之后、占位在挂载成功后无条件移除；
 * 一旦 app.mount() 自身抛错（auth store 初始化异常等），钩子没装、占位被撤、app 内
 * 横幅也渲染不出来 → 纯白且零提示。现改为：成功才移除；失败转失败态并保留纯 HTML
 * 的「重新加载」出口（不依赖任何 JS）。
 */
import { describe, it, expect, beforeEach } from 'vitest'
import { clearBootGuard, showBootFailure } from '@/utils/bootGuard'

function mountGuard () {
  document.body.innerHTML =
    '<div id="boot-guard">' +
    '<div class="boot-title">正在启动 Sticky note…</div>' +
    '<div class="boot-hint">若长时间停留在此页面，请按 Ctrl+R 重新加载。</div>' +
    '<a id="boot-reload" href="/" style="display:none;">重新加载 Sticky note</a>' +
    '</div>'
}

describe('utils/bootGuard（v21.195）', () => {
  beforeEach(() => { document.body.innerHTML = '' })

  it('挂载成功：clearBootGuard 移除占位', () => {
    mountGuard()
    clearBootGuard()
    expect(document.getElementById('boot-guard')).toBeNull()
  })

  it('挂载失败：占位保留 + 标题转失败态 + 展示错误摘要', () => {
    mountGuard()
    showBootFailure("Unexpected token 'u', \"undefined\" is not valid JSON")
    const g = document.getElementById('boot-guard')
    expect(g).not.toBeNull()
    expect(g.querySelector('.boot-title').textContent).toContain('启动失败')
    expect(g.querySelector('.boot-hint').textContent).toContain('Unexpected token')
    expect(g.querySelector('.boot-hint').textContent).toContain('sticky-note-data')
  })

  it('挂载失败：重新加载出口可见且是纯 <a href>（JS 全挂也能自救）', () => {
    mountGuard()
    showBootFailure('boom')
    const a = document.getElementById('boot-reload')
    expect(a).not.toBeNull()
    expect(a.tagName).toBe('A')
    expect(a.getAttribute('href')).toBe('/')
    expect(a.style.display).toBe('inline-block')
  })

  it('无占位元素时调用不抛（幂等）', () => {
    expect(() => { clearBootGuard(); showBootFailure('x') }).not.toThrow()
  })
})
