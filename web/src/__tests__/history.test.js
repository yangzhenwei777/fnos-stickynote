import { describe, it, expect } from 'vitest'
import { formatHistorySize, historySourceLabel } from '@/utils/history'

describe('2.5.0 版本历史工具', () => {
  it('大小格式化覆盖 B/KB/MB', () => {
    expect(formatHistorySize(0)).toBe('0 B')
    expect(formatHistorySize(1023)).toBe('1023 B')
    expect(formatHistorySize(2048)).toBe('2.0 KB')
    expect(formatHistorySize(3 * 1024 * 1024)).toBe('3.0 MB')
  })

  it('来源标签：保存 / 自动快照', () => {
    expect(historySourceLabel('user')).toBe('保存')
    expect(historySourceLabel('auto')).toBe('自动快照')
    expect(historySourceLabel(undefined)).toBe('保存')
  })
})
