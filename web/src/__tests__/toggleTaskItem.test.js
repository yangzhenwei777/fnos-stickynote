import { describe, it, expect } from 'vitest'
import { toggleTaskItemInHtml } from '@/utils/note'

// v21.113：预览/阅读态任务清单勾选——DOMParser 补丁函数回归
// HTML 形态与 Tiptap 3.x TaskItem renderHTML 契约一致：
// li[data-type=taskItem][data-checked] > label > input[type=checkbox]

const twoItems = [
  '<ul data-type="taskList">',
  '<li data-type="taskItem" data-checked="false"><label><input type="checkbox"><span></span></label><div><p>任务A</p></div></li>',
  '<li data-type="taskItem" data-checked="false"><label><input type="checkbox"><span></span></label><div><p>任务B</p></div></li>',
  '</ul>'
].join('')

describe('toggleTaskItemInHtml', () => {
  it('勾选：data-checked 置 true 且 input 补 checked 属性', () => {
    const out = toggleTaskItemInHtml(twoItems, 0, true)
    expect(out).toContain('data-checked="true"')
    expect(out).toMatch(/<input type="checkbox" checked=/)
    // 只影响目标项
    expect((out.match(/data-checked="true"/g) || []).length).toBe(1)
    expect((out.match(/data-checked="false"/g) || []).length).toBe(1)
  })

  it('取消勾选：data-checked 置 false 且移除 checked 属性', () => {
    const withChecked = twoItems.replace(/data-checked="false"/, 'data-checked="true"')
    const out = toggleTaskItemInHtml(withChecked, 0, false)
    expect(out).toContain('data-checked="false"')
    expect(out).not.toMatch(/<input type="checkbox" checked=/)
  })

  it('按文档序定位：index=1 只改第二项，第一项不动', () => {
    const out = toggleTaskItemInHtml(twoItems, 1, true)
    const items = out.split('<li ')
    expect(items[1]).toContain('data-checked="false"')
    expect(items[2]).toContain('data-checked="true"')
    expect(items[2]).toContain('任务B')
  })

  it('嵌套子清单：文档序覆盖内外层项，父项索引不受嵌套影响错位', () => {
    const nested = [
      '<ul data-type="taskList">',
      '<li data-type="taskItem" data-checked="false"><label><input type="checkbox"><span></span></label><div><p>父任务</p>',
      '<ul data-type="taskList">',
      '<li data-type="taskItem" data-checked="false"><label><input type="checkbox"><span></span></label><div><p>子任务</p></div></li>',
      '</ul></div></li>',
      '</ul>'
    ].join('')
    const out = toggleTaskItemInHtml(nested, 1, true)
    expect(out).toContain('子任务')
    const parts = out.split('<li ')
    expect(parts[1]).toContain('data-checked="false"') // 父项未动
    expect(parts[2]).toContain('data-checked="true"')  // 子项已勾
  })

  it('普通 bulletList 不计入 taskItem 索引也不被修改', () => {
    const mixed = '<ul><li data-type="listItem"><p>普通项</p></li></ul>' + twoItems
    const out = toggleTaskItemInHtml(mixed, 0, true)
    expect(out).toContain('普通项')
    expect((out.match(/data-checked="true"/g) || []).length).toBe(1)
  })

  it('序列化保持其余内容原样（文本/标题/段落 round-trip）', () => {
    const html = '<h1>标题</h1>' + twoItems + '<p>尾段</p>'
    const out = toggleTaskItemInHtml(html, 0, true)
    expect(out.startsWith('<h1>标题</h1>')).toBe(true)
    expect(out.endsWith('<p>尾段</p>')).toBe(true)
    expect(out).toContain('任务A')
  })

  it('非法输入返回 null：空串 / 非字符串 / 负数索引', () => {
    expect(toggleTaskItemInHtml('', 0, true)).toBeNull()
    expect(toggleTaskItemInHtml(null, 0, true)).toBeNull()
    expect(toggleTaskItemInHtml(123, 0, true)).toBeNull()
    expect(toggleTaskItemInHtml(twoItems, -1, true)).toBeNull()
  })

  it('索引越界返回 null', () => {
    expect(toggleTaskItemInHtml(twoItems, 2, true)).toBeNull()
  })

  it('无 taskList 的普通 HTML 返回 null', () => {
    expect(toggleTaskItemInHtml('<p>没有清单</p>', 0, true)).toBeNull()
  })
})
