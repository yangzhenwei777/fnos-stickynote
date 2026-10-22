/**
 * 评审 B4-4c 前置：工具栏交互特征测试（拆分 EditorToolbar.vue 的回归护栏）。
 * 断言"工具栏按钮 → 驱动 editor 命令"这一耦合行为，拆分前后必须一致。
 */
import { describe, it, expect } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TiptapEditor from '@/components/TiptapEditor.vue'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
async function ready(wrapper) {
  await flushPromises()
  let t = 0
  while (!wrapper.vm.editor && t++ < 100) { await sleep(10); await flushPromises() }
  await flushPromises()
  return wrapper.vm.editor
}

describe('工具栏交互特征（按钮↔editor 接线）', () => {
  it('工具栏渲染出 撤销/恢复/清除格式 按钮（抽取后结构不丢）', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>hello</p>' } })
    await ready(wrapper)
    for (const t of ['撤销', '恢复', '清除格式']) {
      expect(wrapper.find(`button[title="${t}"]`).exists()).toBe(true)
    }
    wrapper.unmount()
  })

  it('选中后点击【清除格式】去除粗体', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p><strong>bold</strong></p>' } })
    const ed = await ready(wrapper)
    expect(ed.isActive('bold')).toBe(true)
    ed.commands.selectAll()
    await flushPromises()
    await wrapper.find('button[title="清除格式"]').trigger('click')
    await sleep(50)
    expect(ed.getHTML()).not.toMatch(/<strong>/)
    wrapper.unmount()
  })

  it('撤销按钮在无历史时禁用（反映 editor.can().undo()）', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>fresh</p>' } })
    await ready(wrapper)
    const undoBtn = wrapper.find('button[title="撤销"]')
    expect(undoBtn.attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
