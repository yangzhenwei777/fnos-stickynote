import { TableCell, TableHeader } from '@tiptap/extension-table'

// tableExt.js — 表格单元格列宽回读扩展（v2.2.2 R3）。
//
// 为什么需要：Tiptap v3 的 TableCell/TableHeader `colwidth.parseHTML` 只认两条通道——
//   ① 单元格自身的 `colwidth` 属性（首选，服务端白名单 R2 起放行）
//   ② `colgroup > col` 的 **width 属性**（回退；注意**只认属性、不认 style**）
// 而 Tiptap 自身序列化写的是 `<col style="width:180px">`，R2 之前入库的存量内容
// 也只有 style 一条通道 ⇒ 重编辑时列宽全丢（用户实测缺陷）。
//
// R2 曾用「载入前正则改写 HTML」补写 width 属性，因正则未吞掉标签收尾 `>` 而
// 在 colgroup 内残留 `>` 文本，演变成「点一次编辑多一行 >」的 P0 缺陷（见
// tableColWidth.js 头注）。R3 改为**在节点解析层**扩展第三条通道（col 的 style），
// 不改写任何内容字符串：载入即正确、序列化自然收敛到规范形态、往返幂等。

/** col 的 style 中的显式像素宽度（min-width 不算显式列宽） */
const COL_STYLE_WIDTH_RE = /(?:^|;)\s*width\s*:\s*(\d+(?:\.\d+)?)px\s*(?:;|$)/i

/**
 * 从 `colgroup > col` 读显式像素宽度：先 width 属性，再 style 的 width 声明。
 * @param {Element|null} col
 * @returns {number|null}
 */
export function readColWidthFromCol (col) {
  if (!col || !col.getAttribute) return null
  const attr = (col.getAttribute('width') || '').trim()
  if (attr && /^\d+(?:\.\d+)?$/.test(attr)) return Math.round(parseFloat(attr))
  const m = COL_STYLE_WIDTH_RE.exec(col.getAttribute('style') || '')
  return m ? Math.round(parseFloat(m[1])) : null
}

/**
 * 单元格列宽解析（三通道通吃）：
 *  1. `colwidth="180"` / `colwidth="180,120"`（跨合单元格，逗号分隔）
 *  2. 同列 `col` 的 `width` 属性（Tiptap 官方回退口径）
 *  3. 同列 `col` 的 `style="width:180px"`（本仓存量入库形态，R3 新增通道）
 * @param {Element} element th/td 元素
 * @returns {number[]|null}
 */
export function parseCellColWidth (element) {
  const raw = element.getAttribute && element.getAttribute('colwidth')
  if (raw) {
    const arr = raw.split(',').map(v => parseInt(v, 10)).filter(n => Number.isFinite(n) && n > 0)
    if (arr.length) return arr
  }
  const row = element.parentElement
  const table = element.closest && element.closest('table')
  if (!row || !table) return null
  const idx = Array.from(row.children).indexOf(element)
  if (idx < 0) return null
  const col = table.querySelectorAll('colgroup > col')[idx]
  const w = readColWidthFromCol(col)
  return w ? [w] : null
}

/** 在节点基础上覆写 colwidth 解析器（保留父级其余属性定义） */
function withColWidthParser (Base) {
  return Base.extend({
    addAttributes () {
      return {
        ...(this.parent ? this.parent() : {}),
        colwidth: {
          default: null,
          parseHTML: parseCellColWidth
        }
      }
    }
  })
}

export const StickyTableCell = withColWidthParser(TableCell)
export const StickyTableHeader = withColWidthParser(TableHeader)
