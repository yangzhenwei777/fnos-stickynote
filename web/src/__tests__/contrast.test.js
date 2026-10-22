import { describe, it, expect } from 'vitest'
import { parseColor, rgbToHex, contrastRatio, relativeLuminance, ensureContrast } from '@/utils/contrast'

describe('contrast 工具', () => {
  it('parseColor 支持 #rgb / #rrggbb / rgb()', () => {
    expect(parseColor('#000')).toEqual({ r: 0, g: 0, b: 0 })
    expect(parseColor('#8B1A1A')).toEqual({ r: 139, g: 26, b: 26 })
    expect(parseColor('rgb(0, 0, 0)')).toEqual({ r: 0, g: 0, b: 0 })
    expect(parseColor('rebeccapurple')).toBeNull()
  })

  it('黑字 vs 白底 = 21:1；同色 = 1:1', () => {
    expect(contrastRatio({ r: 0, g: 0, b: 0 }, { r: 255, g: 255, b: 255 })).toBeCloseTo(21, 0)
    expect(contrastRatio({ r: 10, g: 10, b: 10 }, { r: 10, g: 10, b: 10 })).toBeCloseTo(1, 2)
  })

  it('ensureContrast：暗底上的纯黑→提亮到对比≥4.5（无色相退化为中性亮色）', () => {
    const bg = '#1e293b'
    const out = ensureContrast('#000000', bg, 4.5)
    expect(out).toMatch(/^#[0-9a-f]{6}$/)
    expect(contrastRatio(parseColor(out), parseColor(bg))).toBeGreaterThanOrEqual(4.5)
    // 提亮：亮度上升
    expect(relativeLuminance(parseColor(out))).toBeGreaterThan(relativeLuminance({ r: 0, g: 0, b: 0 }))
  })

  it('ensureContrast：深红→保色相(仍偏红)且对比≥4.5', () => {
    const bg = '#1e293b'
    const out = ensureContrast('#8b1a1a', bg, 4.5)
    expect(out).toMatch(/^#[0-9a-f]{6}$/)
    expect(contrastRatio(parseColor(out), parseColor(bg))).toBeGreaterThanOrEqual(4.5)
    const c = parseColor(out)
    expect(c.r).toBeGreaterThan(c.g) && expect(c.r).toBeGreaterThan(c.b) // 仍以红为主分量
  })

  it('ensureContrast：已满足对比则返回 null（不改）', () => {
    expect(ensureContrast('#f3f4f6', '#1e293b', 4.5)).toBeNull()
    expect(ensureContrast('#374151', '#ffffff', 4.5)).toBeNull()
  })

  it('rgbToHex 归一并限幅', () => {
    expect(rgbToHex({ r: 255, g: 0, b: 0 })).toBe('#ff0000')
    expect(rgbToHex({ r: 300, g: -5, b: 26 })).toBe('#ff001a')
  })
})
