// 评审 B4 拆分护栏：ResizableImage（已从组件外提至 editor/extensions.js）行为回归
import { describe, it, expect } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TiptapEditor from '@/components/TiptapEditor.vue'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
async function waitEditor(wrapper) {
  await flushPromises()
  let t = 0
  while (!wrapper.vm.editor && t++ < 100) { await sleep(10); await flushPromises() }
  return wrapper.vm.editor
}

describe('ResizableImage 扩展（外提后）', () => {
  it('setImage 带 width → 文档模型保留 width 属性并序列化输出', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>正文</p>' } })
    const ed = await waitEditor(wrapper)
    ed.commands.setImage({ src: '/uploads/x.png', alt: 'a', width: 240 })
    await flushPromises()
    const html = ed.getHTML()
    expect(html).toContain('/uploads/x.png')
    expect(html).toMatch(/width="240"/)
    wrapper.unmount()
  })

  it('未设 width 时不产出 width 属性', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>正文</p>' } })
    const ed = await waitEditor(wrapper)
    ed.commands.setImage({ src: '/uploads/y.png', alt: '' })
    await flushPromises()
    expect(ed.getHTML()).not.toMatch(/width=/)
    wrapper.unmount()
  })
})
