import { describe, it, expect } from 'vitest'
import { isPrivateIPv4, parseServerHost, inspectServerURL } from '../utils/serverUrlInspector'

/**
 * v21.202 · 服务器地址误配检测回归钉。
 *
 * 定罪（用户实机 2026-10-10）：生产已**禁用 IP 登录**仅支持域名，而登录框
 * placeholder 是内网 IP `http://192.0.2.10:13389` → 用户照填必然连不通，
 * 且报错是传输层噪声（`net/http: TLS handshake timeout`），不知原因。
 * 本模块把「必然失败」提前到点按钮那一刻就说清。
 */
describe('isPrivateIPv4 私网识别', () => {
  it.each([
    ['10.0.0.1', true],
    ['192.168.31.100', true],
    ['172.16.0.1', true],
    ['172.31.255.254', true],
    ['127.0.0.1', true],
    ['169.254.1.1', true],
    ['100.64.0.1', true]
  ])('%s -> %s', (ip, want) => {
    expect(isPrivateIPv4(ip)).toBe(want)
  })

  it.each([
    ['8.8.8.8', false],
    ['172.15.0.1', false],   // 172.15 不在 RFC1918 段
    ['172.32.0.1', false],   // 172.32 已出段
    ['1.1.1.1', false],
    ['', false],
    ['nas.local', false],     // 域名不判
    ['::1', false],          // IPv6 不猜
    ['notanip', false],
    ['999.1.1.1', false]     // 越界段
  ])('%s -> %s', (ip, want) => {
    expect(isPrivateIPv4(ip)).toBe(want)
  })
})

describe('parseServerHost 主机解析', () => {
  it('各形态均正确取 host', () => {
    expect(parseServerHost('https://note.example.com/')).toBe('note.example.com')
    expect(parseServerHost('http://192.0.2.10:13389')).toBe('192.0.2.10')
    expect(parseServerHost('https://example.com:8443/api')).toBe('example.com')
    expect(parseServerHost('http://[::1]:13389')).toBe('::1')
  })
  it('空/垃圾输入返回空串（不当成误配）', () => {
    expect(parseServerHost('')).toBe('')
    expect(parseServerHost('   ')).toBe('')
  })
})

describe('inspectServerURL 误配诊断', () => {
  it('域名地址 = ok（最常见的正确用法，不打扰）', () => {
    const r = inspectServerURL('https://note.example.com/')
    expect(r.level).toBe('ok')
    expect(r.hint).toBe('')
  })

  it('缺协议 = error（提示补http/https）', () => {
    const r = inspectServerURL('note.example.com')
    expect(r.level).toBe('error')
    expect(r.hint).toContain('http://')
  })

  it('生产禁用 IP 登录时，内网 IP = error 且提示改用域名', () => {
    const r = inspectServerURL('http://192.168.31.100:13389', { ipLoginDisabled: true })
    expect(r.level).toBe('error')
    expect(r.hint).toContain('禁用 IP 登录')
    expect(r.hint).toContain('域名')
  })

  it('允许 IP 登录时，内网 IP 仅 warn（不阻断——同内网确实可用）', () => {
    const r = inspectServerURL('http://192.168.31.100:13389', { ipLoginDisabled: false })
    expect(r.level).toBe('warn')
    expect(r.hint).toContain('代理')
  })

  it('内网 IP 提示必须提到代理（代理 TUN/Fake-IP 是本次实机真凶）', () => {
    const r = inspectServerURL('http://10.0.0.5:13389')
    expect(r.level).not.toBe('ok')
    expect(r.hint).toMatch(/代理|直连/)
  })

  it('公网 IP = ok（服务器自己会拒，不替用户判断）', () => {
    const r = inspectServerURL('https://203.0.113.10:13389')
    expect(r.level).toBe('ok')
  })

  it('无法解析 = error', () => {
    const r = inspectServerURL('https://')
    expect(r.level).toBe('error')
  })

  it('空地址 = ok（不阻塞用户刚开始输入）', () => {
    expect(inspectServerURL('').level).toBe('ok')
  })
})
