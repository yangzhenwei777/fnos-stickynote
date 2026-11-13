/* eslint-disable no-console -- 测试入口需要有选择地包装 console，以过滤测试桩噪声。 */
// 统一处理测试环境已知噪声，不屏蔽产品错误。
// 这些警告来自测试桩（无真实 router、Element Plus 原生 DOM 属性透传），
// 其他 console.warn/error 仍原样输出，便于发现新增问题。
const acceptedTestWarnings = [
  /injection "Symbol\(router\)" not found/,
  /Failed to resolve component: el-/,
  /Failed setting prop "size" on <input>/
]

for (const method of ['warn', 'error']) {
  const original = console[method]
  console[method] = (...args) => {
    const text = args.map(String).join(' ')
    if (acceptedTestWarnings.some((pattern) => pattern.test(text))) return
    original.apply(console, args)
  }
}
