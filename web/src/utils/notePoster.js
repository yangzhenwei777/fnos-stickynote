import { sanitizeNoteHtml } from './sanitize'

/**
 * notePoster — 笔记导出长图引擎（v2.2.2「导出为长图」）。
 *
 * ## 设计要点
 * - **离屏容器**：不复用预览区 DOM（宽度随窗口、暗色变量会渗入、带交互元素）。
 *   独立 `.note-poster` 命名空间 + 显式亮色配色（分享/打印通用，不跟随主题），
 *   挂到 body 后由 SnapDOM 捕获，完毕即移除。
 * - **渲染库**：@zumer/snapdom（SVG foreignObject，浏览器原生绘制；MIT、零运行时依赖）。
 *   动态 import() —— 仅点击导出时才加载，首屏 bundle 不变。
 * - **超长笔记**：`capture.toBlob()` 走 SnapDOM 内置分窗编码（PNG 自动超 canvas 上限出完整图，
 *   刀 1 Spike 实测 59417 CSS px @ dpr2 → 1340×118834 完整产物）。
 *   ⚠ 不可用 toPng（单画布守卫会把超高图压到 32767px 物理高）。
 * - **dpr 自适应**：高度越大清晰度降档（≤8000→2 / ≤16000→1.5 / 其余→1），平衡体积与锐度。
 * - **checkbox 静态化**：任务清单的原生 `input[type=checkbox]` 在 SVG foreignObject 内
 *   跨内核渲染不稳（Chromium 实测正常，iOS WebKit 已知坏点），替换为纯 DOM 方框符号。
 * - **图片绝对化**：子路径部署下相对 src 序列化进 SVG 后按 data URL 的 base 解析会 404，
 *   统一 `new URL(src, location.href)` 绝对化（只改克隆副本，不动笔记数据）。
 * - 图片全同源（sanitize 白名单仅 /uploads/）→ 无跨域 canvas 污染问题；/uploads 鉴权
 *   走 cookie 通道，浏览器自动携带（desktop/Wails 同源同理）。
 * - 图片不落盘、不进备份/配额；纯前端内存产物经 saveBlob 下载（与 MD 导出同通道）。
 */

/** 导出图固定宽度（CSS px） */
export const POSTER_WIDTH = 800

/** 海报左右内边距（与 buildPosterElement 的 padding 保持一致） */
const POSTER_PADDING_X = 56

/** 海报内容可用宽度（CSS px） */
export const POSTER_CONTENT_WIDTH = POSTER_WIDTH - POSTER_PADDING_X * 2

/** 文件名最大长度（超出截断，含扩展名前） */
const NAME_MAX = 80

/**
 * 按内容高度选 dpr（刀 1 实测口径：宽度 800 CSS px 时 Chrome 单边上限对应 16383 CSS px @ dpr2）。
 * @param {number} cssHeight 容器内容高度（CSS px）
 * @returns {number} 2 | 1.5 | 1
 */
export function pickDpr (cssHeight) {
  if (cssHeight <= 8000) return 2
  if (cssHeight <= 16000) return 1.5
  return 1
}

/**
 * 文件名清洗：替换非法字符、压缩空白、限长；空标题兜底「未命名」。
 * @param {string} title
 * @returns {string}
 */
export function sanitizeFilename (title) {
  const cleaned = String(title || '')
    .replace(/[\\/:*?"<>|\r\n]+/g, '_')
    .replace(/\s+/g, ' ')
    .trim()
    .replace(/[.\s]+$/, '')
    .slice(0, NAME_MAX)
    .replace(/[.\s]+$/, '')
  // 清洗后只剩占位下划线（如标题全为非法字符）视同空标题
  return /^_+$/.test(cleaned) ? '未命名' : (cleaned || '未命名')
}

/**
 * 本地时间格式化 YYYY-MM-DD HH:mm。
 * @param {Date|string|number} d
 * @returns {string}
 */
export function formatPosterDate (d) {
  const date = d ? new Date(d) : new Date()
  if (Number.isNaN(date.getTime())) return ''
  const p = (n) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${p(date.getMonth() + 1)}-${p(date.getDate())} ${p(date.getHours())}:${p(date.getMinutes())}`
}

/** 文件名日期段 YYYYMMDD */
function fileStamp (d) {
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}`
}

/** 长图文件名：标题-YYYYMMDD.png */
export function posterFilename (title, now = new Date()) {
  return `${sanitizeFilename(title)}-${fileStamp(now)}.png`
}

/**
 * 任务清单 checkbox 静态化（仅改容器副本，不动笔记数据）。
 * 原生 input 在 foreignObject 内跨内核渲染不稳，替换为纯 DOM 方框。
 * @param {HTMLElement} root
 */
export function staticizeCheckboxes (root) {
  root.querySelectorAll('input[type="checkbox"]').forEach((input) => {
    const checked = input.hasAttribute('checked') && input.getAttribute('checked') !== 'false'
    const span = document.createElement('span')
    span.className = checked ? 'poster-check is-checked' : 'poster-check'
    span.textContent = checked ? '✓' : ''
    input.replaceWith(span)
  })
}

/**
 * 图片 URL 绝对化（仅改容器副本）：相对路径序列化进 SVG data URL 后会按 data: base 解析 → 404。
 * @param {HTMLElement} root
 */
export function absolutizeImages (root) {
  root.querySelectorAll('img').forEach((img) => {
    const src = img.getAttribute('src') || ''
    if (!src || /^(data:|blob:|https?:)/i.test(src)) return
    try {
      img.setAttribute('src', new URL(src, window.location.href).href)
    } catch { /* 非法 URL 保留原样，由捕获层 fallback */ }
  })
}

/**
 * 等待容器内图片解码完成（否则长图出现空白块）；失败不阻塞（保留占位）。
 * @param {HTMLElement} root
 */
export async function waitForImages (root) {
  const imgs = Array.from(root.querySelectorAll('img'))
  await Promise.all(imgs.map((img) => {
    try {
      if (img.decode) return img.decode().catch(() => {})
    } catch { /* jsdom 无 decode，直接过 */ }
    return Promise.resolve()
  }))
}

/**
 * 海报代码字体栈（v2.2.2 R7）。
 *
 * ⚠️ 必须写字面量，禁止改成 `var(--font-code)`：POSTER_CSS 会随海报子树一起被
 * SnapDOM 克隆进 SVG，而 `:root` 上的自定义属性不在克隆文档里；同时本文件的
 * `.poster-body code` 选择器优先级高于 SnapDOM 生成的 `.cN` 类，`var()` 解析失败时
 * 会回退到无中文回退的 mono 栈 → Windows 用 NSimSun（新宋体），长图中英割裂。
 * 与 global.css 的 `--font-code` 保持同一字体顺序（编辑/预览/长图三处观感一致）。
 */
export const POSTER_CODE_FONT = "'Sarasa Mono SC', 'Noto Sans Mono CJK SC', 'Cascadia Mono', ui-monospace, SFMono-Regular, Consolas, 'SF Mono', Menlo, 'Microsoft YaHei UI', 'Microsoft YaHei', 'PingFang SC', 'Noto Sans CJK SC', monospace"

/** 导出容器内的排版样式（.note-poster 命名空间，全部显式亮色，不引用主题 CSS 变量） */
const POSTER_CSS = `
.note-poster{position:fixed;left:-10000px;top:0;z-index:-1;background:#ffffff;color:#1f2328;
  font-family:-apple-system,'Segoe UI','PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif;
  font-size:15px;line-height:1.75;box-sizing:border-box;}
.note-poster,.note-poster *,.note-poster *::before,.note-poster *::after{box-sizing:border-box;}
.note-poster .poster-head{border-bottom:1px solid #e5e7eb;padding-bottom:16px;margin-bottom:24px;}
.note-poster .poster-title{font-size:24px;font-weight:700;color:#111827;line-height:1.4;word-break:break-word;}
.note-poster .poster-meta{margin-top:8px;font-size:12px;color:#9ca3af;}
.note-poster .poster-body{word-break:break-word;}
.note-poster .poster-body p{margin:0 0 12px;}
.note-poster .poster-body h1,.note-poster .poster-body h2,.note-poster .poster-body h3,
.note-poster .poster-body h4,.note-poster .poster-body h5,.note-poster .poster-body h6{
  margin:20px 0 10px;font-weight:700;color:#111827;line-height:1.4;}
.note-poster .poster-body h1{font-size:21px;}.note-poster .poster-body h2{font-size:19px;}
.note-poster .poster-body h3{font-size:17px;}.note-poster .poster-body h4{font-size:15px;}
.note-poster .poster-body h5,.note-poster .poster-body h6{font-size:14px;color:#4b5563;}
.note-poster .poster-body ul,.note-poster .poster-body ol{margin:0 0 12px;padding-left:1.6em;}
.note-poster .poster-body li{margin:4px 0;}
.note-poster .poster-body ul[data-type="taskList"]{list-style:none;padding-left:0;}
.note-poster .poster-body ul[data-type="taskList"] li{list-style:none;padding-left:0;
  display:flex;align-items:flex-start;}
.note-poster .poster-body ul[data-type="taskList"] li>div{flex:1;min-width:0;}
.note-poster .poster-body ul[data-type="taskList"] li>div>p:last-child{margin-bottom:0;}
.note-poster .poster-body label{display:inline-flex;align-items:center;margin-right:6px;vertical-align:middle;}
.note-poster .poster-body .poster-check{display:inline-flex;align-items:center;justify-content:center;
  width:16px;height:16px;border:1.5px solid #9ca3af;border-radius:4px;background:#ffffff;
  font-size:12px;line-height:1;color:transparent;flex:none;margin-top:5px;}
.note-poster .poster-body .poster-check.is-checked{background:#2563eb;border-color:#2563eb;color:#ffffff;}
.note-poster .poster-body blockquote{margin:0 0 12px;padding:8px 16px;border-left:4px solid #d1d5db;
  background:#f9fafb;color:#4b5563;}
.note-poster .poster-body pre{margin:0 0 12px;padding:14px 16px;background:#f6f8fa;
  border:1px solid #e5e7eb;border-radius:8px;
  /* 长图为静态 PNG 无滚动交互，超宽行必须软换行才能完整呈现（区别于预览态的横向滚动） */
  white-space:pre-wrap;word-break:break-all;overflow-wrap:anywhere;}
.note-poster .poster-body code{font-family:${POSTER_CODE_FONT};
  font-size:13px;}
.note-poster .poster-body :not(pre)>code{background:#f3f4f6;border:1px solid #e5e7eb;border-radius:4px;
  padding:1px 5px;}
.note-poster .poster-body table{border-collapse:collapse;width:100%;margin:0 0 12px;}
/* 编辑器拖宽的表格（sanitize 标记 data-has-colwidth）按 fixed 布局保持列宽。
   v2.2.2 R4（方案 A）：列宽已是百分比 → 撑满海报正文宽即可，列宽比例与预览/编辑器
   完全一致（不再依赖 applyPreviewMetrics 的像素实测投影）。 */
.note-poster .poster-body table[data-has-colwidth]{table-layout:fixed;width:100%;}
.note-poster .poster-body table[data-has-colwidth] td,
.note-poster .poster-body table[data-has-colwidth] th{overflow-wrap:anywhere;}
.note-poster .poster-body th,.note-poster .poster-body td{border:1px solid #e5e7eb;padding:8px 12px;text-align:left;}
.note-poster .poster-body th{background:#f9fafb;font-weight:600;}
.note-poster .poster-body img{max-width:100%;height:auto;border-radius:6px;margin:4px 0;}
.note-poster .poster-body mark{background:#fef08a;color:#1f2328;padding:0 2px;border-radius:2px;}
.note-poster .poster-body a{color:#2563eb;text-decoration:none;}
.note-poster .poster-body hr{border:none;border-top:1px solid #e5e7eb;margin:16px 0;}
.note-poster .poster-body div[data-youtube-video],.note-poster .poster-body iframe{max-width:100%;}
/* 极简代码高亮（GitHub Light 风格子集，覆盖 lowlight 常用 token） */
.note-poster .hljs-keyword,.note-poster .hljs-selector-tag,.note-poster .hljs-literal,
.note-poster .hljs-section,.note-poster .hljs-doctag,.note-poster .hljs-type,.note-poster .hljs-name{color:#cf222e;}
.note-poster .hljs-string,.note-poster .hljs-title.class_,.note-poster .hljs-title.function_,
.note-poster .hljs-attr,.note-poster .hljs-attribute,.note-poster .hljs-symbol,.note-poster .hljs-addition{color:#0a3069;}
.note-poster .hljs-title,.note-poster .hljs-title_,{color:#953800;}
.note-poster .hljs-comment,.note-poster .hljs-quote,.note-poster .hljs-deletion{color:#6e7781;font-style:italic;}
.note-poster .hljs-number,.note-poster .hljs-regexp,.note-poster .hljs-variable,
.note-poster .hljs-template-variable,.note-poster .hljs-built_in{color:#0550ae;}
.note-poster .poster-foot{margin-top:28px;padding-top:12px;border-top:1px solid #e5e7eb;
  font-size:12px;color:#9ca3af;text-align:center;}
`

/**
 * 将「预览态实测宽度」按比例投影到海报容器（v2.2.2 R3 引入，R4 起仅用于**图片**）。
 *
 * 表格：R4 起列宽在消毒层已投影为百分比、表格 `width:100%`，海报与预览天然同比例，
 * 此处**不再**改写表格列宽（改写会退化成 px，重新引入容器宽度依赖）。
 * 图片：仍按 k = min(1, 海报内容宽 / 预览内容宽) 缩放，保持「图片占正文的比例」一致。
 *
 * @param {HTMLElement} body 海报正文容器（已写入消毒后的 HTML）
 * @param {{contentWidth:number, tables?:number[][], images:number[]}|null} metrics 预览实测
 * @param {number} posterContentWidth 海报内容可用宽度
 * @returns {boolean} 是否实际应用（false = 无实测数据，退回 CSS 默认布局）
 */
export function applyPreviewMetrics (body, metrics, posterContentWidth = POSTER_CONTENT_WIDTH) {
  if (!body || !metrics || !(metrics.contentWidth > 0) || !(posterContentWidth > 0)) return false
  const k = Math.min(1, posterContentWidth / metrics.contentWidth)
  if (!(k > 0)) return false
  let applied = false

  const imgs = body.querySelectorAll('img')
  imgs.forEach((img, ii) => {
    const w = metrics.images && metrics.images[ii]
    if (!(w > 0)) return
    const px = Math.max(24, Math.round(Math.min(w * k, posterContentWidth)))
    img.setAttribute('width', String(px))
    img.style.width = `${px}px`
    applied = true
  })

  return applied
}

/**
 * 构建离屏导出容器（头部标题+日期 / 正文 / 页脚）。
 * 标题等文本一律 textContent 写入防注入；正文 HTML 必须先过 sanitizeNoteHtml。
 * @param {{title:string, html:string, updated:string, created:string, width?:number, withHeader?:boolean, exportAt?:Date, previewMetrics?:object}} opts
 * @returns {HTMLElement}
 */
export function buildPosterElement (opts) {
  const {
    title, html, updated, created,
    width = POSTER_WIDTH, withHeader = true, exportAt = new Date(), previewMetrics = null
  } = opts

  const root = document.createElement('div')
  root.className = 'note-poster'
  root.setAttribute('data-theme', 'light')
  root.style.width = `${width}px`
  root.style.padding = '48px 56px 40px'

  const style = document.createElement('style')
  style.textContent = POSTER_CSS
  root.appendChild(style)

  if (withHeader) {
    const head = document.createElement('div')
    head.className = 'poster-head'
    const h = document.createElement('div')
    h.className = 'poster-title'
    h.textContent = String(title || '')
    const meta = document.createElement('div')
    meta.className = 'poster-meta'
    const when = updated || created || ''
    meta.textContent = when ? `更新于 ${when}` : ''
    head.appendChild(h)
    head.appendChild(meta)
    root.appendChild(head)
  }

  const body = document.createElement('div')
  body.className = 'poster-body'
  body.innerHTML = html
  // v2.2.2 R3：按预览实测宽度等比投影（表格列宽 / 图片宽度），无实测则退回 CSS 默认
  applyPreviewMetrics(body, previewMetrics, width - POSTER_PADDING_X * 2)
  staticizeCheckboxes(body)
  absolutizeImages(body)
  root.appendChild(body)

  const foot = document.createElement('div')
  foot.className = 'poster-foot'
  foot.textContent = `Sticky note · 导出于 ${formatPosterDate(exportAt)}`
  root.appendChild(foot)

  return root
}

/**
 * blob 触发浏览器下载（与 MD 导出同通道；桌面壳 WebView2 行为一致）。
 * @param {Blob} blob
 * @param {string} filename
 */
export function saveBlobDownload (blob, filename) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/**
 * 导出一篇笔记为长图 PNG。
 * @param {{title:string, content:string, updated_at?:string, created_at?:string}} note 已解密明文笔记
 * @param {{width?:number, withHeader?:boolean, previewMetrics?:object}} [opts]
 *   previewMetrics：预览态实测（contentWidth/tables/images），用于把表格列宽与图片宽度
 *   等比投影到 800px 海报（见 applyPreviewMetrics）；缺省则按 CSS 默认布局导出。
 * @returns {Promise<{filename:string, blob:Blob, dpr:number, cssHeight:number, warnings:Array, sizeMB:number}>}
 *   warnings 非空时由调用方决定提示文案（如分窗降级 / 图片占位）。
 */
export async function exportNotePoster (note, opts = {}) {
  const { width = POSTER_WIDTH, withHeader = true, previewMetrics = null } = opts
  if (!note || !note.content) throw new Error('笔记内容为空')

  const html = sanitizeNoteHtml(note.content)
  const exportAt = new Date()
  const el = buildPosterElement({
    title: note.title,
    html,
    updated: formatPosterDate(note.updated_at),
    created: formatPosterDate(note.created_at),
    width,
    withHeader,
    exportAt,
    previewMetrics
  })
  document.body.appendChild(el)

  try {
    // 等样式应用与图片解码（双 rAF 保证 layout 完成）
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
    await waitForImages(el)

    const cssHeight = el.scrollHeight
    const dpr = pickDpr(cssHeight)
    const { snapdom } = await import('@zumer/snapdom')
    const capture = await snapdom(el, { reconcile: true, embedFonts: true })
    const blob = await capture.toBlob({ format: 'png', dpr })
    if (!blob || !blob.size) throw new Error('图片编码失败')
    const warnings = Array.isArray(capture.warnings) ? capture.warnings : []
    const filename = posterFilename(note.title, exportAt)
    saveBlobDownload(blob, filename)
    return {
      filename, blob, dpr, cssHeight, warnings,
      sizeMB: Math.round(blob.size / 1024 / 1024 * 10) / 10
    }
  } finally {
    el.remove()
  }
}
