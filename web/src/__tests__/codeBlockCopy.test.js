/**
 * v21.220 代码块复制按钮（codeBlockCopy.js）测试
 * 覆盖：复制函数（clipboard API 优先 + execCommand 兜底）、按钮工厂、预览态挂载（幂等 +
 *       内容剔除按钮）、编辑态 widget 集成（挂载/复制实时内容/getHTML 无污染）、字体下拉含
 *       Noto Sans SC（219 反馈需求 ③）。
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TiptapEditor from '@/components/TiptapEditor.vue'
import {
  legacyCopyText,
  copyCodeText,
  createCodeCopyButton,
  attachCodeCopyButtons
} from '@/components/editor/codeBlockCopy'
import { FONTS } from '@/components/editor/constants'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
async function ready(wrapper) {
  await flushPromises()
  let t = 0
  while (!wrapper.vm.editor && t++ < 100) { await sleep(10); await flushPromises() }
  await flushPromises()
  return wrapper.vm.editor
}

afterEach(() => {
  // 还原 navigator.clipboard 注入（jsdom 原生无此 API）；fakeTimers 兜底还原（防用例失败时泄漏冻结后续用例）
  delete navigator.clipboard
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('复制函数（clipboard API 优先，execCommand 兜底）', () => {
  it('clipboard API 可用时优先使用且返回 true', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    await expect(copyCodeText('hello')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('hello')
  })

  it('clipboard.writeText 拒绝时回退 execCommand（桌面权限拒绝场景）', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn(() => Promise.reject(new Error('denied'))) },
      configurable: true
    })
    const exec = vi.fn(() => true)
    document.execCommand = exec
    await expect(copyCodeText('world')).resolves.toBe(true)
    expect(exec).toHaveBeenCalledWith('copy')
  })

  it('无 clipboard API（局域网 http 非安全上下文）时直接走 execCommand 兜底', async () => {
    const exec = vi.fn(() => true)
    document.execCommand = exec
    await expect(copyCodeText('plain')).resolves.toBe(true)
    expect(exec).toHaveBeenCalledWith('copy')
  })

  it('execCommand 抛异常时返回 false 且不外抛（兜底再失败也不崩）', async () => {
    document.execCommand = vi.fn(() => { throw new Error('not supported') })
    await expect(copyCodeText('x')).resolves.toBe(false)
  })

  it('legacyCopyText 复制后清理临时 textarea（不留孤儿节点）', () => {
    document.execCommand = vi.fn(() => true)
    expect(legacyCopyText('cleanup')).toBe(true)
    expect(document.querySelector('textarea[style*="fixed"]')).toBeNull()
  })
})

describe('按钮工厂 createCodeCopyButton', () => {
  it('DOM 结构：类名/aria/防编辑/双图标 span', () => {
    const btn = createCodeCopyButton(() => 'x')
    expect(btn.className).toBe('code-copy-btn')
    expect(btn.getAttribute('aria-label')).toBe('复制代码')
    expect(btn.getAttribute('title')).toBe('复制代码')
    expect(btn.contentEditable).toBe('false')
    expect(btn.querySelector('.code-copy-ic svg')).not.toBeNull()
    expect(btn.querySelector('.code-copy-ok svg')).not.toBeNull()
  })

  it('mousedown 阻止默认（防编辑器抢焦点/光标跳动）', () => {
    const btn = createCodeCopyButton(() => 'x')
    const ev = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    btn.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(true)
  })

  it('点击复制 getText() 结果并进入已复制态（1.5s 后还原）', async () => {
    vi.useFakeTimers()
    try {
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: vi.fn(() => Promise.resolve()) },
        configurable: true
      })
      const btn = createCodeCopyButton(() => 'CODE_TEXT')
      const clickEv = new MouseEvent('click', { bubbles: true, cancelable: true })
      btn.dispatchEvent(clickEv)
      expect(clickEv.defaultPrevented).toBe(true)
      // 仅 flush 微任务让 then 链落地（勿用 runAllTimersAsync：会把 1.5s 复位 timer 一并跑掉）
      await vi.advanceTimersByTimeAsync(1)
      expect(navigator.clipboard.writeText).toHaveBeenCalledWith('CODE_TEXT')
      expect(btn.dataset.copied).toBe('1')
      vi.advanceTimersByTime(1600)
      expect(btn.dataset.copied).toBe('')
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('预览态挂载 attachCodeCopyButtons', () => {
  it('为容器内每个 pre 挂按钮；重复调用幂等不重复挂', () => {
    const container = document.createElement('div')
    container.innerHTML = '<pre><code>a</code></pre><div><pre><code>b</code></pre></div>'
    expect(attachCodeCopyButtons(container)).toBe(2)
    expect(container.querySelectorAll('.code-copy-btn').length).toBe(2)
    expect(container.querySelectorAll('pre.has-code-copy').length).toBe(2)
    expect(attachCodeCopyButtons(container)).toBe(0)
    expect(container.querySelectorAll('.code-copy-btn').length).toBe(2)
  })

  it('空容器/null 安全返回 0', () => {
    expect(attachCodeCopyButtons(null)).toBe(0)
    expect(attachCodeCopyButtons(document.createElement('div'))).toBe(0)
  })

  it('复制内容为纯代码文本（clone 剔除按钮，不受按钮 DOM 影响）', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn(() => Promise.resolve()) },
      configurable: true
    })
    const container = document.createElement('div')
    container.innerHTML = '<pre><code>line1\nline2</code></pre>'
    attachCodeCopyButtons(container)
    const btn = container.querySelector('.code-copy-btn')
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('line1\nline2')
  })
})

describe('编辑态集成（CodeBlockCopy widget 扩展）', () => {
  it('含代码块的文档渲染出复制按钮，且 getHTML 无按钮污染（decoration 不进文档）', async () => {
    const wrapper = mount(TiptapEditor, {
      props: { modelValue: '<pre><code>const a = 1</code></pre>' }
    })
    const ed = await ready(wrapper)
    expect(wrapper.find('.code-copy-btn').exists()).toBe(true)
    expect(ed.getHTML()).not.toContain('code-copy-btn')
    wrapper.unmount()
  })

  it('点击复制代码块当前内容；编辑内容后再点复制为新内容（posAtDOM 实时反查）', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const wrapper = mount(TiptapEditor, {
      props: { modelValue: '<pre><code>const a = 1</code></pre>' }
    })
    const ed = await ready(wrapper)
    await wrapper.find('.code-copy-btn').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenLastCalledWith('const a = 1')
    // 模拟用户编辑代码后复制实时性
    ed.commands.setContent('<pre><code>const b = 2\nconst c = 3</code></pre>')
    await flushPromises()
    await sleep(30)
    await wrapper.find('.code-copy-btn').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenLastCalledWith('const b = 2\nconst c = 3')
    wrapper.unmount()
  })

  it('无代码块的文档不出现按钮', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>plain</p>' } })
    await ready(wrapper)
    expect(wrapper.find('.code-copy-btn').exists()).toBe(false)
    wrapper.unmount()
  })

  it('D1：光标在代码块内时字体下拉禁用 + 提示 title；回到段落恢复', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>hello</p>' } })
    const ed = await ready(wrapper)
    // 段落内：正常态
    ed.commands.setTextSelection(2)
    await flushPromises()
    expect(wrapper.find('button[title="字体"]').exists()).toBe(true)
    expect(wrapper.find('.tb-btn.tb-disabled').exists()).toBe(false)
    // 进入代码块：禁用态 + 提示
    ed.commands.setContent('<pre><code>x</code></pre>')
    ed.commands.setTextSelection(2)
    await flushPromises()
    expect(ed.isActive('codeBlock')).toBe(true)
    expect(wrapper.find('button[title="代码块使用等宽字体，不支持更换"]').exists()).toBe(true)
    expect(wrapper.find('.tb-btn.tb-disabled').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('字体下拉（219 反馈需求 ③）', () => {
  it('FONTS 含 Noto Sans SC 且带 Source Han Sans SC 回落', () => {
    const hit = FONTS.find((f) => f.label === 'Noto Sans SC')
    expect(hit).toBeDefined()
    expect(hit.value).toContain("'Noto Sans SC'")
    expect(hit.value).toContain("'Source Han Sans SC'")
  })
})
