// 2.4.0 阅读态大纲：从已消毒的预览 HTML 提取 h1~h3，不改变渲染内容本身。
export function extractNoteOutline(html, maxItems = 50) {
  if (typeof html !== 'string' || !html.trim()) return []
  const doc = new DOMParser().parseFromString(html, 'text/html')
  const items = []
  for (const node of doc.body.querySelectorAll('h1, h2, h3')) {
    const text = (node.textContent || '').trim()
    if (!text) continue
    items.push({ level: Number(node.tagName.charAt(1)), text })
    if (items.length >= maxItems) break
  }
  return items
}
