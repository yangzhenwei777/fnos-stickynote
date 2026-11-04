import { describe, it, expect } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TiptapEditor from '@/components/TiptapEditor.vue'
import { filterSlashItems } from '@/components/editor/slashItems'

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

describe('2.4.0 斜杠命令', () => {
  it('过滤：英文、中文、别名与无匹配', () => {
    expect(filterSlashItems('h1').map((i) => i.id)).toEqual(['h1'])
    expect(filterSlashItems('标题').map((i) => i.id)).toEqual(['h1', 'h2', 'h3'])
    expect(filterSlashItems('todo').map((i) => i.id)).toEqual(['task'])
    expect(filterSlashItems('zzz')).toEqual([])
  })

  it('段首输入 / 打开菜单，mousedown 插入标题并关闭菜单', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p></p>' } })
    const ed = await waitEditor(wrapper)

    ed.commands.focus('end')
    ed.commands.insertContent('/')
    await flushPromises()
    await sleep(0)

    const menu = document.querySelector('.slash-menu')
    expect(menu).toBeTruthy()
    const first = menu.querySelector('.slash-menu-item')
    first.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
    await flushPromises()

    expect(ed.getHTML()).toContain('<h1')
    expect(document.querySelector('.slash-menu')).toBeFalsy()
    wrapper.unmount()
  })

  it('输入过滤词后回车，插入任务清单', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p></p>' } })
    const ed = await waitEditor(wrapper)

    ed.commands.focus('end')
    ed.commands.insertContent('/todo')
    await flushPromises()
    await sleep(0)
    expect(document.querySelector('.slash-menu')).toBeTruthy()

    ed.view.dom.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await flushPromises()

    expect(ed.getHTML()).toContain('data-type="taskList"')
    wrapper.unmount()
  })

  it('普通正文中的 / 不触发菜单', async () => {
    const wrapper = mount(TiptapEditor, { props: { modelValue: '<p>路径 /usr/local</p>' } })
    const ed = await waitEditor(wrapper)

    ed.commands.focus('end')
    ed.commands.insertContent(' ')
    await flushPromises()
    await sleep(0)

    expect(document.querySelector('.slash-menu')).toBeFalsy()
    wrapper.unmount()
  })
})
