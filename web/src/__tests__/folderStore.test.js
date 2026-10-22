/**
 * useFolderStore 测试（v21.86）：纯逻辑——listParam 计算 + selectFolder + removeFolder 的 notEmpty 分支
 * mock：@/api/folder、@/utils/message、pinia
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('@/api/folder', () => ({
  getFolders: vi.fn(),
  createFolder: vi.fn(),
  renameFolder: vi.fn(),
  moveFolder: vi.fn(),
  deleteFolder: vi.fn()
}))
vi.mock('@/utils/message', () => ({
  message: { success: vi.fn(), error: vi.fn() }
}))

import { getFolders, createFolder, deleteFolder } from '@/api/folder'
import { useFolderStore } from '@/stores/folder'

const defaultFolder = { id: null, name: '我的笔记', is_default: true, sort_order: 0, note_count: 2 }
const customA = { id: 10, name: '工作', is_default: false, sort_order: 1, note_count: 1 }
const customB = { id: 11, name: '生活', is_default: false, sort_order: 2, note_count: 0 }

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  getFolders.mockResolvedValue({ data: { folders: [defaultFolder, customA, customB] } })
})

describe('listParam 计算', () => {
  it('默认 all → 不传 folder_id（undefined）', () => {
    const s = useFolderStore()
    expect(s.activeFolderId).toBe('all')
    expect(s.listParam).toBeUndefined()
  })

  it('selectFolder("default") → "default"', () => {
    const s = useFolderStore()
    s.selectFolder('default')
    expect(s.activeFolderId).toBe('default')
    expect(s.listParam).toBe('default')
  })

  it('selectFolder(10) → "10"', () => {
    const s = useFolderStore()
    s.selectFolder(10)
    expect(s.activeFolderId).toBe(10)
    expect(s.listParam).toBe('10')
  })
})

describe('activeFolder 计算', () => {
  it('all → null', async () => {
    const s = useFolderStore()
    await s.loadFolders()
    s.selectFolder('all')
    expect(s.activeFolder).toBeNull()
  })

  it('default → 默认文件夹对象', async () => {
    const s = useFolderStore()
    await s.loadFolders()
    s.selectFolder('default')
    expect(s.activeFolder).toStrictEqual(defaultFolder)
  })

  it('自定义 id → 对应文件夹', async () => {
    const s = useFolderStore()
    await s.loadFolders()
    s.selectFolder(10)
    expect(s.activeFolder).toStrictEqual(customA)
  })
})

describe('createNewFolder', () => {
  it('成功 → 追加到 folders 末尾', async () => {
    const newOne = { id: 12, name: '新夹', is_default: false, sort_order: 3, note_count: 0 }
    createFolder.mockResolvedValue({ data: newOne })
    const s = useFolderStore()
    await s.loadFolders()
    const f = await s.createNewFolder('新夹')
    expect(f).toStrictEqual(newOne)
    expect(s.folders).toHaveLength(4)
    expect(s.folders[3]).toStrictEqual(newOne)
  })

  it('失败（重名 409）→ 不追加，message.error 已调', async () => {
    createFolder.mockRejectedValue({ response: { status: 409, data: { error: '文件夹名称已存在' } } })
    const s = useFolderStore()
    await s.loadFolders()
    const f = await s.createNewFolder('工作')
    expect(f).toBeNull()
    expect(s.folders).toHaveLength(3)
  })
})

describe('removeFolder', () => {
  it('空文件夹 → 删除成功', async () => {
    deleteFolder.mockResolvedValue({})
    const s = useFolderStore()
    await s.loadFolders()
    const res = await s.removeFolder(11) // 生活（note_count=0）
    expect(res.ok).toBe(true)
    expect(s.folders).toHaveLength(2)
  })

  it('非空文件夹 → notEmpty=true，列表不变', async () => {
    deleteFolder.mockRejectedValue({ response: { status: 409, data: { code: 'FOLDER_NOT_EMPTY' } } })
    const s = useFolderStore()
    await s.loadFolders()
    const res = await s.removeFolder(10) // 工作（note_count=1）
    expect(res.ok).toBe(false)
    expect(res.notEmpty).toBe(true)
    expect(s.folders).toHaveLength(3) // 不变
  })

  it('删除当前选中文件夹 → activeFolderId 回退到 all', async () => {
    deleteFolder.mockResolvedValue({})
    const s = useFolderStore()
    await s.loadFolders()
    s.selectFolder(11)
    await s.removeFolder(11)
    expect(s.activeFolderId).toBe('all')
  })
})
