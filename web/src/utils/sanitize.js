import DOMPurify from 'dompurify'
import { isUploadUrl, toDisplayUrl } from './uploadUrl'

// 评审 P2：笔记 HTML 消毒集中于此。
// 原实现把 DOMPurify.addHook('afterSanitizeAttributes', …) 写在 NotesView 模块作用域，
// 随组件热重载会重复注册、且泄漏到其它 DOMPurify 消费者。此处 hook 只幂等注册一次。

let hookRegistered = false
function ensureUploadOnlyImgHook() {
  if (hookRegistered) return
  DOMPurify.addHook('afterSanitizeAttributes', (node) => {
    if (node.tagName === 'IMG') {
      const src = node.getAttribute('src') || ''
      // v2.1.2：白名单兼容「存储态 /uploads/」与「已带挂载前缀的展示态」两种形态，
      // 并在渲染副本上补挂载前缀（子路径下 /uploads/… 会被解析到宿主根 → 图片全裂）。
      // 只改 DOMPurify 输出的副本，**不改笔记存储**。
      if (!isUploadUrl(src)) {
        node.remove()
      } else {
        node.setAttribute('src', toDisplayUrl(src))
      }
    }
    // 2026-10-15 用户需求：预览/阅读态点击链接在新窗口打开（原页跳转会丢正在看的笔记）。
    // 集中在消毒 hook 强制补齐——存量笔记的 <a> 没存 target，靠 ALLOWED_ATTR 放行不够。
    if (node.tagName === 'A' && node.getAttribute('href')) {
      node.setAttribute('target', '_blank')
      node.setAttribute('rel', 'noopener noreferrer')
    }
  })
  hookRegistered = true
}

// v21.82：div/label/input 为任务清单（taskList）渲染所需；data-type/data-checked
// 由 DOMPurify 默认放行 data-*；v21.113 起阅读态/预览态复选框可勾选
//（@change 委托回写 content，CSS 上仅 16px 方框可点、label 与文字不可点）
const ALLOWED_TAGS = ['p', 'br', 'hr', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'strong', 'b', 'em', 'i', 'u', 's', 'del', 'strike', 'code', 'pre', 'blockquote', 'ul', 'ol', 'li', 'span', 'mark', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'a', 'img', 'div', 'label', 'input']
const ALLOWED_ATTR = ['href', 'src', 'alt', 'title', 'width', 'height', 'style', 'type', 'checked', 'disabled', 'target', 'rel']

// 笔记正文/预览态消毒（阅读态与编辑内预览共用）。图片仅允许同源 /uploads/。
export function sanitizeNoteHtml(html) {
  ensureUploadOnlyImgHook()
  return DOMPurify.sanitize(html || '', {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto):|[^a-z]|[a-z+.-]+(?:[^a-z+.\-:]|$))/i
  })
}

// 列表搜索高亮片段：仅放行 <mark>
export function sanitizeHighlight(html) {
  return DOMPurify.sanitize(html || '', { ALLOWED_TAGS: ['mark'] })
}
