/**
 * v2.3.0 文件夹访问锁：全局密码弹框状态机。
 * mock folder store 的加密/解密/解锁动作，验证模式文案、校验与成功后回调。
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const folderStore = {
  encryptFolderById: vi.fn(),
  decryptFolderById: vi.fn(),
  unlockFolderById: vi.fn()
}
vi.mock('@/stores/folder', () => ({ useFolderStore: () => folderStore }))
vi.mock('@/utils/message', () => ({
  message: { success: vi.fn(), warning: vi.fn(), error: vi.fn() }
}))

import { message } from '@/utils/message'
import { useFolderSecurityStore } from '@/stores/folderSecurity'

const folder = { id: 7, name: '秘密', is_encrypted: false }

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
})

describe('folderSecurity 弹框', () => {
  it('加密模式：标题/警示文案/双输入', () => {
    const s = useFolderSecurityStore()
    s.openEncrypt(folder)
    expect(s.visible).toBe(true)
    expect(s.mode).toBe('encrypt')
    expect(s.title).toBe('加密文件夹')
    expect(s.warningText).toContain('隐藏')
  })

  it('解密/解锁模式：单输入，无加密警示', () => {
    const s = useFolderSecurityStore()
    s.openDecrypt(folder)
    expect(s.title).toBe('解密文件夹')
    expect(s.warningText).toBe('')
    s.openUnlock(folder)
    expect(s.title).toBe('解锁加密文件夹')
    expect(s.placeholder).toBe('请输入文件夹密码')
  })

  it('空密码/两次不一致 → warning 且不调用 API', async () => {
    const s = useFolderSecurityStore()
    s.openEncrypt(folder)
    await s.submit()
    expect(message.warning).toHaveBeenCalledWith('请输入密码')
    s.input = 'abc123'
    s.confirm = 'other'
    await s.submit()
    expect(message.warning).toHaveBeenCalledWith('两次输入的密码不一致')
    expect(folderStore.encryptFolderById).not.toHaveBeenCalled()
  })

  it('加密成功 → 调 store、提示并关闭', async () => {
    folderStore.encryptFolderById.mockResolvedValue({})
    const s = useFolderSecurityStore()
    s.openEncrypt(folder)
    s.input = 'abc123'
    s.confirm = 'abc123'
    await s.submit()
    expect(folderStore.encryptFolderById).toHaveBeenCalledWith(7, 'abc123', 'abc123')
    expect(message.success).toHaveBeenCalledWith('文件夹已加密')
    expect(s.visible).toBe(false)
  })

  it('解锁成功 → 执行待执行回调', async () => {
    folderStore.unlockFolderById.mockResolvedValue({})
    const cb = vi.fn()
    const s = useFolderSecurityStore()
    s.openUnlock(folder, cb)
    s.input = 'abc123'
    await s.submit()
    expect(folderStore.unlockFolderById).toHaveBeenCalledWith(7, 'abc123')
    expect(cb).toHaveBeenCalled()
    expect(s.visible).toBe(false)
  })

  it('429 → 使用后端文案提示', async () => {
    folderStore.unlockFolderById.mockRejectedValue({
      response: { status: 429, data: { error: '密码尝试次数过多，请在 9 分钟后再试' } }
    })
    const s = useFolderSecurityStore()
    s.openUnlock(folder)
    s.input = 'bad'
    await s.submit()
    expect(message.error).toHaveBeenCalledWith('密码尝试次数过多，请在 9 分钟后再试')
    expect(s.visible).toBe(true)
  })

  it('取消关闭 → 清空输入', () => {
    const s = useFolderSecurityStore()
    s.openUnlock(folder, () => {})
    s.input = 'abc'
    s.close()
    expect(s.visible).toBe(false)
    expect(s.input).toBe('')
  })
})
