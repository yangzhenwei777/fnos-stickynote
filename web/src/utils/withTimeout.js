// withTimeout.js —— v21.200：给可能长耗时的桌面绑定调用加**超时上限**。
//
// 为什么需要（用户 2026-10-09 实机取证）：`remoteLogin` / `bootstrapNow` 在Go侧是
// **同步阻塞**调用（BootstrapNow 内部轮询快照就绪，最长 pollTimeout），前端
// `await` 期间按钮永久 loading。用户实测「登录按钮一直转圈圈」即此——Go 侧最终
// 会返回（甚至失败），但 UI 在此期间**没有任何反馈、没有取消、没有上限**。
//
// 定位原则（不改语义，只加边界）：
//   - 超时**不承诺**后端已停止工作，只解除 UI 的等待状态；
//   - 超时文案必须说明「后端可能仍在进行」，避免用户重复点击造成并发；
//   - 超时时长取 Go 侧 pollTimeout 的**上限 + 余量**（90s + 30s= 120s），
//     保证正常路径不会被误杀（正常登录是秒级）。
export const DEFAULT_TIMEOUT_MS = 120000

/**
 * withTimeout 给 Promise 加超时上限。
 * @param {Promise} p 原始 Promise
 * @param {number} ms 超时毫秒（默认 120s）
 * @param {string} label 超时文案里的动作名（如「登录」）
 * @returns {Promise} 超时则 reject 一个带可读 message 的 Error
 */
export function withTimeout (p, ms = DEFAULT_TIMEOUT_MS, label = '操作') {
  let timer = null
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => {
      const err = new Error(
        `${label}超过 ${Math.round(ms / 1000)} 秒仍未返回，已解除界面等待。`
        + '后台可能仍在继续处理，请稍候刷新页面查看结果，不要重复点击。'
      )
      err.code = 'DESKTOP_CALL_TIMEOUT'
      reject(err)
    }, ms)
  })
  return Promise.race([p, timeout]).finally(() => {
    if (timer) clearTimeout(timer)
  })
}