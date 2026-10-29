// tableColPct.js — 表格列宽的「比例投影」（v2.2.2 R4，方案 A）。
//
// ## 为什么需要
// 列宽此前以**绝对像素**存储（`<col style="width:180px">` / 单元格 `colwidth="180"`），
// 而「未拖过的列」只有 min-width、靠**剩余空间**撑开。于是同一张表在
// 编辑区 / 编辑内预览 / 查看态 / 长图(固定 800px) / md 查看器 五处容器宽度不同、
// 布局算法不同（auto / fixed），必然长得不一样：未拖列要么塌成竖排单字、
// 要么被撑得极宽（用户 R4 实测反馈）。
//
// ## 本方案（R4）
// **存储不变（px 保留，零 schema/零白名单变更）**，但**所有渲染面统一投影为百分比**：
// 每列按 px 比例换成「占表格的百分比」，表格 `table-layout:fixed + width:100%`。
// 这样列宽只表达**内部比例**，与容器宽度彻底解耦——任何窗口/任何导出都同比例。
//
// 实测（Chromium）：百分比列宽在 300/423/670/688/1000/1600px 容器下比例完全一致；
// 而 px 列宽在「容器更窄」时会溢出、「更宽」时只在 sum ≤ 容器时等比（不可用）。
//
// ## 未拖列的补齐规则（确定性，跨端一致）
// 未指定 px 的列按**已指定列的平均宽度**补齐后再算比例（不是 min-width 25px，
// 否则会被压成竖排单字）。JS 与 Go 两侧实现同一规则，保证预览/长图/md 一致。
//
// ## 幂等
// 已投影为百分比的列（`style="width:37.5%"`）直接归一化到 100 后返回，不重复换算。

/** 单列宽度解析结果：px 或 pct（不支持混合单位） */
const COL_WIDTH_RE = /^(\d+(?:\.\d+)?)(px|%)?$/

/**
 * 把 px 宽度数组换算为百分比（合计 100）。
 * 未指定（0/null/负数）的列按**已指定列平均值**补齐；全未指定返回 null（保持自适应）。
 * @param {Array<number|null|undefined>} widthsPx
 * @returns {number[]|null} 例：[37.5, 30, 32.5]
 */
export function percentsFromPx (widthsPx) {
  if (!Array.isArray(widthsPx) || !widthsPx.length) return null
  const nums = widthsPx.map((w) => (Number.isFinite(w) && w > 0 ? w : 0))
  const explicit = nums.filter((n) => n > 0)
  if (!explicit.length) return null
  const avg = explicit.reduce((a, b) => a + b, 0) / explicit.length
  const filled = nums.map((n) => (n > 0 ? n : avg))
  const total = filled.reduce((a, b) => a + b, 0)
  if (!(total > 0)) return null
  return normalizeTo100(filled.map((n) => (n / total) * 100))
}

/**
 * 百分比数组归一化到合计 100（保留 1 位小数），舍入差额并入最大列。
 * @param {number[]} pcts
 * @returns {number[]}
 */
export function normalizeTo100 (pcts) {
  const rounded = pcts.map((p) => Math.round(p * 10) / 10)
  const sum = Math.round(rounded.reduce((a, b) => a + b, 0) * 10) / 10
  const drift = Math.round((100 - sum) * 10) / 10
  if (drift !== 0) {
    let mi = 0
    for (let i = 1; i < rounded.length; i++) {
      if (rounded[i] > rounded[mi]) mi = i
    }
    rounded[mi] = Math.round((rounded[mi] + drift) * 10) / 10
  }
  return rounded
}

/**
 * 读取单个 `<col>` 的显式宽度：返回 `{px}` / `{pct}`，未指定返回 null。
 * @param {Element} col
 * @returns {{px?:number, pct?:number}|null}
 */
export function readColWidth (col) {
  const raw = ((col && col.style && col.style.width) || '').trim()
  const m = COL_WIDTH_RE.exec(raw)
  if (!m) return null
  const n = parseFloat(m[1])
  if (!Number.isFinite(n) || n <= 0) return null
  return m[2] === '%' ? { pct: n } : { px: n }
}

/**
 * 依据 `colgroup > col` 解出列百分比：全部为 % → 归一化；含 px → 按平均补齐换算。
 * @param {Element[]|NodeList} cols
 * @returns {number[]|null}
 */
export function resolveColPercents (cols) {
  const list = Array.from(cols || [])
  if (!list.length) return null
  const parsed = list.map(readColWidth)
  if (parsed.every((p) => p === null)) return null
  if (parsed.every((p) => p === null || p.pct !== undefined)) {
    // 已是百分比（可能个别列缺失）→ 用已指定均值补齐再归一化
    return percentsFromPx(parsed.map((p) => (p && p.pct ? p.pct : 0)))
  }
  // 存量 px 形态（含混合）→ 统一按 px 处理（% 值按 px 数值参与，仅作兜底）
  return percentsFromPx(parsed.map((p) => (p ? (p.px !== undefined ? p.px : p.pct) : 0)))
}

/**
 * 把整段 HTML 中「带显式列宽」的表格列宽投影为百分比（**只读展示用**，
 * 不用于存储）。已符合百分比形态时原样返回；无表格列宽时零开销直通。
 * @param {string} html
 * @returns {string}
 */
export function projectColPercentsInHtml (html) {
  if (!html || typeof html !== 'string' || !html.includes('<colgroup')) return html
  const tpl = document.createElement('template')
  tpl.innerHTML = html
  let changed = false
  tpl.content.querySelectorAll('table').forEach((table) => {
    const cols = Array.from(table.querySelectorAll('colgroup > col'))
    if (!cols.length) return
    const pct = resolveColPercents(cols)
    if (!pct) return
    cols.forEach((col, i) => {
      const next = `${pct[i]}%`
      if (col.style.width !== next) {
        col.style.width = next
        changed = true
      }
      if (col.style.minWidth) {
        col.style.minWidth = ''
        changed = true
      }
    })
  })
  return changed ? tpl.innerHTML : html
}
