// Markdown 粘贴自动转换（v20.1 引入，v2.2.1 迁移 + 收紧）
// ─────────────────────────────────────────────────────────────
// 迁移原因（v2.2.1 用户反馈 2b：粘贴双插）：原实现挂在 EditorToolbar 的 DOM
// addEventListener('paste') 监听上，而 ProseMirror 自己的 paste handler 先于该
// 监听执行，preventDefault 拦不住默认插入 → 纯文本与转换结果各插一份。
// 现拆为纯函数模块：
//   - isMarkdownPaste / mdToHtml 由 TiptapEditor 的 editorProps.handlePaste 调用
//     （官方粘贴接管点：返回 true 即接管并阻止默认，单份插入转换结果）；
//   - EditorToolbar 仅保留图片上传兜底，复用 isMarkdownPaste 守卫混合粘贴
//     （Markdown 优先、图片忽略——与迁移前语义一致）。

// 触发判定（v2.2.1 收紧）：仅当出现明确的块级 Markdown 语法意图时才接管粘贴。
// 旧正则 /^[#*->`[].*|^`.+`$/m 过宽——「-2026年」（减号连数字无空格）这类普通
// 文本也会触发。规则：标题 # / 无序列表 - 或 * / 有序列表 1. / 引用 > 均要求
// 「行首符号 + 空格 + 内容」；行内代码仅当整行整体被反引号包裹时触发（故此分支
// 单独带行尾断言，块级分支允许行内其余内容）。
const MARKDOWN_TRIGGER_RE = /(^|\n)(#{1,3}\s+\S|[-*]\s+\S|\d+\.\s+\S|>\s+\S|`[^`\n]+`(?=\n|$))/

// isMarkdownPaste 判断粘贴的纯文本是否命中 Markdown 触发条件。
export function isMarkdownPaste(text) {
  return typeof text === 'string' && MARKDOWN_TRIGGER_RE.test(text)
}

// mdToHtml 将常见 Markdown 行内/块级语法转为 HTML（零依赖纯正则转换）。
// 转换范围与工具栏能力对齐（标题 h1~h3）；转换前后无变化时调用方应放行默认粘贴。
export function mdToHtml(text) {
  let html = text
  // 代码块（```...```）转 <pre><code>
  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code class="language-$1">$2</code></pre>')
  // 行内代码
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>')
  // 标题
  html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>')
  html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>')
  html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>')
  // 粗体+斜体
  html = html.replace(/\*\*\*(.+?)\*\*\*/g, '<strong><em>$1</em></strong>')
  // 粗体
  html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
  // 斜体
  html = html.replace(/\*(.+?)\*/g, '<em>$1</em>')
  // 删除线
  html = html.replace(/~~(.+?)~~/g, '<s>$1</s>')
  // 链接
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2">$1</a>')
  // 任务清单（- [ ] / - [x]）
  html = html.replace(/^- \[([ xX])\] (.+)$/gm, (m, c, t) => {
    const checked = c.toLowerCase() === 'x'
    return `<li data-type="taskItem" data-checked="${checked}"><label><input type="checkbox"${checked ? ' checked' : ''}></label><div><p>${t}</p></div></li>`
  })
  // 无序列表
  html = html.replace(/^- (.+)$/gm, '<li>$1</li>')
  // 任务清单项包成 taskList
  html = html.replace(/(<li data-type="taskItem"[\s\S]*?<\/li>)/g, '<ul data-type="taskList">$1</ul>')
  // 普通列表项包成 ul（精确匹配 <li>，不会命中带属性的 taskItem）
  html = html.replace(/(<li>[\s\S]*?<\/li>)/g, '<ul>$1</ul>')
  return html
}
