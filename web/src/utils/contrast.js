// v21.118 评审修复（问题①）：文字色 WCAG 对比度自适应纯函数。
// 策略：保色相(H)与饱和度(S)不变，仅调整明度(L)，逼近到与背景对比度 ≥ 阈值。
// 纯计算、无副作用；实际"仅暗色覆盖显示、不改存储/导出"由 autoContrast.js 负责。

export function parseColor(str) {
  if (!str) return null
  const s = String(str).trim().toLowerCase()
  let m = s.match(/^#([0-9a-f]{3})$/)
  if (m) {
    const h = m[1]
    return { r: parseInt(h[0] + h[0], 16), g: parseInt(h[1] + h[1], 16), b: parseInt(h[2] + h[2], 16) }
  }
  m = s.match(/^#([0-9a-f]{6})$/)
  if (m) {
    const h = m[1]
    return { r: parseInt(h.slice(0, 2), 16), g: parseInt(h.slice(2, 4), 16), b: parseInt(h.slice(4, 6), 16) }
  }
  m = s.match(/^rgba?\(\s*(\d+)[,\s]+(\d+)[,\s]+(\d+)/)
  if (m) return { r: +m[1], g: +m[2], b: +m[3] }
  return null
}

export function rgbToHex({ r, g, b }) {
  const c = (n) => Math.max(0, Math.min(255, Math.round(n))).toString(16).padStart(2, '0')
  return `#${c(r)}${c(g)}${c(b)}`
}

function chan(v) {
  v /= 255
  return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4)
}
export function relativeLuminance(rgb) {
  return 0.2126 * chan(rgb.r) + 0.7152 * chan(rgb.g) + 0.0722 * chan(rgb.b)
}
export function contrastRatio(a, b) {
  const la = relativeLuminance(a), lb = relativeLuminance(b)
  const hi = Math.max(la, lb), lo = Math.min(la, lb)
  return (hi + 0.05) / (lo + 0.05)
}

function rgbToHsl({ r, g, b }) {
  r /= 255; g /= 255; b /= 255
  const max = Math.max(r, g, b), min = Math.min(r, g, b)
  let h = 0, s = 0
  const l = (max + min) / 2
  const d = max - min
  if (d !== 0) {
    s = d / (1 - Math.abs(2 * l - 1))
    if (max === r) h = ((g - b) / d) % 6
    else if (max === g) h = (b - r) / d + 2
    else h = (r - g) / d + 4
    h *= 60; if (h < 0) h += 360
  }
  return { h, s, l }
}
function hslToRgb({ h, s, l }) {
  const c = (1 - Math.abs(2 * l - 1)) * s
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1))
  const m = l - c / 2
  let r, g, b
  if (h < 60) [r, g, b] = [c, x, 0]
  else if (h < 120) [r, g, b] = [x, c, 0]
  else if (h < 180) [r, g, b] = [0, c, x]
  else if (h < 240) [r, g, b] = [0, x, c]
  else if (h < 300) [r, g, b] = [x, 0, c]
  else [r, g, b] = [c, 0, x]
  return { r: (r + m) * 255, g: (g + m) * 255, b: (b + m) * 255 }
}

// 若 fg 与 bg 对比已 ≥ min 或无法解析，返回 null（不改）；否则返回保色相提亮后的 #rrggbb。
export function ensureContrast(fg, bg, min = 4.5) {
  const f = parseColor(fg), b = parseColor(bg)
  if (!f || !b) return null
  if (contrastRatio(f, b) >= min) return null
  const { h, s, l } = rgbToHsl(f)
  // 背景偏暗→提亮 L；背景偏亮→压暗 L。逐步逼近阈值。
  const toward = relativeLuminance(b) < 0.5 ? 1 : 0
  const step = toward === 1 ? 0.02 : -0.02
  let cur = l
  for (let i = 0; i <= 60; i++) {
    cur = Math.max(0, Math.min(1, cur + step))
    const cand = hslToRgb({ h, s, l: cur })
    if (contrastRatio(cand, b) >= min) return rgbToHex(cand)
    if (cur === toward) break
  }
  // 兜底：直接取纯黑/纯白前景（对比最高）
  return toward === 1 ? '#ffffff' : '#000000'
}
