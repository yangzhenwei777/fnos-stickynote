import { describe, it, expect } from 'vitest'
import { extractNoteOutline } from '@/composables/useNoteOutline'

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
