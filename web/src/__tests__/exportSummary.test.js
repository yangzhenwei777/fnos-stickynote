// 导出提示文案拼装（开发纪律 A-15.2）：Web 读响应头、桌面读 body，但文案只此一份。
// 回归点（2026-10-30 用户报障）：桌面「导出全部」漏「N 条加密文件夹中的笔记」。
import { describe, it, expect } from 'vitest'
import { buildSkippedParts, joinSkipped, buildDesktopExportMessage, buildWebExportAllMessage } from '@/utils/exportSummary'

describe('exportSummary 跳过项文案', () => {
  it('两类跳过都拼出（顺序：加密笔记 → 加密文件夹中的笔记）', () => {
    expect(buildSkippedParts(2, 3)).toEqual(['2 篇加密笔记', '3 条加密文件夹中的笔记'])
    expect(joinSkipped(buildSkippedParts(2, 3))).toBe('跳过 2 篇加密笔记、3 条加密文件夹中的笔记')
  })

  it('只有锁定文件夹（加密笔记数 0）也必须出文案 —— 用户报障场景', () => {
    expect(joinSkipped(buildSkippedParts(0, 4))).toBe('跳过 4 条加密文件夹中的笔记')
  })

  it('只有加密笔记（旧行为不回归）', () => {
    expect(joinSkipped(buildSkippedParts(5, 0))).toBe('跳过 5 篇加密笔记')
  })

  it('无跳过 → 空串（调用方走"全部导出/已保存"分支）', () => {
    expect(buildSkippedParts(0, 0)).toEqual([])
    expect(joinSkipped([])).toBe('')
  })

  it('字符串/undefined 归一（header 值来自 HTTP 头，是字符串）', () => {
    expect(joinSkipped(buildSkippedParts('3', undefined))).toBe('跳过 3 篇加密笔记')
    expect(buildSkippedParts('', '')).toEqual([])
    expect(buildSkippedParts(null, null)).toEqual([])
  })
})

describe('桌面「导出全部/批量」文案（body 口径）—— 用户报障回归', () => {
  it('只有锁定文件夹：必须提示「N 条加密文件夹中的笔记」（2.3.0 漏的就是这句）', () => {
    expect(buildDesktopExportMessage({ exported: 5, skipped: 0, foldersSkipped: 4, savedPath: 'C:/a.zip' }))
      .toBe('已导出 5 篇笔记，跳过 4 条加密文件夹中的笔记')
  })

  it('两类跳过都在 → 顿号连接', () => {
    expect(buildDesktopExportMessage({ exported: 5, skipped: 2, foldersSkipped: 4, savedPath: 'C:/a.zip' }))
      .toBe('已导出 5 篇笔记，跳过 2 篇加密笔记、4 条加密文件夹中的笔记')
  })

  it('无跳过 → 显示保存路径', () => {
    expect(buildDesktopExportMessage({ exported: 5, skipped: 0, foldersSkipped: 0, savedPath: 'C:/a.zip' }))
      .toBe('已导出 5 篇笔记，已保存到 C:/a.zip')
  })

  it('路径缺失走兜底文案（不出现 undefined）', () => {
    expect(buildDesktopExportMessage({ exported: 1 })).toBe('已导出 1 篇笔记，已保存到 所选位置')
  })
})

describe('Web「导出全部」文案（header 口径）', () => {
  it('两类跳过都在', () => {
    expect(buildWebExportAllMessage({ encryptedSkipped: '2', folderSkipped: '3' }))
      .toBe('导出成功，已跳过 2 篇加密笔记、3 条加密文件夹中的笔记')
  })
  it('无跳过', () => {
    expect(buildWebExportAllMessage({})).toBe('导出成功，已导出全部笔记')
  })
})
