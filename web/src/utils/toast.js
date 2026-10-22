// Toast 通知组件 — v21.14 极简重设计
// 顶部居中、白色背景、彩色圆形图标、标题+描述、自动堆叠（最多5条）、hover暂停
const TOAST_DURATION = 3000
const MAX_TOASTS = 5

let container = null
const iconMap = { success: '✓', error: '✕', warning: '!', info: 'i' }
const colorMap = { success: '#10b981', error: '#ef4444', warning: '#f59e0b', info: '#3b82f6' }

function ensureContainer() {
  if (container) return
  container = document.createElement('div')
  container.id = 'toast-container'
  container.style.cssText = `
    position: fixed;
    top: 24px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 10000;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    pointer-events: none;
  `
  document.body.appendChild(container)
}

function removeToast(el) {
  el.style.opacity = '0'
  el.style.transform = 'translateY(-12px)'
  setTimeout(() => el.remove(), 200)
}

function show(title, description = '', type = 'info', duration = TOAST_DURATION) {
  ensureContainer()

  // 超过上限，移除最早
  if (container.children.length >= MAX_TOASTS) {
    removeToast(container.firstChild)
  }

  const color = colorMap[type] || colorMap.info
  const isError = type === 'error'
  if (isError) duration = Math.max(duration, 4000)

  // v21.125 对齐修复：单行 toast 图标/文字/关闭符垂直居中；
  // 双行保持首行对齐，图标(22px)/关闭符(14px) 相对标题行盒(14px*1.4=19.6px)光学居中补偿
  const multi = !!(description && description !== title)
  const iconMt = multi ? '-1.2px' : '0px'
  const closeMt = multi ? '2.8px' : '0px'

  const toast = document.createElement('div')
  toast.style.cssText = `
    display: flex;
    align-items: ${multi ? 'flex-start' : 'center'};
    gap: 10px;
    width: 340px;
    max-width: calc(100vw - 32px);
    padding: 14px 16px;
    background: var(--bg-primary);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    box-shadow: 0 2px 8px rgba(0,0,0,.06);
    opacity: 0;
    transform: translateY(-20px);
    transition: all 0.25s ease;
    pointer-events: auto;
    position: relative;
  `

  // 图标
  const icon = document.createElement('span')
  icon.style.cssText = `
    display: flex; align-items: center; justify-content: center;
    width: 22px; height: 22px; border-radius: 50%;
    background: ${color}; color: #fff;
    font-size: 12px; font-weight: 700; flex-shrink: 0; margin-top: ${iconMt};
  `
  icon.textContent = iconMap[type] || iconMap.info
  toast.appendChild(icon)

  // 内容区
  const body = document.createElement('div')
  body.style.cssText = 'flex:1; min-width: 0;'

  const titleEl = document.createElement('div')
  titleEl.style.cssText = 'font-size:14px; font-weight:600; color:var(--text-primary); line-height:1.4; word-break:break-word;'
  titleEl.textContent = title || description
  body.appendChild(titleEl)

  if (description && description !== title) {
    const descEl = document.createElement('div')
    descEl.style.cssText = 'font-size:13px; color:var(--text-secondary); line-height:1.4; margin-top:2px; word-break:break-word;'
    descEl.textContent = description
    body.appendChild(descEl)
  }

  toast.appendChild(body)

  // 关闭按钮
  const close = document.createElement('button')
  close.textContent = '✕'
  close.style.cssText = `
    background: none; border: none; color: var(--text-muted); cursor: pointer;
    font-size: 14px; padding: 0 2px; line-height: 1; margin-top: ${closeMt}; flex-shrink: 0;
    transition: color .15s;
  `
  close.onmouseenter = () => { close.style.color = 'var(--text-primary)' }
  close.onmouseleave = () => { close.style.color = 'var(--text-muted)' }
  close.onclick = () => { clearTimer(); removeToast(toast) }
  toast.appendChild(close)

  container.appendChild(toast)

  // 滑入
  requestAnimationFrame(() => {
    toast.style.opacity = '1'
    toast.style.transform = 'translateY(0)'
  })

  // 自动关闭
  let timer = setTimeout(() => removeToast(toast), duration)
  function clearTimer() { clearTimeout(timer) }

  // hover 暂停
  toast.onmouseenter = () => clearTimer()
  toast.onmouseleave = () => { timer = setTimeout(() => removeToast(toast), duration) }
}

export const toast = {
  success: (title, description) => show(title, description, 'success'),
  error: (title, description) => show(title, description, 'error'),
  warning: (title, description) => show(title, description, 'warning'),
  info: (title, description) => show(title, description, 'info'),
}
