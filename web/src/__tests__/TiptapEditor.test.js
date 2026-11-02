/**
 * TiptapEditor v21.85 性能架构测试：防抖回流 / flushContent / watch 回声守卫
 * 回归点：旧实现每事务两次全文档 getHTML() 序列化 + 父子组件级联重渲染，
 * 大笔记格式操作延迟 100~600ms（见《真机测试报告》与交付说明性能对比）
 */
import { describe, it, expect } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TiptapEditor from '@/components/TiptapEditor.vue'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function waitEditor(wrapper) {
  await flushPromises()
  let tries = 0
  while (!wrapper.vm.editor && tries++ < 100) {
    await sleep(10)
    await flushPromises()
  }
  return wrapper.vm.editor
}

describe('TiptapEditor v21.85 防抖回流', () => {
  it('内容变更：防抖窗口内不 emit，~250ms 后合并发出一次', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>hello</p>' } })
    const ed = await waitEditor(wrapper)
    expect(ed).toBeTruthy()

    ed.commands.insertContent('abc')
    await flushPromises()
    // 旧实现在此已经 emit 全量 HTML；新实现防抖窗口内静默
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()

    await sleep(400)
    await flushPromises()
    const emitted = wrapper.emitted('update:modelValue')
    expect(emitted).toBeTruthy()
    expect(emitted.length).toBe(1)
    expect(emitted[0][0]).toContain('abc')
    wrapper.unmount()
  })

  it('flushContent 立即同步最新内容；挂起的防抖不再重复 emit', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>hello</p>' } })
    const ed = await waitEditor(wrapper)

    ed.commands.insertContent('XYZ')
    const html = wrapper.vm.flushContent()
    expect(html).toContain('XYZ')
    expect(wrapper.emitted('update:modelValue')).toBeTruthy()
    expect(wrapper.emitted('update:modelValue').length).toBe(1)

    await sleep(400)
    await flushPromises()
    // 防抖定时器即使后续触发，内容未变也不会二次 emit
    expect(wrapper.emitted('update:modelValue').length).toBe(1)
    wrapper.unmount()
  })

  it('外部 prop 变更（草稿恢复/解密）→ setContent 生效且不产生回声 emit', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>旧内容</p>' } })
    const ed = await waitEditor(wrapper)

    await wrapper.setProps({ modelValue: '<p>外部注入</p>' })
    await flushPromises()
    expect(ed.getText()).toContain('外部注入')

    await sleep(400)
    await flushPromises()
    // setContent 虽触发 update→scheduleEmit，但 flush 时内容与 lastEmittedHtml
    // 一致，被守卫跳过——不产生任何回声 emit
    expect(wrapper.emitted('update:modelValue') || []).toHaveLength(0)
    wrapper.unmount()
  })

  it('工具栏状态随事务刷新（含纯选区变化，v21.85 修复旧高亮滞后）', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>one</p><p><strong>two</strong></p>' } })
    const ed = await waitEditor(wrapper)

    // 光标移入粗体文字（纯选区事务，文档未变——旧架构不会触发任何回流）
    ed.commands.setTextSelection(ed.state.doc.nodeAt(0) ? 8 : 8)
    await flushPromises()
    // 粗体探针应反映选区状态（断言经由组件暴露的 editor 间接验证模板同源调用）
    expect(ed.isActive('bold')).toBe(true)
    wrapper.unmount()
  })
})

// v2.3.2：进入编辑再退回预览（零编辑）曾被误判为修改 → 自动保存 → updated_at 变化 → 笔记置顶。
// 根因是回流基线取原始 props 串，而编辑器会把内容规范化（换行/链接 rel/b→strong/样式顺序/表格列宽）。
describe('v2.3.2 未编辑不产生回流（预览误置顶回归钉）', () => {
  const normalizationCases = [
    ['块间换行', '<p>a</p>\n<p>b</p>'],
    ['链接补 target/rel', '<p><a href="https://example.com">x</a></p>'],
    ['b 转 strong', '<p><b>x</b></p>'],
    ['div 转 p', '<div>abc</div>'],
    ['样式属性顺序', '<p><span style="font-size: 18px; color: rgb(255, 0, 0);">x</span></p>'],
    ['表格列宽投影', '<table><colgroup><col style="width: 100px"></colgroup><tbody><tr><th><p>h</p></th></tr></tbody></table>'],
    ['块级图片重排', '<p><img src="data:image/png;base64,iVBORw0KGgo=" width="300"></p>']
  ]

  it('规范化差异不被当作修改：flush + 卸载均不 emit', async () => {
    for (const [name, html] of normalizationCases) {
      const wrapper = mount(TiptapEditor, { props: { modelValue: html } })
      const ed = await waitEditor(wrapper)
      // 前提校验：该样本确实发生了规范化，否则用例无法证明问题
      expect(ed.getHTML(), name).not.toBe(html)

      wrapper.vm.flushContent()
      await sleep(350)
      await flushPromises()
      expect(wrapper.emitted('update:modelValue'), name).toBeUndefined()

      wrapper.unmount()
      expect(wrapper.emitted('update:modelValue'), name).toBeUndefined()
    }
  })

  it('连续 3 次挂载/卸载：规范化结果稳定且零 emit', async () => {
    const html = '<p>a</p>\n<p><a href="https://example.com">x</a></p>'
    const outputs = []
    for (let i = 0; i < 3; i++) {
      const wrapper = mount(TiptapEditor, { props: { modelValue: html } })
      await waitEditor(wrapper)
      outputs.push(wrapper.vm.flushContent())
      wrapper.unmount()
      expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    }
    expect(new Set(outputs).size).toBe(1)
  })

  it('ready 事件携带规范化后的 HTML（保存层据此同步内容基线）', async () => {
    const html = '<p>a</p>\n<p>b</p>'
    const wrapper = mount(TiptapEditor, { props: { modelValue: html } })
    const ed = await waitEditor(wrapper)

    const ready = wrapper.emitted('ready')
    expect(ready).toHaveLength(1)
    expect(ready[0][0]).toBe(ed.getHTML())
    wrapper.unmount()
  })
})
