// notePager.test.js —— 分页数字窗纯函数（1.0.218）边界钉。
// 三段式与用户参考图逐行对齐：首部 1 2 3 4 …N / 中部 1 … c-1 c c+1 …N / 尾部 1 … N-3..N。
import { describe, it, expect } from 'vitest'
import { buildPagerItems } from '@/utils/pager'

const toText = (items) => items.map(i => (i.type === 'page' ? i.page : '…')).join(' ')

describe('分页数字窗（参考图三段式）', () => {
  it('N ≤ 7 全量直列（不折叠）', () => {
    expect(toText(buildPagerItems(1, 1))).toBe('1')
    expect(toText(buildPagerItems(7, 4))).toBe('1 2 3 4 5 6 7')
  })

  it('首部态：1 2 3 4 … N（参考图第 1 行）', () => {
    expect(toText(buildPagerItems(12, 1))).toBe('1 2 3 4 … 12')
    expect(toText(buildPagerItems(12, 4))).toBe('1 2 3 4 … 12')
  })

  it('中部态：1 … c-1 c c+1 … N（参考图第 2 行，当前 6）', () => {
    expect(toText(buildPagerItems(12, 6))).toBe('1 … 5 6 7 … 12')
    expect(toText(buildPagerItems(12, 5))).toBe('1 … 4 5 6 … 12')
  })

  it('尾部态：1 … N-3 N-2 N-1 N（参考图第 3 行）', () => {
    expect(toText(buildPagerItems(12, 10))).toBe('1 … 9 10 11 12')
    expect(toText(buildPagerItems(12, 12))).toBe('1 … 9 10 11 12')
  })

  it('边界衔接不重复不塌缩：N=8 两侧', () => {
    expect(toText(buildPagerItems(8, 4))).toBe('1 2 3 4 … 8')
    expect(toText(buildPagerItems(8, 5))).toBe('1 … 5 6 7 8')
  })

  it('非法入参钳制（不炸、不给出界页码）', () => {
    expect(toText(buildPagerItems(0, 0))).toBe('1')
    expect(toText(buildPagerItems(12, 999))).toBe('1 … 9 10 11 12')
    expect(toText(buildPagerItems(12, -3))).toBe('1 2 3 4 … 12')
  })
})
