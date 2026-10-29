import DOMPurify from 'dompurify'
import { isUploadUrl, toDisplayUrl } from './uploadUrl'
import { stripLegacyColWidthArtifacts } from '@/components/editor/tableColWidth'
import { projectColPercentsInHtml } from './tableColPct'

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
    // v2.2.2 表格列宽保持：编辑器 resizable 产出的 <col style="width:Npx"> 经白名单放行后，
    // 给存在真实列宽的 table 打标记（min-width 声明不计），CSS 据此定向启用 fixed 布局——
    // 用属性选择器而非 :has()，兼容旧内核 build target（chrome87）。
    if (node.tagName === 'TABLE' && node.querySelector('colgroup')) {
      const cols = node.querySelectorAll('col')
      for (const c of cols) {
        if (c.style && c.style.width) {
          node.setAttribute('data-has-colwidth', 'true')
          break
        }
      }
    }
  })
  hookRegistered = true
}

// v21.82：div/label/input 为任务清单（taskList）渲染所需；data-type/data-checked
// 由 DOMPurify 默认放行 data-*；v21.113 起阅读态/预览态复选框可勾选
//（@change 委托回写 content，CSS 上仅 16px 方框可点、label 与文字不可点）
// v2.2.2：colgroup/col 为表格列宽（编辑器 resizable 拖宽 → <col style="width:Npx">）渲染所需；
// col 的 style 属性经 ALLOWED_ATTR 放行，其余属性（若将来出现）随标签白名单不含即剥。
const ALLOWED_TAGS = ['p', 'br', 'hr', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'strong', 'b', 'em', 'i', 'u', 's', 'del', 'strike', 'code', 'pre', 'blockquote', 'ul', 'ol', 'li', 'span', 'mark', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'colgroup', 'col', 'a', 'img', 'div', 'label', 'input']
const ALLOWED_ATTR = ['href', 'src', 'alt', 'title', 'width', 'height', 'style', 'type', 'checked', 'disabled', 'target', 'rel']

// 笔记正文/预览态消毒（阅读态与编辑内预览共用）。图片仅允许同源 /uploads/。
// v2.2.2 R3：消毒后清一次 R2 列宽缺陷遗留的「表格前纯 > 段落」（存量笔记在
// 未重新编辑前也不会把杂字符带到预览/长图；重新编辑保存后由编辑器侧永久清除）
// v2.2.2 R4（方案 A）：列宽统一投影为百分比（与编辑器/长图/md 同比例），
// 消除「未拖列吃剩余空间」导致的容器宽度依赖
export function sanitizeNoteHtml(html) {
  ensureUploadOnlyImgHook()
  const cleaned = stripLegacyColWidthArtifacts(DOMPurify.sanitize(html || '', {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto):|[^a-z]|[a-z+.-]+(?:[^a-z+.\-:]|$))/i
  }))
  return projectColPercentsInHtml(cleaned)
}

// 列表搜索高亮片段：仅放行 <mark>
export function sanitizeHighlight(html) {
  return DOMPurify.sanitize(html || '', { ALLOWED_TAGS: ['mark'] })
}
