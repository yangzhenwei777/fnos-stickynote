export function stripHtml(html, maxLen = 80) {
  if (!html) return ''
  // v18.8: 改用 DOMParser 惰性解析（不挂载到文档），避免 innerHTML 赋值触发的
  // 资源加载/脚本执行副作用（onerror 等属性在 innerHTML 场景下可能触发请求）
  const doc = new DOMParser().parseFromString(html, 'text/html')
  const text = doc.body ? (doc.body.textContent || '') : ''
  return text.length > maxLen ? text.substring(0, maxLen) + '...' : text
}

export function splitTags(tagsStr) {
  if (!tagsStr) return []
  return tagsStr.split(',').map(t => t.trim()).filter(Boolean)
}

// v21.113：预览/阅读态任务清单勾选——对 Tiptap 生成的 taskList HTML 做文档序
// 第 index 个 taskItem 的勾选态补丁。DOMParser 惰性解析（不挂载文档，与
// stripHtml 同一安全基线）；返回新 HTML 字符串，输入非法/越界返回 null。
// data-checked="true"/"false" 与 li 上 input[checked] 双写，与 TaskItem
// parseHTML/renderHTML 契约一致（parseHTML 只认 li 的 data-checked）。
export function toggleTaskItemInHtml(html, index, checked) {
  if (typeof html !== 'string' || !html || index < 0) return null
  const doc = new DOMParser().parseFromString(html, 'text/html')
  const items = doc.querySelectorAll('ul[data-type="taskList"] li[data-type="taskItem"]')
  if (!items.length || index >= items.length) return null
  const li = items[index]
  li.setAttribute('data-checked', checked ? 'true' : 'false')
  const input = li.querySelector('input[type="checkbox"]')
  if (input) {
    if (checked) input.setAttribute('checked', 'checked')
    else input.removeAttribute('checked')
  }
  return doc.body ? doc.body.innerHTML : null
}

export function formatDate(dateStr) {
  if (!dateStr) return ''
  const d = new Date(dateStr)
  const now = new Date()
  const diff = now - d
  if (diff < 60000) return '刚刚'
  if (diff < 3600000) return Math.floor(diff / 60000) + '分钟前'
  if (diff < 86400000) return Math.floor(diff / 3600000) + '小时前'
  if (diff < 604800000) return Math.floor(diff / 86400000) + '天前'
  return d.toLocaleDateString()
}
