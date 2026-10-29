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
  default: { post: vi.fn(), get: vi.fn(), defaults: { baseURL: '' } }
}))
vi.mock('@/api/local/transport', () => ({ IS_DESKTOP: false }))
vi.mock('@/api/local/desktopApi', () => ({ exportNoteMD: vi.fn() }))
vi.mock('@/utils/notePoster', () => ({ saveBlobDownload: vi.fn() }))
vi.mock('@/utils/message', () => ({
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() }
}))

import { encryptNote, verifyEncrypt } from '@/api/note'
import { message } from '@/utils/message'
import api from '@/api/request'
import { saveBlobDownload } from '@/utils/notePoster'
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

// v2.2.2 R2 下载通道统一（桌面 window.open 空窗缺陷 + Web 通道归一）：
// 非加密 = GET blob；加密 = POST blob（密码入 body 不入 URL）；落盘统一 saveBlobDownload。
describe('useNoteEncrypt 下载通道', () => {
  it('非加密笔记 → GET blob + saveBlobDownload（不再 window.open）', async () => {
    const ctx = makeCtx()
    const { downloadNote } = useNoteEncrypt(ctx.currentNote, ctx.sync)
    api.get.mockResolvedValue({ data: new Blob(['# md']) })

    await downloadNote()

    expect(api.get).toHaveBeenCalledWith('/notes/1/export', { responseType: 'blob' })
    expect(saveBlobDownload).toHaveBeenCalled()
    const [blob, filename] = saveBlobDownload.mock.calls[0]
    expect(filename).toBe('t.md')
    expect(blob).toBeInstanceOf(Blob)
  })

  it('加密未解锁 → 弹口令框；带密码下载 → POST blob（密码入 body）', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value = { id: 2, title: 'enc', content: '', is_encrypted: true }
    const { downloadNote, pwdDialogVisible, pwdDialogMode, handlePwdSubmit, pwdInput } = useNoteEncrypt(ctx.currentNote, ctx.sync)

    downloadNote()
    expect(pwdDialogVisible.value).toBe(true)
    expect(pwdDialogMode.value).toBe('download')

    pwdInput.value = 'secret'
    api.post.mockResolvedValue({ data: new Blob(['# enc']) })
    await handlePwdSubmit()

    expect(api.post).toHaveBeenCalledWith('/notes/2/export', { password: 'secret' }, { responseType: 'blob' })
    expect(saveBlobDownload).toHaveBeenCalled()
  })
})
