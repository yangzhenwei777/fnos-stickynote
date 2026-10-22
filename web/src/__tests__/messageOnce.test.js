/**
 * v21.107 P1-6：message.errorOnce 同文案窗口去重（拦截器全局兜底与透传调用点
 * 并存时避免双弹）。mock utils/toast 验证调用次数。
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'

vi.mock('@/utils/toast', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }
}))

import { toast } from '@/utils/toast'
import { message } from '@/utils/message'

beforeEach(() => {
  vi.clearAllMocks()
})

describe('message.errorOnce 去重（v21.107 P1-6）', () => {
  it('同文案窗口内只弹一次', () => {
    message.errorOnce('存储空间不足，请先清理图片', 4000)
    message.errorOnce('存储空间不足，请先清理图片', 4000)
    message.errorOnce('存储空间不足，请先清理图片', 4000)
    expect(toast.error).toHaveBeenCalledTimes(1)
  })

  it('不同文案互不影响', () => {
    message.errorOnce('文案A', 4000)
    message.errorOnce('文案B', 4000)
    expect(toast.error).toHaveBeenCalledTimes(2)
  })

  it('窗口过期后可再次弹出', () => {
    vi.useFakeTimers()
    message.errorOnce('限流提示', 3000)
    vi.advanceTimersByTime(3100)
    message.errorOnce('限流提示', 3000)
    expect(toast.error).toHaveBeenCalledTimes(2)
    vi.useRealTimers()
  })
})
