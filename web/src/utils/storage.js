/**
 * localStorage JSON 读写双向加固 + 存量毒化键自愈。
 *
 * v21.195 白屏根修（2026-10-08 绿色版白屏实证）：
 * 事故链 = setAuth 收到 undefined → JSON.stringify(undefined) 返回 undefined
 * → localStorage.setItem('user', undefined) 把**字面量字符串 "undefined"** 落盘
 * → 下次启动 stores/auth.js 裸 JSON.parse("undefined") 抛 SyntaxError
 * → auth store 初始化中断 → 根组件/router-view 渲染中断 → **整个窗口纯白**。
 *
 * 纪律：凡往 localStorage 写 JSON，一律走 writeJSON（写侧守卫）；
 * 读一律走 readJSON（读侧 try/catch + 毒值清除）。裸JSON.parse(localStorage…) 是缺陷。
 * 本模块同时承担存量自愈：老版本已投毒的存储必须能自愈，否则修复后用户依旧白屏。
 */

// JSON.stringify 的合法「退化输出」：写进存储即毒值，读时必须当垃圾处理
const POISON_VALUES = new Set(['undefined', 'null', 'NaN', '[object object]', 'Infinity', '-Infinity'])

// 已知以 JSON 形式存储的键：解析失败即视为毒值清除（其余键值都是字符串，不解析）
const JSON_KEYS = ['user', 'userId', 'login_remember']

function removeKey (key) {
  try { localStorage.removeItem(key) } catch { /* 隐私模式/配额异常：忽略 */ }
}

/**
 * 安全读取 localStorage 中的 JSON。
 * @param {string} key 键名
 * @param {*} fallback 解析失败/毒值/缺失时的返回值（默认 null）
 * @returns {*} 解析结果；毒值与非法 JSON 一律**清除该键**后返回 fallback
 */
export function readJSON (key, fallback = null) {
  let raw
  try { raw = localStorage.getItem(key) } catch { return fallback }
  if (raw === null || raw === undefined) return fallback
  const trimmed = String(raw).trim()
  if (POISON_VALUES.has(trimmed) || trimmed === '') { removeKey(key); return fallback }
  try {
    return JSON.parse(trimmed)
  } catch {
    removeKey(key)
    return fallback
  }
}

/**
 * 安全写入 localStorage JSON。
 * undefined / 无法序列化（函数、Symbol、循环引用）→ **删除键**而非落脏值。
 * @returns {boolean} 是否真正写入
 */
export function writeJSON (key, value) {
  try {
    if (value === undefined) { removeKey(key); return false }
    const raw = JSON.stringify(value)
    if (raw === undefined || POISON_VALUES.has(String(raw).trim())) { removeKey(key); return false }
    localStorage.setItem(key, raw)
    return true
  } catch {
    // 循环引用等不可序列化异常：同样删键——宁可「无值」也不留与内存不一致的错值
    removeKey(key)
    return false
  }
}

/**
 * 启动期存量自愈：清扫 localStorage 中被旧版本写坏的键。
 * 规则（保守，只动确证为毒的值）：
 *  ① 任意键的原始值 ∈ POISON_VALUES → 删
 *  ② 已知 JSON 键（JSON_KEYS）无法 JSON.parse → 删
 * @returns {{removed: string[], scanned: number}} 供日志/上报
 */
export function sanitizeLocalStorage (jsonKeys = JSON_KEYS) {
  const removed = []
  let scanned = 0
  let keys
  try { keys = Object.keys(localStorage) } catch { return { removed, scanned } }
  for (const key of keys) {
    let raw
    try { raw = localStorage.getItem(key) } catch { continue }
    scanned++
    const trimmed = String(raw ?? '').trim()
    if (POISON_VALUES.has(trimmed)) { removeKey(key); removed.push(key); continue }
    if (jsonKeys.includes(key) && trimmed !== '') {
      try { JSON.parse(trimmed) } catch { removeKey(key); removed.push(key) }
    }
  }
  return { removed, scanned }
}

export { POISON_VALUES, JSON_KEYS }
