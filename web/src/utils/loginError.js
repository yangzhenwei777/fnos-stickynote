// loginError.js —— 登录异常 → 可行动文案（v2.1.4）。
//
// 背景：安卓中继链路（https://<id>.fnos.net）实测 CORS 403 空 body，
// 旧逻辑 `data?.error || '登录失败'` 只能显示裸的四个字，用户无从判断该找谁。
// 分类目标：每一类都给出下一步动作，永远不再出现裸的「登录失败」。

/**
 * @param {Error} e axios 错误或任意异常
 * @returns {string} 展示给用户的一句话
 */
export function describeLoginError (e) {
  // ① 服务端业务错误（401 密码错 / 423 锁定 / 429 限流都带 {"error": 中文}）
  const detail = e?.response?.data?.error
  if (detail) return detail
  // ② 有 HTTP 响应但 body 不是 JSON：典型是 CORS 拦截（403 空 body）、网关 502 HTML
  if (e?.response) {
    return '登录请求被拦截（HTTP ' + e.response.status + '，响应非 JSON）。' +
      '多为网关/CORS 拦截：请确认访问域名与网络后重试，仍失败请联系维护者'
  }
  // ③ 请求根本没到服务端（网络层失败）
  if (e?.code === 'ERR_NETWORK' || e?.message?.includes('Network Error')) {
    return '无法连接服务器，请检查网络或稍后重试'
  }
  // ④ 其余：带原始 message 兜底
  return '登录失败：' + (e?.message || '未知错误')
}
