/**
 * @vitest-environment jsdom
 * 覆盖 utils/note.js 纯逻辑函数（纪律 4.4：前端测试基线，冒烟 A1/A6 检查项的防回归兜底）
 */
import { describe, it, expect } from 'vitest'
import { stripHtml, splitTags, formatDate } from '@/utils/note'

describe('stripHtml', () => {
  it('空输入返回空字符串', () => {
    expect(stripHtml(null)).toBe('')
    expect(stripHtml('')).toBe('')
  })

  it('提取纯文本', () => {
    expect(stripHtml('<p>Hello <strong>World</strong></p>')).toBe('Hello World')
  })

  it('去除 script 和 onclick 标签，innerText 无 HTML 标记', () => {
    const out = stripHtml('<p>Text</p><script>alert(1)</script><div onclick="x()">click</div>')
    // stripHtml 用 textContent——浏览器会保留 script 文本但不执行，且不会暴露 onclick
    expect(out).toContain('Text')
    expect(out).toContain('click')
    expect(out).not.toContain('<script>')
    expect(out).not.toContain('<p>')
  })

  it('超长截断并加省略号', () => {
    const long = '<p>' + 'A'.repeat(200) + '</p>'
    expect(stripHtml(long, 80)).toHaveLength(83) // 80 + '...'
    expect(stripHtml(long, 80)).toMatch(/\.{3}$/)
  })

  it('保留文本内空格和中文', () => {
    expect(stripHtml('<p>你好 World</p>')).toBe('你好 World')
  })

  it('img 标签内容不被打入文本', () => {
    expect(stripHtml('<p>hello</p><img src="x.png" />')).toBe('hello')
  })
})

describe('splitTags', () => {
  it('空输入返回空数组', () => {
    expect(splitTags(null)).toEqual([])
    expect(splitTags('')).toEqual([])
  })

  it('单个标签', () => {
    expect(splitTags('工作')).toEqual(['工作'])
  })

  it('多个标签按逗号分割并去空格', () => {
    expect(splitTags('工作, 生活, 学习')).toEqual(['工作', '生活', '学习'])
    expect(splitTags('a,b,c')).toEqual(['a', 'b', 'c'])
  })

  it('过滤空白标签', () => {
    expect(splitTags('工作,, 生活')).toEqual(['工作', '生活'])
    expect(splitTags('  ,  ,')).toEqual([])
  })
})

describe('formatDate', () => {
  it('空输入返回空字符串', () => {
    expect(formatDate(null)).toBe('')
    expect(formatDate('')).toBe('')
  })

  it('刚刚（60 秒内）', () => {
    const now = new Date()
    expect(formatDate(now.toISOString())).toBe('刚刚')
  })

  it('N 分钟前', () => {
    const ago5min = new Date(Date.now() - 5 * 60 * 1000)
    expect(formatDate(ago5min.toISOString())).toBe('5分钟前')
  })

  it('N 小时前', () => {
    const ago3h = new Date(Date.now() - 3 * 3600 * 1000)
    expect(formatDate(ago3h.toISOString())).toBe('3小时前')
  })

  it('久远日期返回格式化', () => {
    const old = new Date('2025-01-01')
    expect(formatDate(old.toISOString())).toBe(old.toLocaleDateString())
  })
})
