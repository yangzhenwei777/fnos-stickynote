/**
 * resolveNoteFolderHint 测试（2.6.7）：正常/边界/错误三态。
 * 该纯函数决定「全部笔记」视图选中笔记在左侧文件夹列表显示蓝点的归属。
 */
import { describe, it, expect } from 'vitest'
import { resolveNoteFolderHint } from '@/utils/noteFolderHint'

describe('resolveNoteFolderHint', () => {
  it('无选中笔记 → null（不显示提示）', () => {
    expect(resolveNoteFolderHint(null)).toBeNull()
    expect(resolveNoteFolderHint(undefined)).toBeNull()
  })

  it('默认「我的笔记」：folder_id 缺省 / null / 空串 / 0 → default', () => {
    expect(resolveNoteFolderHint({})).toBe('default')
    expect(resolveNoteFolderHint({ folder_id: null })).toBe('default')
    expect(resolveNoteFolderHint({ folder_id: undefined })).toBe('default')
    expect(resolveNoteFolderHint({ folder_id: '' })).toBe('default')
    expect(resolveNoteFolderHint({ folder_id: 0 })).toBe('default')
    expect(resolveNoteFolderHint({ folder_id: '0' })).toBe('default')
  })

  it('自定义文件夹：数字 / 数字串 → number', () => {
    expect(resolveNoteFolderHint({ folder_id: 12 })).toBe(12)
    expect(resolveNoteFolderHint({ folder_id: '12' })).toBe(12)
  })

  it('非法值 → null（不误标任何文件夹）', () => {
    expect(resolveNoteFolderHint({ folder_id: 'abc' })).toBeNull()
    expect(resolveNoteFolderHint({ folder_id: -3 })).toBeNull()
    expect(resolveNoteFolderHint({ folder_id: Number.NaN })).toBeNull()
  })
})
