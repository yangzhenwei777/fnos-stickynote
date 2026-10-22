/**
 * 启动占位（boot-guard）控制。
 *
 * v21.195：194 的做法是「挂载成功即移除占位」，但若app.mount() 本身抛错
 * （如 auth store 初始化异常），占位与错误横幅都随 app 一起消失 → 纯白屏。
 * 现改为三态：启动中（静态占位）/ 成功（移除）/ 失败（占位转失败态并展示原因）。
 * 失败态里的「重新加载」是纯 <a href>，**不依赖任何 JS**，故JS 全挂时也能自救。
 */

const GUARD_ID = 'boot-guard'

function el (id) {
  return typeof document === 'undefined' ? null : document.getElementById(id)
}

/** 挂载成功：撤掉静态占位 */
export function clearBootGuard () {
  // v2.1.2：先置位成功标记——index.html 的内联兜底脚本在 8s 超时时会读它，
  // 避免「已正常启动但占位刚被移除」的极短窗口被误判成失败。
  if (typeof window !== 'undefined') window.__SN_BOOTED__ = true
  const g = el(GUARD_ID)
  if (g) g.remove()
}

/**
 * 挂载失败：把占位转成失败态（保留可点击的重新加载出口）。
 * @param {string} summary 失败摘要（异常信息）
 */
export function showBootFailure (summary) {
  // v2.1.2：置位后内联兜底的 8s 定时器不再重复改写（本函数的文案更详细）。
  if (typeof window !== 'undefined') window.__SN_BOOT_FAILED__ = true
  const g = el(GUARD_ID)
  if (!g || !document.body) return
  const title = g.querySelector('.boot-title')
  const hint = g.querySelector('.boot-hint')
  if (title) { title.textContent = 'Sticky note 启动失败'; title.style.color = '#c45656' }
  if (hint) {
    hint.textContent = (summary ? `错误摘要：${summary}` : '应用初始化时发生异常。') +
      ' 可尝试重新加载；若反复出现，请在「账号与设备」查看同步状态，或删除程序目录下的 sticky-note-data\\webview2 后重启（不影响本机笔记数据）。'
  }
  const reload = el('boot-reload')
  if (reload) reload.style.display = 'inline-block'
  const g2 = el(GUARD_ID)
  if (g2) g2.style.background = '#fef0f0'
}

export { GUARD_ID }
