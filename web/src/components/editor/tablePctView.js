import { Table, TableView, createColGroup } from '@tiptap/extension-table'
import { mergeAttributes } from '@tiptap/core'
import { percentsFromPx } from '@/utils/tableColPct'

// tablePctView.js — 表格渲染层「列宽比例投影」（v2.2.2 R4，方案 A）。
//
// 存储仍是 px（单元格 colwidth / colgroup 宽度），但**渲染**一律转成百分比：
//   · 编辑器节点视图（PctTableView）：col style 写 `width:X%`，表格 `width:100%
//     + table-layout:fixed`（官方 TableView 会写 px 并设 table 宽度为 px 和，
//     在窄窗口里会溢出；这里在其 update 之后统一覆写）。
//   · 序列化（renderHTML）：colgroup 直接输出百分比，单元格仍带 colwidth px
//     （编辑器重解析的首选通道），保证「存进去的比例」与「渲染出来的比例」一致。
//
// 为什么不改用「百分比存储」：Tiptap/prosemirror-tables 的拖拽算法以 px 为基准
// （startWidth 读 attrs.colwidth 或 DOM offsetWidth），存百分比会破坏拖拽。
// 保持 px 存储 + 渲染投影是改动面最小的做法；但**单纯投影会让 px 域与屏幕域
// 脱节**（R4 的拖拽 P0）。R5 由 tablePctResize.js 在官方拖拽前后做尺度归一，
// 使「按下 / 拖动 / 松开」全程 1:1，详见该文件头注。
//
// 未拖过的列按「已拖列平均值」补齐（见 tableColPct.percentsFromPx），
// 避免 min-width 25px 被压成竖排单字。

/**
 * 从表格节点首行提取逐列宽度（px，未指定为 0；colspan 展开）。
 * @param {object} node Tiptap table 节点
 * @returns {number[]}
 */
export function colWidthsFromTableNode (node) {
  const widths = []
  const row = node && node.firstChild
  if (!row) return widths
  for (let i = 0; i < row.childCount; i += 1) {
    const { colspan, colwidth } = row.child(i).attrs
    for (let j = 0; j < colspan; j += 1) {
      widths.push(colwidth && colwidth[j] ? colwidth[j] : 0)
    }
  }
  return widths
}

/** 官方 TableView 子类：在 px 渲染之后把列宽投影为百分比并撑满正文宽 */
export class PctTableView extends TableView {
  constructor (node, cellMinWidth, view, HTMLAttributes = {}) {
    super(node, cellMinWidth, view, HTMLAttributes)
    this.applyColPercents()
  }

  update (node) {
    if (node.type !== this.node.type) return false
    const ok = super.update(node)
    if (!ok) return false
    this.applyColPercents()
    return true
  }

  /** 取代官方 px 列宽：col 写百分比，表格固定布局 + 100% 宽 */
  applyColPercents () {
    const pct = percentsFromPx(colWidthsFromTableNode(this.node))
    const cols = this.colgroup ? this.colgroup.children : null
    if (!pct || !cols || cols.length !== pct.length) {
      // 无显式列宽（从未拖过）→ 交回官方自适应布局
      this.table.style.tableLayout = ''
      return
    }
    for (let i = 0; i < cols.length; i += 1) {
      cols[i].style.width = `${pct[i]}%`
      cols[i].style.minWidth = ''
    }
    this.table.style.width = '100%'
    this.table.style.maxWidth = '100%'
    this.table.style.minWidth = ''
    this.table.style.tableLayout = 'fixed'
  }
}

/**
 * 扩展 Table：列宽输出百分比（colgroup）+ 使用 PctTableView 渲染。
 * 其余行为（拖拽、合并、增删行列）全部沿用官方实现。
 */
export const StickyTable = Table.extend({
  renderHTML ({ node, HTMLAttributes }) {
    const pct = percentsFromPx(colWidthsFromTableNode(node))
    let colgroup
    if (pct) {
      colgroup = ['colgroup', {}, ...pct.map((p) => ['col', { style: `width: ${p}%` }])]
    } else {
      // 无显式列宽：沿用官方（min-width 列），表格保持自适应
      colgroup = createColGroup(node, this.options.cellMinWidth).colgroup
    }
    const userStyles = HTMLAttributes.style
    const style = userStyles || (pct ? 'width: 100%' : undefined)
    const table = [
      'table',
      mergeAttributes(this.options.HTMLAttributes, HTMLAttributes, style ? { style } : {}),
      colgroup,
      ['tbody', 0]
    ]
    return this.options.renderWrapper ? ['div', { class: 'tableWrapper' }, table] : table
  },

  addNodeView () {
    // 与官方一致：仅「可编辑 + resizable」时由 columnResizing 插件用 options.View 建视图
    const isResizable = this.options.resizable && this.editor.isEditable
    if (isResizable) return null
    if (!this.options.View) return null
    return ({ node, view, HTMLAttributes }) => {
      const merged = mergeAttributes(this.options.HTMLAttributes, HTMLAttributes)
      return new this.options.View(node, this.options.cellMinWidth, view, merged)
    }
  }
})
