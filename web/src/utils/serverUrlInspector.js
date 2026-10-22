/**
 * v21.202 · 服务器地址误配检测（P1）。
 *
 * 定罪（用户实机 2026-10-10）：用户的生产服务器已**禁用 IP 登录**，仅支持
 * 域名（`https://note.example.com/`）。而登录框的 placeholder 是
 * `http://192.0.2.10:13389`（内网IP），用户很容易照着填 → 生产环境
 * 必然连不通，且报错是传输层噪声，用户不知道原因。
 *
 * 同时覆盖另一类高 frequent 误配：填了**内网 IP** 但当前机器走代理/不在
 * 同一内网（代理 TUN/Fake-IP 环境下同理）。
 *
 * 纯函数，无副作用 —— 便于 Vitest 直接覆盖。
 */

// 私网/回环/link-local 网段（RFC 1918 + loopback + CGNAT + link-local）
const PRIVATE_V4 = [
  { re: /^10\./, label: '10.x.x.x（RFC1918 私网）' },
  { re: /^192\.168\./, label: '192.168.x.x（RFC1918 私网）' },
  { re: /^172\.(1[6-9]|2\d|3[01])\./, label: '172.16~31.x.x（RFC1918 私网）' },
  { re: /^127\./, label: '127.0.0.1（本机回环）' },
  { re: /^169\.254\./, label: '169.254.x.x（link-local，通常是没配好）' },
  { re: /^100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\./, label: '100.64~127.x.x（CGNAT 运营商级 NAT）' }
]

const HOST_PORT = /^(\[[0-9a-fA-F:]+\]|[^:/?#]+)(?::(\d+))?\/?$/

/**
 * isPrivateIPv4 判断 host 是否私网/回环地址。
 * 只识别裸 IPv4；域名与 IPv6 一律返回 false（不猜）。
 */
export function isPrivateIPv4 (host) {
  const h = String(host || '').trim()
  if (!h || h.includes(':') || !/^\d{1,3}(\.\d{1,3}){3}$/.test(h)) return false
  const p = h.split('.').map(Number)
  if (p.some((n) => Number.isNaN(n) || n < 0 || n > 255)) return false
  for (const { re } of PRIVATE_V4) {
    if (re.test(h)) return true
  }
  return false
}

/**
 * parseServerHost 从服务器地址里取出 host（去协议/端口/路径）。
 * 解析失败返回空串（不当成误配 —— 解析不了就让用户自己填对）。
 */
export function parseServerHost (baseURL) {
  const raw = String(baseURL || '').trim()
  if (!raw) return ''
  const m = raw.match(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\/([^/?#]+)/)
  const authority = m ? m[1] : raw
  const hm = authority.match(HOST_PORT)
  if (!hm) return ''
  return String(hm[1] || '').replace(/^\[|\]$/g, '')
}

/**
 * inspectServerURL 给出服务器地址的误配诊断。
 *
 * @returns {{level:'ok'|'warn'|'error', hint:string}}
 *   ok    = 无可判定的误配
 *   warn  = 疑似（可能处在同一内网，仍可能可用 —— 不阻断）
 *   error = 明确不可用（生产已禁 IP 登录 / 格式错误）
 */
export function inspectServerURL (baseURL, opts = {}) {
  const raw = String(baseURL || '').trim()
  if (!raw) return { level: 'ok', hint: '' }

  if (!/^https?:\/\//i.test(raw)) {
    return {
      level: 'error',
      hint: '地址必须以 http:// 或 https:// 开头，例如 https://note.example.com'
    }
  }

  const host = parseServerHost(raw)
  if (!host) {
    return { level: 'error', hint: '无法解析服务器地址，请检查格式（应为 https://域名 或 http://IP:端口）' }
  }

  // 域名（最常见的正确用法）：不判误配。
  // 注：内网域名（如 nas.local）也走这一支 —— 无法静态判定是否可达。
  if (!/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) {
    return { level: 'ok', hint: '' }
  }

  if (isPrivateIPv4(host)) {
    const kind = (PRIVATE_V4.find(({ re }) => re.test(host)) || {}).label || '私网地址'
    const ipLoginDisabled = opts.ipLoginDisabled !== false
    if (ipLoginDisabled) {
      return {
        level: 'error',
        hint: `这是${kind}。若服务器已禁用 IP 登录，请改用域名地址；` +
          '若本机装了代理/ VPN（TUN 模式），内网 IP 也可能走代理而失败，可改为直连或用域名'
      }
    }
    return {
      level: 'warn',
      hint: `这是${kind}，仅在与服务器同一内网时可用。若装了代理软件（尤其 TUN 模式），` +
        '内网 IP 可能被代理接管而连不上——建议改用域名或在代理规则里把该地址设为直连'
    }
  }

  // 公网 IP：不判误配（生产禁用 IP 登录时服务器自己会拒）。
  return { level: 'ok', hint: '' }
}
