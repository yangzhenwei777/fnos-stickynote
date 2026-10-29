// tableColPct（v2.2.2 R4 方案 A）：列宽「比例投影」纯逻辑护栏。
//
// 背景：列宽以 px 存储、未拖列靠剩余空间撑开 → 编辑/预览/长图/md 四处容器宽度不同，
// 同一张表长得不一样（用户实测）。R4 起渲染层统一投影为百分比 + 固定布局 + 100% 宽，
// 列宽只表达内部比例。本文件钉的是：补齐规则、合计 100、归一化/幂等、展示投影。
import { describe, it, expect } from 'vitest'
import {
  percentsFromPx, normalizeTo100, resolveColPercents, projectColPercentsInHtml, readColWidth
} from '@/utils/tableColPct'

describe('percentsFromPx — px 比例换算', () => {
  it('全显式：按 px 比例折算（合计 100）', () => {
    expect(percentsFromPx([300, 240, 260])).toEqual([37.5, 30, 32.5])
  })

  it('部分列缺失：按已拖列**平均值**补齐（不用 min-width，避免塌成竖排）', () => {
    // 120/140/缺/100 → 缺列补 120 → 总 480 → 25/29.2/25/20.8
    expect(percentsFromPx([120, 140, 0, 100])).toEqual([25, 29.2, 25, 20.8])
  })

  it('全缺失 / 空 / 非数组 → null（保持自适应，不改动）', () => {
    expect(percentsFromPx([0, 0])).toBeNull()
    expect(percentsFromPx([])).toBeNull()
    expect(percentsFromPx(null)).toBeNull()
  })

  it('合计恒为 100（含 1/3 等除不尽的场景）', () => {
    for (const w of [[100, 100, 100], [1, 1, 1, 1, 1, 1], [7, 13, 29, 51], [1, 999]]) {
      const pct = percentsFromPx(w)
      const sum = Math.round(pct.reduce((a, b) => a + b, 0) * 10) / 10
      expect(sum).toBe(100)
    }
  })
})

describe('normalizeTo100 — 舍入差额并入最大列', () => {
  it('99.9 → 100（差额给最大列）', () => {
    const out = normalizeTo100([37, 29.6, 33.3])
    expect(Math.round(out.reduce((a, b) => a + b, 0) * 10) / 10).toBe(100)
    expect(out[0]).toBe(37.1)
  })
})

describe('readColWidth / resolveColPercents — 两形态解析', () => {
  function cols (styles) {
    const d = document.createElement('div')
    d.innerHTML = `<table><colgroup>${styles.map((s) => `<col style="${s}">`).join('')}</colgroup></table>`
    return d.querySelectorAll('colgroup > col')
  }

  it('px 形态识别；min-width 不算显式宽度', () => {
    const c = cols(['width: 180px', 'min-width: 25px'])
    expect(readColWidth(c[0])).toEqual({ px: 180 })
    expect(readColWidth(c[1])).toBeNull()
  })

  it('百分比形态识别并归一化（R4 序列化形态）', () => {
    const c = cols(['width: 37.5%', 'width: 30%', 'width: 32.5%'])
    expect(resolveColPercents(c)).toEqual([37.5, 30, 32.5])
  })

  it('px 与 pct 混合时按数值近似统一处理（兜底不死锁）', () => {
    const c = cols(['width: 100px', 'width: 100%'])
    expect(resolveColPercents(c)).toEqual([50, 50])
  })

  it('无 col / 全无显式宽度 → null', () => {
    expect(resolveColPercents([])).toBeNull()
    expect(resolveColPercents(cols(['min-width: 25px', 'min-width: 25px']))).toBeNull()
  })
})

describe('projectColPercentsInHtml — 展示层投影', () => {
  const px = '<table><colgroup><col style="width: 300px"><col style="width: 240px"><col style="min-width: 25px"></colgroup><tbody><tr><td>a</td></tr></tbody></table>'

  it('px → 百分比，并清掉 min-width', () => {
    const out = projectColPercentsInHtml(px)
    expect(out).toContain('width: 37.1%')
    expect(out).toContain('width: 29.6%')
    expect(out).toContain('width: 33.3%')
    expect(out).not.toContain('min-width')
    expect(out).not.toContain('width: 300px')
  })

  it('已是百分比 → 原样返回（幂等，不重复构造 DOM）', () => {
    const pctHtml = '<table><colgroup><col style="width: 37.5%"><col style="width: 62.5%"></colgroup><tbody><tr><td>a</td></tr></tbody></table>'
    expect(projectColPercentsInHtml(pctHtml)).toBe(pctHtml)
  })

  it('无 colgroup 内容零开销直通（含 null/空）', () => {
    const plain = '<p>纯文本</p><table><tbody><tr><td>a</td></tr></tbody></table>'
    expect(projectColPercentsInHtml(plain)).toBe(plain)
    expect(projectColPercentsInHtml('')).toBe('')
    expect(projectColPercentsInHtml(null)).toBe(null)
  })
})
