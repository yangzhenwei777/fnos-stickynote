/**
 * useNoteEncrypt 加密笔记密码注入测试（v21.58 A-1：固化 F-01/F-11 密码注入回归点）
 * mock：api/note、api/request、utils/message
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref } from 'vue'

vi.mock('@/api/note', () => ({
  encryptNote: vi.fn().mockResolvedValue({}),
  decryptNote: vi.fn().mockResolvedValue({}),
  verifyEncrypt: vi.fn().mockResolvedValue({ data: { id: 1, content: '<p>明文</p>', is_encrypted: true } }),
  getNote: vi.fn().mockResolvedValue({ data: { id: 1, content: '<p>明文</p>', is_encrypted: false } })
}))
vi.mock('@/api/request', () => ({
  default: { post: vi.fn(), defaults: { baseURL: '' } }
}))
vi.mock('@/utils/message', () => ({
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() }
}))

import { encryptNote, verifyEncrypt } from '@/api/note'
import { message } from '@/utils/message'
import { useNoteEncrypt } from '@/composables/useNoteEncrypt'

function makeCtx() {
  const currentNote = ref({ id: 1, title: 't', content: '<p>x</p>', is_encrypted: false })
  const sync = vi.fn()
  return { currentNote, sync }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('useNoteEncrypt 密码注入与状态', () => {
  it('showEncryptDialog → 进入 encrypt 模式并弹窗', () => {
    const ctx = makeCtx()
    const { showEncryptDialog, pwdDialogMode, pwdDialogVisible } = useNoteEncrypt(ctx.currentNote, ctx.sync)

    showEncryptDialog()
    expect(pwdDialogMode.value).toBe('encrypt')
    expect(pwdDialogVisible.value).toBe(true)
  })

  it('encrypt 模式密码不一致 → warning 且不调用 encryptNote', async () => {
    const ctx = makeCtx()
    const { showEncryptDialog, handlePwdSubmit, pwdInput, pwdConfirm } = useNoteEncrypt(ctx.currentNote, ctx.sync)

    showEncryptDialog()
    pwdInput.value = 'secret'
    pwdConfirm.value = 'mismatch'
    await handlePwdSubmit()

    expect(message.warning).toHaveBeenCalled()
    expect(encryptNote).not.toHaveBeenCalled()
  })

  it('encrypt 成功 → is_encrypted=true、content 清空、encryptUnlocked=false', async () => {
    const ctx = makeCtx()
    const { showEncryptDialog, handlePwdSubmit, pwdInput, pwdConfirm, encryptUnlocked } = useNoteEncrypt(ctx.currentNote, ctx.sync)

    showEncryptDialog()
    pwdInput.value = 'secret'
    pwdConfirm.value = 'secret'
    await handlePwdSubmit()

    expect(encryptNote).toHaveBeenCalledWith(1, 'secret', 'secret')
    expect(ctx.currentNote.value.is_encrypted).toBe(true)
    expect(ctx.currentNote.value.content).toBe('')
    expect(encryptUnlocked.value).toBe(false)
  })

  it('view 模式 verifyEncrypt 成功 → encryptUnlocked=true 且注入 encryptPassword（保存密码来源）', async () => {
    const ctx = makeCtx()
    const { showPasswordDialog, handlePwdSubmit, pwdInput, encryptUnlocked, encryptPassword } = useNoteEncrypt(ctx.currentNote, ctx.sync)

    showPasswordDialog('view')
    pwdInput.value = 'secret'
    await handlePwdSubmit()

    expect(verifyEncrypt).toHaveBeenCalledWith(1, 'secret')
    expect(encryptUnlocked.value).toBe(true)
    expect(encryptPassword.value).toBe('secret') // 关键：自动保存/切换保存依赖此密码注入
  })

  it('resetOnNoteChange 加密笔记 → 清空解锁状态与明文', () => {
    const ctx = makeCtx()
    ctx.currentNote.value = { id: 1, title: 't', content: '<p>明文</p>', is_encrypted: true }
    const { resetOnNoteChange, encryptUnlocked, encryptPassword } = useNoteEncrypt(ctx.currentNote, ctx.sync)
    encryptUnlocked.value = true
    encryptPassword.value = 'secret'

    resetOnNoteChange()
    expect(encryptUnlocked.value).toBe(false)
    expect(encryptPassword.value).toBe('')
    expect(ctx.currentNote.value.content).toBe('')
  })
})
