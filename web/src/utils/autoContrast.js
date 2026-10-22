// v21.118 评审修复（问题①）：暗色模式下文字色对比度自适应层。
// 关键：只注入一段作用域限定、仅暗色生效的 <style>（用 !important 覆盖显示），
// 绝不修改元素内联 style——因此 ProseMirror 模型的 getHTML()、以及存储/导出的颜色一律保持原值，
// 仅在"看"的层面按 WCAG 4.5:1 保色相提亮。切回亮色自动清空规则（还原原色显示）。
import { watch, nextTick } from 'vue'
import { useTheme } from '@/composables/useTheme'
import { ensureContrast, parseColor } from './contrast'

const MIN_RATIO = 4.5
const FALLBACK_BG = '#1e293b'
const SCOPES = ['.ProseMirror', '.note-preview-content', '.view-content']
const STYLE_ID = 'sticky-auto-contrast-style'

function effectiveBg(el) {
  let node = el
  while (node && node.nodeType === 1) {
    const bg = getComputedStyle(node).backgroundColor
    if (bg && bg !== 'rgba(0, 0, 0, 0)' && bg !== 'transparent' && parseColor(bg)) return bg
    node = node.parentElement
  }
  return FALLBACK_BG
}

// v21.120 修复（暗色纯黑字仍不反白）：属性限定 [style*=…] 必须逐作用域各拼一份，
// 而非把三态容器逗号拼成前缀后只在末尾追加一次（那样只命中最后一段 .view-content，
// 编辑/预览态于是变成给容器本身设色，而容器色覆盖不了子 span 自己的内联 color）。
// 纯函数、单独导出，便于回归测试断言选择器结构。
export function buildContrastRules(entries) {
  let css = ''
  for (const [orig, adj] of entries) {
    const esc = orig.replace(/["\\]/g, (c) => '\\' + c)
    const sel = SCOPES.map((s) => `html[data-theme="dark"] ${s} [style*="${esc}" i]`).join(',')
    css += `${sel}{color:${adj}!important}`
  }
  return css
}

export function installAutoContrast(rootEl) {
  const { isDark } = useTheme()
  let styleEl = document.getElementById(STYLE_ID)
  if (!styleEl) {
    styleEl = document.createElement('style')
    styleEl.id = STYLE_ID
    document.head.appendChild(styleEl)
  }
  let timer = null

  function rebuild() {
    if (!rootEl || !rootEl.isConnected) return
    if (!isDark.value) { styleEl.textContent = ''; return }
    const seen = new Map() // 原色字符串 -> 提亮色
    rootEl.querySelectorAll('[style]').forEach((el) => {
      const raw = el.getAttribute('style') || ''
      const m = raw.match(/(?:^|[;\s])color\s*:\s*([^;]+)/i)
      if (!m) return
      const orig = m[1].trim()
      if (!orig || seen.has(orig)) return
      const adj = ensureContrast(orig, effectiveBg(el), MIN_RATIO)
      if (adj) seen.set(orig, adj)
    })
    styleEl.textContent = buildContrastRules(seen)
  }

  function schedule() {
    if (timer) clearTimeout(timer)
    timer = setTimeout(rebuild, 80)
  }

  const mo = new MutationObserver(schedule)
  mo.observe(rootEl, { subtree: true, childList: true, attributes: true, attributeFilter: ['style'] })
  const stop = watch(isDark, () => { nextTick(rebuild) })
  rebuild()
  return () => {
    if (timer) clearTimeout(timer)
    mo.disconnect()
    stop()
    if (styleEl) styleEl.textContent = ''
  }
}
