import { Extension } from '@tiptap/core'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { columnResizingPluginKey, TableMap } from '@tiptap/pm/tables'
import { colWidthsFromTableNode } from './tablePctView'
import { percentsFromPx, normalizeTo100 } from '@/utils/tableColPct'

// tablePctResize.js — 百分比渲染下的列宽拖拽桥接（v2.2.2 R5）。
//
// ## 要解决的问题（R4 实测 P0）
// R4 的 PctTableView 把**渲染**统一成百分比（colgroup `width:X%` + table
// `table-layout:fixed; width:100%`），但 prosemirror-tables 的拖拽协议仍按 px 工作：
//   ① 按下时 `currentColWidth()` 取的是存储 px（如 300），而屏幕上是百分比换算后的
//      实际值（如 233px）→ 起始基准不一致；
//   ② 紧接着 `displayColumnWidth()` 直接改写 colgroup/table 为 px，并把表宽设为
//      **各列 px 之和**（如 800px）→ 表格瞬间撑开，拖拽把手跳位；
//   ③ 拖动期间始终走 px 直写 DOM，绕开 PctTableView.update；
//   ④ 松开后 `updateColumnWidth()` 提交事务，PctTableView 又把 px 投影回百分比 →
//      表格缩回容器宽，视觉增量 = delta × (总宽-起始列宽)/总宽，终端用户感受为
//      「拖 80px 只变 35px」「越拖越乱」。
//
// ## 本文件的做法（不改 schema / 不改百分比渲染 / 不改 Go 导出）
// **在官方拖拽前后各做一次尺度归一，把 px 域临时对齐到屏幕域：**
//   1. `mousedown` 捕获阶段（早于官方 handler）：若把手已激活，读取当前**显示宽**
//      （首行单元格 rect，colspan 均摊），按比例换算成合计 = 表格显示宽的整数 px，
//      一次性写入所有单元格的 `colwidth`（比例不变，展示零变化）。
//      → 官方 `currentColWidth()` 读到的 px 与屏幕一致，按下不再跳变。
//   2. 官方拖拽结束事务后（`appendTransaction`，此时 dragging 仍为 true）：把
//      「各列 px 之和相对拖拽前的增量」从右侧相邻列扣回（不足时顺延到后续列，
//      每列保留最小宽），使总宽恒定。
//      → PctTableView 再投影百分比时，被拖列显示宽 = 起始宽 + 鼠标位移（1:1），
//        右侧列同步收窄，表格总宽不跳。
//
// 边界：
//   · 只桥接「把手已激活」的 mousedown，普通点击/选区不受影响；
//   · 归一化事务 `addToHistory:false`，且比例不变，只在首次拖拽/窗口宽变化后发生；
//   · 最后一列的右边界在固定宽表格里没有可移动空间，由 `lastColumnResizable:false`
//     关闭（TiptapEditor 配置），本文件不处理；
//   · 归一化失败（如无 DOM / 行缺失）时静默退回官方行为，不阻断编辑。

/** 桥接插件 key（仅用于调试/扩展，不参与状态） */
export const tablePctResizeKey = new PluginKey('tablePctResize')

/** 与 prosemirror-tables/cellMinWidth 同口径的最小列宽（px） */
export const PCT_RESIZE_MIN_PX = 25

/** 最小列宽：25px 与表宽 5% 取大（窄表不塌成竖排单字） */
export function minColumnWidth (tableWidth) {
  return Math.max(PCT_RESIZE_MIN_PX, Math.round((Number(tableWidth) || 0) * 0.05))
}

/**
 * 把一组宽度按比例缩放到指定总宽（整数 px，合计精确等于 total）。
 * 舍入差额并入最大列（与 `tableColPct.percentsFromPx` 同口径）。
 * @param {number[]} widths
 * @param {number} total
 * @returns {number[]|null}
 */
export function fitWidthsToTotal (widths, total) {
  if (!Array.isArray(widths) || !widths.length) return null
  const target = Math.round(Number(total))
  if (!(target > 0)) return null
  const nums = widths.map((w) => (Number.isFinite(w) && w > 0 ? w : 0))
  const sum = nums.reduce((a, b) => a + b, 0)
  if (!(sum > 0)) return null
  const scaled = nums.map((w) => Math.max(1, Math.round((w / sum) * target)))
  let drift = target - scaled.reduce((a, b) => a + b, 0)
  if (drift !== 0) {
    let mi = 0
    for (let i = 1; i < scaled.length; i++) if (scaled[i] > scaled[mi]) mi = i
    scaled[mi] = Math.max(1, scaled[mi] + drift)
    drift = target - scaled.reduce((a, b) => a + b, 0)
    // 极端情况下最大列被 1px 下限钳住，差额顺延到其余列，保证合计不变
    for (let i = 0; drift !== 0 && i < scaled.length; i++) {
      if (i === mi) continue
      const next = Math.max(1, scaled[i] + drift)
      drift -= next - scaled[i]
      scaled[i] = next
    }
  }
  return scaled
}

/**
 * 拖拽结束后保持总宽不变：把 `sum(widths) - targetTotal` 的增量从吸收列扣回。
 * 吸收顺序 = 被拖列右侧相邻列优先，不足顺延到后续列，再兜底到左侧列；
 * 增长（拖窄）时直接补给右侧相邻列。
 * @param {number[]} widths 拖拽后的各列宽（px）
 * @param {number} targetTotal 拖拽前总宽（px）
 * @param {Set<number>|number[]} changedCols 被拖拽修改的列下标
 * @param {number} [minPx] 吸收列的最小宽
 * @returns {number[]|null}
 */
export function rebalanceColumnWidths (widths, targetTotal, changedCols, minPx = PCT_RESIZE_MIN_PX) {
  if (!Array.isArray(widths) || widths.length < 2) return null
  const target = Math.round(Number(targetTotal))
  if (!(target > 0)) return null
  const nums = widths.map((w) => Math.max(1, Math.round(w)))
  const changed = new Set(changedCols instanceof Set ? changedCols : (changedCols || []))
  if (!changed.size) return null
  const absorbers = []
  const lastChanged = Math.max(...changed)
  for (let i = lastChanged + 1; i < nums.length; i += 1) if (!changed.has(i)) absorbers.push(i)
  for (let i = 0; i < nums.length; i += 1) if (!changed.has(i) && !absorbers.includes(i)) absorbers.push(i)
  if (!absorbers.length) return null
  const min = Math.max(1, Math.round(minPx))
  let diff = nums.reduce((a, b) => a + b, 0) - target
  if (diff > 0) {
    // 变宽：从吸收列扣回
    for (const i of absorbers) {
      if (diff <= 0) break
      const take = Math.min(diff, Math.max(0, nums[i] - min))
      nums[i] -= take
      diff -= take
    }
    // 吸收列都到最小宽仍不够：从被拖列自身回收，守住总宽不溢出
    if (diff > 0) {
      for (const i of changed) {
        if (diff <= 0) break
        const take = Math.min(diff, Math.max(0, nums[i] - min))
        nums[i] -= take
        diff -= take
      }
    }
    if (diff > 0) return null
  } else if (diff < 0) {
    // 变窄：多出的宽度补给右侧相邻列
    nums[absorbers[0]] += -diff
  }
  return nums
}

/**
 * 读取表格首行各列的**显示宽**（colspan 均摊）。
 * @param {HTMLTableElement} tableDom
 * @returns {number[]|null}
 */
export function measureDisplayWidths (tableDom) {
  const row = tableDom && tableDom.rows && tableDom.rows[0]
  if (!row) return null
  const out = []
  for (const cell of Array.from(row.cells || [])) {
    const span = Math.max(1, parseInt(cell.getAttribute('colspan') || '1', 10) || 1)
    const width = cell.getBoundingClientRect().width
    for (let i = 0; i < span; i += 1) out.push(width > 0 ? width / span : 0)
  }
  return out.length ? out : null
}

/**
 * 把逐列宽写入表格所有单元格的 `colwidth`（跨列单元格按数组下标写入）。
 * 结构不变，只改属性；同一单元格的多次写入合并后一次 setNodeMarkup。
 * @param {import('@tiptap/pm/state').Transaction} tr
 * @param {number} tablePos 表格节点位置
 * @param {number[]} widths 逐列宽（长度 = 表格列数）
 * @returns {boolean}
 */
export function applyTableColWidths (tr, tablePos, widths) {
  if (!Number.isInteger(tablePos) || tablePos < 0 || tablePos >= tr.doc.content.size) return false
  const table = tr.doc.nodeAt(tablePos)
  if (!table || !Array.isArray(widths)) return false
  const map = TableMap.get(table)
  if (!map || map.width !== widths.length) return false
  /** @type {Map<number, {attrs:object, colwidth:number[]}>} */
  const updates = new Map()
  for (let col = 0; col < widths.length; col += 1) {
    const width = Math.round(widths[col])
    if (!(width > 0)) continue
    for (let row = 0; row < map.height; row += 1) {
      const mapIndex = row * map.width + col
      // 纵向跨行：同一单元格只在首行处理一次
      if (row > 0 && map.map[mapIndex] === map.map[mapIndex - map.width]) continue
      const rel = map.map[mapIndex]
      const cell = table.nodeAt(rel)
      if (!cell) continue
      const attrs = cell.attrs
      const index = attrs.colspan === 1 ? 0 : col - map.colCount(rel)
      if (index < 0 || index >= attrs.colspan) continue
      let entry = updates.get(rel)
      if (!entry) {
        entry = {
          attrs,
          colwidth: attrs.colwidth ? attrs.colwidth.slice() : new Array(attrs.colspan).fill(0)
        }
        updates.set(rel, entry)
      }
      entry.colwidth[index] = width
    }
  }
  for (const [rel, entry] of updates) {
    tr.setNodeMarkup(tablePos + 1 + rel, null, { ...entry.attrs, colwidth: entry.colwidth })
  }
  return updates.size > 0
}

/** 从把手位置向上找表格 DOM（照搬官方 displayColumnWidth 的定位方式） */
function findTableDom (view, $cell) {
  let dom
  try {
    dom = view.domAtPos($cell.start(-1)).node
  } catch {
    return null
  }
  while (dom && dom.nodeName !== 'TABLE') dom = dom.parentNode
  return dom && dom.nodeName === 'TABLE' ? dom : null
}

/**
 * 拖动中的实时预览：把重平衡后的列宽写成百分比，表格保持容器宽。
 * 官方每次 mousemove 会先把 DOM 改回 px（表宽 = px 总和），本函数在其后
 * （同一事件内、绘制前）覆写，因此用户看不到表格临时撑开/松开回跳。
 * @param {HTMLTableElement} tableDom
 * @param {number[]} widths 已重平衡的逐列宽（合计 = total）
 * @param {number} total
 */
export function applyLiveColumnPreview (tableDom, widths, total) {
  if (!tableDom || !Array.isArray(widths) || !(total > 0)) return false
  const cols = tableDom.querySelectorAll('colgroup > col')
  if (!cols.length || cols.length !== widths.length) return false
  const pct = normalizeTo100(widths.map((w) => (w / total) * 100))
  for (let i = 0; i < cols.length; i += 1) {
    cols[i].style.width = `${pct[i]}%`
    cols[i].style.minWidth = ''
  }
  tableDom.style.width = '100%'
  tableDom.style.maxWidth = '100%'
  tableDom.style.minWidth = ''
  tableDom.style.tableLayout = 'fixed'
  return true
}

/**
 * 官方拖拽结束事务之后，重平衡被拖表格的列宽。
 * @returns {import('@tiptap/pm/state').Transaction|null}
 */
function rebalanceAfterDrag (oldState, newState, handle) {
  let $cell
  try {
    $cell = newState.doc.resolve(handle)
  } catch {
    return null
  }
  const tablePos = $cell.before(-1)
  const table = newState.doc.nodeAt(tablePos)
  const oldTable = oldState.doc.nodeAt(tablePos)
  if (!table || !oldTable || table.type.name !== 'table' || oldTable.type.name !== 'table') return null
  // 结构变化（增删行列/合并）不参与重平衡，交回官方行为
  if (table.nodeSize !== oldTable.nodeSize) return null
  const newWidths = colWidthsFromTableNode(table)
  const oldWidths = colWidthsFromTableNode(oldTable)
  if (!newWidths.length || newWidths.length !== oldWidths.length) return null
  if (newWidths.some((w) => !(w > 0)) || oldWidths.some((w) => !(w > 0))) return null
  const target = Math.round(oldWidths.reduce((a, b) => a + b, 0))
  const changed = []
  for (let i = 0; i < newWidths.length; i += 1) {
    if (Math.abs(newWidths[i] - oldWidths[i]) > 0.5) changed.push(i)
  }
  if (!changed.length) return null
  const rebalanced = rebalanceColumnWidths(newWidths, target, new Set(changed), minColumnWidth(target))
  if (!rebalanced || rebalanced.every((w, i) => w === newWidths[i])) return null
  const tr = newState.tr
  if (!applyTableColWidths(tr, tablePos, rebalanced)) return null
  return tr.docChanged ? tr : null
}

/**
 * 被拖拽列在表格中的列下标（官方口径：多列单元格取最右列）。
 * @returns {number} 失败返回 -1
 */
function draggedColumnIndex (state, handle) {
  try {
    const $cell = state.doc.resolve(handle)
    const table = $cell.node(-1)
    if (!table || table.type.name !== 'table') return -1
    const map = TableMap.get(table)
    return map.colCount($cell.pos - $cell.start(-1)) + $cell.nodeAfter.attrs.colspan - 1
  } catch {
    return -1
  }
}

/**
 * 编辑器扩展：在官方 columnResizing 前后补两次尺度归一（见文件头注）。
 * 必须排在 Table 扩展之后（插件/appendTransaction 顺序）。
 *
 * ⚠️ 时序（实测踩坑）：归一化事务会 setNodeMarkup 重写单元格，官方 ResizeState
 * 在 docChanged 时可能把 activeHandle 映射为 -1；若在**捕获阶段**派发，官方
 * mousedown 随后直接 return，本次拖拽失效。因此捕获阶段只**记录**显示宽，
 * 归一化放到冒泡阶段（官方 handler 之后）执行，并显式恢复把手 + startWidth。
 * 两次派发都在同一个 mousedown 事件内、下一帧绘制前完成，用户无闪烁。
 */
export const TablePctResize = Extension.create({
  name: 'tablePctResize',

  addProseMirrorPlugins () {
    return [
      new Plugin({
        key: tablePctResizeKey,
        view: (editorView) => {
          /** 捕获阶段暂存：按下前的显示域快照 */
          let pendingDrag = null
          /** 进行中的实时预览清理函数（新拖拽/销毁时先清旧的） */
          let stopLiveDrag = null

          const onMouseDownCapture = (event) => {
            pendingDrag = null
            if (event.button !== 0 || !editorView.editable) return
            const resize = columnResizingPluginKey.getState(editorView.state)
            if (!resize || resize.activeHandle < 0 || resize.dragging) return
            let $cell
            try {
              $cell = editorView.state.doc.resolve(resize.activeHandle)
            } catch {
              return
            }
            const tableDom = findTableDom(editorView, $cell)
            if (!tableDom) return
            const table = $cell.node(-1)
            if (!table || table.type.name !== 'table') return
            const tableWidth = Math.round(tableDom.getBoundingClientRect().width)
            if (!(tableWidth > 0)) return
            // 显示宽优先由「当前渲染的百分比」反推（与 PctTableView 同口径），
            // 这样跨列单元格内部的列比例也不会被 DOM 均摊破坏；
            // 从未拖过的表格（无显式列宽）没有百分比，退回实测首行宽度。
            const stored = colWidthsFromTableNode(table)
            const pct = percentsFromPx(stored)
            const displayWidths = pct
              ? pct.map((p) => (p / 100) * tableWidth)
              : measureDisplayWidths(tableDom)
            if (!displayWidths || !displayWidths.length) return
            pendingDrag = { handle: resize.activeHandle, displayWidths, tableWidth }
          }

          // 冒泡阶段：挂到 document 上，晚于 ProseMirror 在 view.dom 上的核心
          // mousedown（官方 columnResizing 已启动拖拽）——比同节点监听器顺序更稳
          const onMouseDownBubble = (event) => {
            if (!editorView.dom.contains(event.target)) {
              pendingDrag = null
              return
            }
            const info = pendingDrag
            pendingDrag = null
            if (!info) return
            const resize = columnResizingPluginKey.getState(editorView.state)
            if (!resize || !resize.dragging) return
            /** 桥接数据异常时撤销本次拖拽，避免退回 R4 的错位行为 */
            const cancelDrag = () => {
              editorView.dispatch(editorView.state.tr.setMeta(columnResizingPluginKey, { setDragging: null }))
            }
            const { startX } = resize.dragging
            const col = draggedColumnIndex(editorView.state, info.handle)
            if (col < 0) return cancelDrag()
            let $cell
            try {
              $cell = editorView.state.doc.resolve(info.handle)
            } catch {
              return cancelDrag()
            }
            const table = $cell.node(-1)
            if (!table || table.type.name !== 'table') return cancelDrag()
            const tablePos = $cell.before(-1)
            const stored = colWidthsFromTableNode(table)
            if (stored.length !== info.displayWidths.length) return cancelDrag()
            const storedSum = stored.reduce((a, b) => a + b, 0)
            const needsNormalize = stored.some((w) => !(w > 0)) || Math.abs(storedSum - info.tableWidth) > 1

            let handle = info.handle
            if (needsNormalize) {
              const widths = fitWidthsToTotal(info.displayWidths, info.tableWidth)
              if (!widths) return cancelDrag()
              const tr = editorView.state.tr
              if (!applyTableColWidths(tr, tablePos, widths)) return cancelDrag()
              // 纯尺度归一（比例不变）：不进撤销栈，且**不参与**拖拽后的总宽重平衡
              // （否则 appendTransaction 会把归一化的差值再摊回其他列，破坏比例）
              tr.setMeta('addToHistory', false)
              tr.setMeta(tablePctResizeKey, { normalize: true })
              editorView.dispatch(tr)
              handle = tr.mapping.map(info.handle, -1)
            }

            // 归一化事务可能把手映射失效（见上方时序说明）→ 显式恢复
            const after = columnResizingPluginKey.getState(editorView.state)
            if (!after || after.activeHandle < 0) {
              editorView.dispatch(editorView.state.tr.setMeta(columnResizingPluginKey, { setHandle: handle }))
            }
            // startWidth 改读「屏幕显示宽」，官方后续增量才能 1:1
            const startWidth = info.displayWidths[col] || resize.dragging.startWidth
            editorView.dispatch(editorView.state.tr.setMeta(columnResizingPluginKey, {
              setDragging: { startX, startWidth }
            }))

            // 拖动中的实时预览：官方 mousemove 先把 DOM 写成 px（表宽 = px 总和），
            // 本监听器注册更晚、在同一事件内覆写为「总宽守恒的百分比」，
            // 表格全程保持容器宽，右侧列实时收窄，松开不再回跳。
            const win = editorView.dom.ownerDocument.defaultView
            if (win) {
              if (stopLiveDrag) stopLiveDrag()
              function stopLive () {
                win.removeEventListener('mousemove', onLiveMove, false)
                win.removeEventListener('mouseup', stopLive, false)
                if (stopLiveDrag === stopLive) stopLiveDrag = null
              }
              function onLiveMove (moveEvent) {
                if (!moveEvent.which) return stopLive()
                const st = columnResizingPluginKey.getState(editorView.state)
                if (!st || !st.dragging) return stopLive()
                let $c
                try {
                  $c = editorView.state.doc.resolve(handle)
                } catch {
                  return stopLive()
                }
                const tableNode = $c.node(-1)
                const tableDom = findTableDom(editorView, $c)
                if (!tableNode || !tableDom) return stopLive()
                const cur = colWidthsFromTableNode(tableNode)
                const total = Math.round(cur.reduce((a, b) => a + b, 0))
                if (!(total > 0) || cur.some((w) => !(w > 0))) return stopLive()
                const raw = cur.slice()
                raw[col] = Math.max(
                  PCT_RESIZE_MIN_PX,
                  Math.round(st.dragging.startWidth + (moveEvent.clientX - st.dragging.startX))
                )
                const live = rebalanceColumnWidths(raw, total, new Set([col]), minColumnWidth(total))
                if (live) applyLiveColumnPreview(tableDom, live, total)
              }
              win.addEventListener('mousemove', onLiveMove, false)
              win.addEventListener('mouseup', stopLive, false)
              stopLiveDrag = stopLive
            }
          }

          const ownerDoc = editorView.dom.ownerDocument
          editorView.dom.addEventListener('mousedown', onMouseDownCapture, true)
          ownerDoc.addEventListener('mousedown', onMouseDownBubble, false)
          return {
            destroy () {
              if (stopLiveDrag) stopLiveDrag()
              editorView.dom.removeEventListener('mousedown', onMouseDownCapture, true)
              ownerDoc.removeEventListener('mousedown', onMouseDownBubble, false)
            }
          }
        },
        appendTransaction: (transactions, oldState, newState) => {
          // 归一化事务（尺度换算）不做重平衡：比例本来就没变
          if (transactions.some((tr) => tr.getMeta(tablePctResizeKey)?.normalize)) return null
          const resize = columnResizingPluginKey.getState(newState)
          if (!resize || !resize.dragging || resize.activeHandle < 0) return null
          if (!transactions.some((tr) => tr.docChanged)) return null
          return rebalanceAfterDrag(oldState, newState, resize.activeHandle)
        }
      })
    ]
  }
})
