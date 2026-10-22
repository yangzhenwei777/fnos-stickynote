import { describe, it, expect } from 'vitest'
import { extractApiError } from '@/utils/apiError'

// v21.131 测试轮：blob 下载错误解析单测（编码 v21.130 "下载失败弹笼统文案" bug 的回归）
describe('extractApiError', () => {
  it('blob 响应：读回 JSON 抽出后端具体 error', async () => {
    const blob = new Blob([JSON.stringify({ error: '未找到可下载副本（已被轮转删除）' })], { type: 'application/json' })
    const e = { response: { data: blob } }
    expect(await extractApiError(e, '下载失败')).toBe('未找到可下载副本（已被轮转删除）')
  })

  it('blob 内容非 JSON：回退兜底文案', async () => {
    const blob = new Blob(['<html>502 Bad Gateway</html>'], { type: 'text/html' })
    const e = { response: { data: blob } }
    expect(await extractApiError(e, '下载失败')).toBe('下载失败')
  })

  it('blob JSON 但无 error 字段：回退兜底', async () => {
    const blob = new Blob([JSON.stringify({ ok: false })], { type: 'application/json' })
    const e = { response: { data: blob } }
    expect(await extractApiError(e, '下载失败')).toBe('下载失败')
  })

  it('普通 JSON 错误响应：取 data.error', async () => {
    const e = { response: { data: { error: '参数错误' } } }
    expect(await extractApiError(e, '默认')).toBe('参数错误')
  })

  it('无 response.data：取 e.message', async () => {
    const e = { message: 'Network Error' }
    expect(await extractApiError(e, '默认')).toBe('Network Error')
  })

  it('啥都没有：返回 fallback', async () => {
    expect(await extractApiError({}, '操作失败')).toBe('操作失败')
  })
})
