import { ref, watch } from 'vue'

// v21.95 F-4（审计 P2）：暗色模式单一真相源修复。
// 此前主题由两处管理互相覆盖：
//   App.vue     matchMedia(系统偏好) → 只设 data-theme，不设 class（Element Plus 暗色失效）
//   useTheme.js localStorage(手动)   → 设 data-theme + class
// 系统偏好变化会覆盖用户手动选择；且系统暗色时 Element Plus 组件不跟随（缺 class）。
// 现统一由本 composable 管理：
//   1) 优先用户手动选择（localStorage 'theme'）；无记录时跟随系统（matchMedia）。
//   2) 跟随系统仅在用户尚未手动选择时生效；手动切换后停止跟随系统。
//   3) applyTheme 统一设置 data-theme + class（Element Plus 暗色正确生效）。

const STORAGE_KEY = 'theme'
const SYSTEM_QUERY = '(prefers-color-scheme: dark)'

function systemPrefersDark() {
  return typeof window !== 'undefined' && window.matchMedia
    ? window.matchMedia(SYSTEM_QUERY).matches
    : false
}

function hasManualChoice() {
  return localStorage.getItem(STORAGE_KEY) !== null
}

const isDark = ref(hasManualChoice() ? localStorage.getItem(STORAGE_KEY) === 'dark' : systemPrefersDark())

function applyTheme(dark) {
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light')
  if (dark) {
    document.documentElement.classList.add('dark')
  } else {
    document.documentElement.classList.remove('dark')
  }
}

let mq = null

function onSystemChange(e) {
  // 用户已手动选择主题后，系统偏好变化不再覆盖（F-4：消除双源互相覆盖）
  if (hasManualChoice()) return
  isDark.value = e.matches
  applyTheme(e.matches)
}

function initTheme() {
  applyTheme(isDark.value)
  // 无手动选择时监听系统变化（仅此时跟随系统）
  if (!hasManualChoice() && typeof window !== 'undefined' && window.matchMedia) {
    mq = window.matchMedia(SYSTEM_QUERY)
    mq.addEventListener('change', onSystemChange)
  }
}

function toggleTheme() {
  isDark.value = !isDark.value
  localStorage.setItem(STORAGE_KEY, isDark.value ? 'dark' : 'light')
  applyTheme(isDark.value)
}

watch(isDark, () => {
  applyTheme(isDark.value)
})

export function useTheme() {
  return { isDark, toggleTheme, initTheme }
}
