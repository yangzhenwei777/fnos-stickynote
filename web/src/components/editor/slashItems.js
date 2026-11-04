// 2.4.0 斜杠命令：仅登记纯编辑器内命令。图片/链接涉及上传与弹框，继续走工具栏入口。
export const SLASH_ITEMS = [
  { id: 'h1', label: '标题 1', hint: 'H1', keywords: ['h1', 'heading', 'biaoti', '标题'], run: (chain) => chain.toggleHeading({ level: 1 }) },
  { id: 'h2', label: '标题 2', hint: 'H2', keywords: ['h2', 'heading', 'biaoti', '标题'], run: (chain) => chain.toggleHeading({ level: 2 }) },
  { id: 'h3', label: '标题 3', hint: 'H3', keywords: ['h3', 'heading', 'biaoti', '标题'], run: (chain) => chain.toggleHeading({ level: 3 }) },
  { id: 'bullet', label: '无序列表', hint: '•', keywords: ['bullet', 'list', 'wuxu', '列表'], run: (chain) => chain.toggleBulletList() },
  { id: 'ordered', label: '有序列表', hint: '1.', keywords: ['ordered', 'number', 'youxu', '列表', '编号'], run: (chain) => chain.toggleOrderedList() },
  { id: 'task', label: '任务清单', hint: '☑', keywords: ['task', 'todo', 'renwu', '清单', '待办'], run: (chain) => chain.toggleTaskList() },
  { id: 'quote', label: '引用', hint: '❝', keywords: ['quote', 'blockquote', 'yinyong'], run: (chain) => chain.toggleBlockquote() },
  { id: 'code', label: '代码块', hint: '</>', keywords: ['code', 'codeblock', 'daima', '代码'], run: (chain) => chain.toggleCodeBlock() },
  { id: 'table', label: '表格', hint: '▦', keywords: ['table', 'biaoge'], run: (chain) => chain.insertTable({ rows: 3, cols: 3, withHeaderRow: true }) },
  { id: 'divider', label: '分割线', hint: '—', keywords: ['divider', 'hr', 'rule', 'fenge'], run: (chain) => chain.setHorizontalRule() }
]

export function filterSlashItems(query, items = SLASH_ITEMS) {
  const q = (query || '').trim().toLowerCase()
  if (!q) return items
  return items.filter((item) =>
    item.label.toLowerCase().includes(q) ||
    item.keywords.some((k) => k.toLowerCase().includes(q))
  )
}
