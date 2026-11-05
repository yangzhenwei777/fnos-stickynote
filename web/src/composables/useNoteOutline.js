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

// 2.6.3 阅读态双滚动：大纲到边界或本身不可滚时阻断滚轮，避免滚动链带动正文。
export function shouldBlockOutlineWheel(deltaY, scrollTop, clientHeight, scrollHeight) {
  if (!deltaY) return false
  if (scrollHeight <= clientHeight + 1) return true
  if (deltaY < 0) return scrollTop <= 0
  return scrollTop + clientHeight >= scrollHeight - 1
}
