// isImportedNote 契约测试：导入调用点的形状闸门。
// 反向锁定历史缺陷「提示导入成功但笔记空白/消失」——错误体（如未接线 501 的
// {error, code}、或任何无 id 的对象）都必须被判定为"不是一篇笔记"。
import { describe, it, expect } from 'vitest'
import { isImportedNote } from '@/composables/useNoteImport'

describe('isImportedNote 导入返回体形状闸门', () => {
  it('合法笔记（有正整数 id）→ true', () => {
    expect(isImportedNote({ id: 9, title: 't', content: '<p>x</p>' })).toBe(true)
    expect(isImportedNote({ id: '9' })).toBe(true) // 兼容字符串数字
  })

  it('历史缺陷体：未接线错误对象 → false（绝不当笔记插入）', () => {
    expect(isImportedNote({ error: '桌面版尚未支持该功能（POST /notes/import）', code: 'DESKTOP_UNMAPPED' })).toBe(false)
  })

  it('无 id / id 非法 → false', () => {
    for (const v of [null, undefined, {}, { title: '无标题' }, { id: 0 }, { id: -1 }, { id: 'abc' }, { id: NaN }, [], 'x', 1]) {
      expect(isImportedNote(v), `value=${JSON.stringify(v)}`).toBe(false)
    }
  })
})
