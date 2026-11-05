import { describe, it, expect } from 'vitest'
import { extractNoteOutline, shouldBlockOutlineWheel } from '@/composables/useNoteOutline'

describe('2.4.0 阅读态大纲提取', () => {
  it('只提取 h1~h3，忽略 h4 与空标题', () => {
    const html = '<h1>一</h1><p>x</p><h2> 二 </h2><h3></h3><h4>四</h4>'
    expect(extractNoteOutline(html)).toEqual([
      { level: 1, text: '一' },
      { level: 2, text: '二' }
    ])
  })

  it('空内容返回空数组', () => {
    expect(extractNoteOutline('')).toEqual([])
    expect(extractNoteOutline(null)).toEqual([])
  })

  it('超出上限时截断', () => {
    const html = Array.from({ length: 60 }, (_, i) => `<h2>T${i}</h2>`).join('')
    expect(extractNoteOutline(html)).toHaveLength(50)
  })
})

describe('2.6.3 阅读态大纲滚轮边界', () => {
  it('大纲不足一屏时两个方向都阻断', () => {
    expect(shouldBlockOutlineWheel(100, 0, 500, 500)).toBe(true)
    expect(shouldBlockOutlineWheel(-100, 0, 500, 501)).toBe(true)
  })

  it('到顶部继续向上阻断，向下放行', () => {
    expect(shouldBlockOutlineWheel(-100, 0, 500, 1200)).toBe(true)
    expect(shouldBlockOutlineWheel(100, 0, 500, 1200)).toBe(false)
  })

  it('到底部继续向下阻断，向上放行', () => {
    expect(shouldBlockOutlineWheel(100, 700, 500, 1200)).toBe(true)
    expect(shouldBlockOutlineWheel(-100, 700, 500, 1200)).toBe(false)
  })

  it('中间位置两个方向都放行', () => {
    expect(shouldBlockOutlineWheel(100, 300, 500, 1200)).toBe(false)
    expect(shouldBlockOutlineWheel(-100, 300, 500, 1200)).toBe(false)
  })

  it('无纵向滚动量不阻断', () => {
    expect(shouldBlockOutlineWheel(0, 0, 500, 1200)).toBe(false)
  })

  it('边界与 1px 亚像素误差内按边界处理', () => {
    expect(shouldBlockOutlineWheel(100, 699.5, 500, 1200)).toBe(true)
    expect(shouldBlockOutlineWheel(100, 698, 500, 1200)).toBe(false)
  })
})
