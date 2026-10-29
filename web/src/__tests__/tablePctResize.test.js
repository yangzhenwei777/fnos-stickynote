// tablePctResize（v2.2.2 R5）：百分比渲染下拖拽桥接的纯逻辑护栏。
//
// 背景（R4 P0）：渲染层把列宽投影成百分比，但 prosemirror-tables 仍按 px 读写，
// 按下时 startWidth 是存储 px、屏幕上是百分比换算值，且表宽被改成 px 总和
// → 把手跳位、拖动错位、松开回跳。R5 在拖拽前后做两次尺度归一（见源文件头注）。
// 本文件钉的是：显示宽归一（合计守恒）、松开后重平衡（总宽守恒 + 最小宽）、
// 跨列/跨行写入，防止回归时又回到「拖 80 变 35」。
import { describe, it, expect } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TiptapEditor from '@/components/TiptapEditor.vue'
import { colWidthsFromTableNode } from '@/components/editor/tablePctView'
import {
  fitWidthsToTotal, rebalanceColumnWidths, measureDisplayWidths, minColumnWidth, applyTableColWidths,
  applyLiveColumnPreview
} from '@/components/editor/tablePctResize'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
async function waitEditor (w) {
  await flushPromises()
  let tries = 0
  while (!w.vm.editor && tries++ < 200) {
    await sleep(10)
    await flushPromises()
  }
  return w.vm.editor
}

function findTable (doc) {
  let tablePos = -1
  let tableNode = null
  doc.descendants((node, pos) => {
    if (tablePos < 0 && node.type.name === 'table') {
      tablePos = pos
      tableNode = node
      return false
    }
    return true
  })
  return { tablePos, tableNode }
}

const sum = (arr) => arr.reduce((a, b) => a + b, 0)

describe('fitWidthsToTotal — 显示宽归一（比例不变、合计精确）', () => {
  it('按比例缩放到目标总宽，合计逐位相等', () => {
    const out = fitWidthsToTotal([300, 240, 260], 622)
    expect(out).toEqual([233, 187, 202])
    expect(sum(out)).toBe(622)
  })

  it('含小数 / 极端比例时合计仍精确（差额并入最大列）', () => {
    for (const [widths, total] of [
      [[232.9, 186.3, 201.8], 621],
      [[1, 1, 1], 100],
      [[7.5, 13.25, 29.75, 51.5], 997]
    ]) {
      const out = fitWidthsToTotal(widths, total)
      expect(sum(out)).toBe(total)
      expect(out.every((w) => w >= 1)).toBe(true)
    }
  })

  it('全零 / 空 / 非法目标 → null（调用方退回官方行为）', () => {
    expect(fitWidthsToTotal([0, 0], 600)).toBeNull()
    expect(fitWidthsToTotal([], 600)).toBeNull()
    expect(fitWidthsToTotal([100, 100], 0)).toBeNull()
    expect(fitWidthsToTotal(null, 600)).toBeNull()
  })
})

describe('rebalanceColumnWidths — 拖拽后总宽守恒', () => {
  it('拖宽 +80：右侧相邻列扣回 80，总宽不变', () => {
    const out = rebalanceColumnWidths([380, 240, 260], 800, new Set([0]), 25)
    expect(out).toEqual([380, 160, 260])
    expect(sum(out)).toBe(800)
  })

  it('相邻列到最小宽仍不足：顺延到后续列', () => {
    // 总宽 670 → 目标 640（+30）；第 2 列只能让 5，第 3 列补 25
    const out = rebalanceColumnWidths([380, 30, 260], 640, new Set([0]), 25)
    expect(out).toEqual([380, 25, 235])
    expect(sum(out)).toBe(640)
  })

  it('所有吸收列触底后从被拖列回收，绝不放大总宽', () => {
    const out = rebalanceColumnWidths([380, 30, 30], 400, new Set([0]), 25)
    expect(sum(out)).toBe(400)
    expect(out).toEqual([350, 25, 25])
  })

  it('拖窄 -20：多出的宽度补给右侧相邻列', () => {
    const out = rebalanceColumnWidths([200, 300, 280], 800, new Set([0]), 25)
    expect(out).toEqual([200, 320, 280])
    expect(sum(out)).toBe(800)
  })

  it('无吸收列 / 无变化列 → null', () => {
    expect(rebalanceColumnWidths([300], 300, new Set([0]))).toBeNull()
    expect(rebalanceColumnWidths([300, 240, 260], 800, new Set())).toBeNull()
  })

  it('最小宽 = max(25px, 表宽 5%)', () => {
    expect(minColumnWidth(622)).toBe(31)
    expect(minColumnWidth(300)).toBe(25)
    expect(minColumnWidth(0)).toBe(25)
  })
})

describe('measureDisplayWidths — 首行显示宽（colspan 均摊）', () => {
  it('逐列展开：普通列 + 跨列均摊', () => {
    const table = document.createElement('table')
    const row = table.insertRow()
    const specs = [[1, 100], [2, 120], [1, 80]]
    for (const [span, width] of specs) {
      const cell = row.insertCell()
      if (span > 1) cell.setAttribute('colspan', String(span))
      cell.getBoundingClientRect = () => ({ width })
    }
    expect(measureDisplayWidths(table)).toEqual([100, 60, 60, 80])
  })

  it('空表 / 无行 → null', () => {
    const table = document.createElement('table')
    expect(measureDisplayWidths(table)).toBeNull()
    expect(measureDisplayWidths(null)).toBeNull()
  })
})

describe('applyLiveColumnPreview — 拖动中实时百分比', () => {
  function tableDom () {
    const t = document.createElement('table')
    t.innerHTML = '<colgroup><col style="width: 37.5%; min-width: 25px"><col style="width: 30%"><col style="width: 32.5%"></colgroup>' +
      '<tbody><tr><td>a</td><td>b</td><td>c</td></tr></tbody>'
    return t
  }

  it('重平衡后的宽度写成百分比：表格 fixed + 100%、min-width 清空（拖动中不撑开）', () => {
    const t = tableDom()
    expect(applyLiveColumnPreview(t, [380, 160, 260], 800)).toBe(true)
    expect(t.style.width).toBe('100%')
    expect(t.style.maxWidth).toBe('100%')
    expect(t.style.tableLayout).toBe('fixed')
    expect(Array.from(t.querySelectorAll('colgroup > col')).map((c) => c.style.width))
      .toEqual(['47.5%', '20%', '32.5%'])
    expect(t.querySelector('col').style.minWidth).toBe('')
  })

  it('列数不一致 / 非法参数 → false 且不改 DOM', () => {
    const t = tableDom()
    const before = t.innerHTML
    expect(applyLiveColumnPreview(t, [100, 200], 800)).toBe(false)
    expect(applyLiveColumnPreview(t, [100, 200, 300], 0)).toBe(false)
    expect(applyLiveColumnPreview(null, [100], 100)).toBe(false)
    expect(t.innerHTML).toBe(before)
  })
})

describe('applyTableColWidths — 写入跨行/跨列单元格', () => {
  it('逐列写入首行与普通行；跨列单元格按数组下标写入', async () => {
    const w = mount(TiptapEditor, {
      props: {
        modelValue: '<table><colgroup><col style="width: 180px"><col style="width: 120px"><col style="width: 200px"></colgroup>' +
          '<tbody><tr><th colspan="2" colwidth="180,120">AB</th><th colwidth="200">C</th></tr>' +
          '<tr><td colspan="2" colwidth="180,120">ab</td><td colwidth="200">c</td></tr></tbody></table>'
      }
    })
    const ed = await waitEditor(w)
    const { tablePos, tableNode } = findTable(ed.state.doc)
    expect(tablePos).toBeGreaterThanOrEqual(0)
    expect(colWidthsFromTableNode(tableNode)).toEqual([180, 120, 200])

    const tr = ed.state.tr
    expect(applyTableColWidths(tr, tablePos, [100, 200, 300])).toBe(true)
    const updated = tr.doc.nodeAt(tablePos)

    // 首列 + 跨列单元格：colwidth 数组逐项更新
    expect(updated.firstChild.firstChild.attrs.colwidth).toEqual([100, 200])
    expect(updated.firstChild.child(1).attrs.colwidth).toEqual([300])
    // 第二行同样同步（否则重进编辑会回到旧宽）
    expect(updated.child(1).firstChild.attrs.colwidth).toEqual([100, 200])
    expect(updated.child(1).child(1).attrs.colwidth).toEqual([300])
    expect(colWidthsFromTableNode(updated)).toEqual([100, 200, 300])
    w.unmount()
  })

  it('列数与 TableMap 不一致 / 非法位置 → false（不误改文档）', async () => {
    const w = mount(TiptapEditor, {
      props: { modelValue: '<table><tbody><tr><td>a</td><td>b</td></tr></tbody></table>' }
    })
    const ed = await waitEditor(w)
    const { tablePos } = findTable(ed.state.doc)
    const tr = ed.state.tr
    expect(applyTableColWidths(tr, tablePos, [100, 200, 300])).toBe(false)
    expect(applyTableColWidths(tr, -1, [100])).toBe(false)
    expect(tr.docChanged).toBe(false)
    w.unmount()
  })
})
