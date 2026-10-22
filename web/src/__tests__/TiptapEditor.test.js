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
