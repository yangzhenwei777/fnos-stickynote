// loginError.test.js —— v2.1.4 登录错误分类门禁。
//
// 背景：安卓中继链路实测 CORS 403 空 body，旧逻辑只显示裸「登录失败」，
// 用户无从判断该找谁。这条门禁钉住四类文案的分流，防止将来退回裸文案。

import { describe, it, expect } from 'vitest'
import { describeLoginError } from '@/utils/loginError'

describe('describeLoginError 分类', () => {
  it('① 业务错误：原样展示服务端中文提示', () => {
    expect(describeLoginError({
      response: { status: 401, data: { error: '用户名或密码错误' } }
    })).toBe('用户名或密码错误')
    expect(describeLoginError({
      response: { status: 429, data: { error: '尝试次数过多，请 5 分钟后再试' } }
    })).toBe('尝试次数过多，请 5 分钟后再试')
  })

  it('② CORS/网关拦截：非 JSON 响应必须给出可诊断文案（不能裸「登录失败」）', () => {
    // 复刻安卓实测形态：CORS 403 空 body
    const msg = describeLoginError({ response: { status: 403, data: '' } })
    expect(msg).toContain('HTTP 403')
    expect(msg).toContain('拦截')
    expect(msg).not.toBe('登录失败')
    // 网关 502 HTML
    expect(describeLoginError({ response: { status: 502, data: '<html>' } })).toContain('502')
  })

  it('③ 网络层失败：给连接类提示', () => {
    expect(describeLoginError({ code: 'ERR_NETWORK' })).toContain('无法连接服务器')
    expect(describeLoginError({ message: 'Network Error' })).toContain('无法连接服务器')
  })

  it('④ 兜底：永远带原因，绝不出现裸的「登录失败」', () => {
    expect(describeLoginError({ message: 'timeout of 20000ms exceeded' }))
      .toBe('登录失败：timeout of 20000ms exceeded')
    expect(describeLoginError(undefined)).toBe('登录失败：未知错误')
    expect(describeLoginError({})).toBe('登录失败：未知错误')
  })
})
