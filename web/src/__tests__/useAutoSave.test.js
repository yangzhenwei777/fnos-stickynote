/**
 * useAutoSave 自动保存测试（v21.54 P1-A：加密密码注入——v21.46 闭包 bug 回归点）
 * mock：api/note.updateNote、idb-keyval、stores/auth、utils/message、ElMessageBox
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref } from 'vue'

// ---- mock 依赖 ----
vi.mock('@/api/note', () => ({
  updateNote: vi.fn().mockResolvedValue({})
}))
vi.mock('idb-keyval', () => ({
  get: vi.fn().mockResolvedValue(undefined),
  set: vi.fn().mockResolvedValue(undefined),
  del: vi.fn().mockResolvedValue(undefined)
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 1, username: 'test' } })
}))
vi.mock('@/utils/message', () => ({
  message: { success: vi.fn(), error: vi.fn() }
}))
// 注：useAutoSave 使用全局 ElMessageBox（未 import element-plus），无需 mock；
// 本测试不触发 checkDraft 分支，故也不需设置 globalThis.ElMessageBox。

import { updateNote } from '@/api/note'
import { get as idbGet, set as idbSet, del as idbDel } from 'idb-keyval'
import { message } from '@/utils/message'
import { useAutoSave } from '@/composables/useAutoSave'

function makeCtx() {
  const currentNote = ref({ id: 1, title: 't', content: '<p>c</p>', tags: '', is_encrypted: true })
  const currentTagInput = ref([])
  const isDirty = ref(false)
  const applied = []
  return { currentNote, currentTagInput, isDirty, applied }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers()
})

describe('useAutoSave 加密密码注入', () => {
  it('加密笔记 + getEncryptPassword 回调 → payload 注入 password', async () => {
    const ctx = makeCtx()
    const getPwd = vi.fn(() => 'secret123')
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, getPwd)

    await saveNote(false)
    expect(updateNote).toHaveBeenCalledWith(1, expect.objectContaining({ password: 'secret123' }))
  })

  it('加密笔记 + 显式密码参数 → 优先使用显式参数', async () => {
    const ctx = makeCtx()
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => 'cb-pwd')

    await saveNote(false, 'explicit-pwd')
    expect(updateNote).toHaveBeenCalledWith(1, expect.objectContaining({ password: 'explicit-pwd' }))
  })

  it('加密笔记但无密码来源 → 不注入 password（后端将 400，符合预期）', async () => {
    const ctx = makeCtx()
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {})

    await saveNote(false)
    const payload = updateNote.mock.calls[0][1]
    expect(payload.password).toBeUndefined()
  })

  it('非加密笔记 → 即使有回调也不注入 password', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => 'pwd')

    await saveNote(false)
    const payload = updateNote.mock.calls[0][1]
    expect(payload.password).toBeUndefined()
  })
})

describe('useAutoSave saveBeforeSwitch 切换前保存', () => {
  it('加密旧笔记切换 → 注入密码（v21.46 静默吞掉回归点）', async () => {
    const ctx = makeCtx()
    ctx.isDirty.value = true
    const { saveBeforeSwitch } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => 'switch-pwd')

    const prev = { id: 5, title: '旧', content: '<p>旧内容</p>', tags: '', is_encrypted: true }
    await saveBeforeSwitch(prev)
    expect(updateNote).toHaveBeenCalledWith(5, expect.objectContaining({ password: 'switch-pwd' }))
  })

  it('保存失败 → 返回 false（F-01 阻止切换，不丢数据）', async () => {
    const ctx = makeCtx()
    ctx.isDirty.value = true
    updateNote.mockRejectedValueOnce(new Error('network'))
    const { saveBeforeSwitch } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {})

    const prev = { id: 5, title: '旧', content: '<p>旧内容</p>', tags: '', is_encrypted: false }
    const ok = await saveBeforeSwitch(prev)
    expect(ok).toBe(false)
  })

  it('保存成功 → 返回 true', async () => {
    const ctx = makeCtx()
    ctx.isDirty.value = true
    const { saveBeforeSwitch } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {})

    const prev = { id: 5, title: '旧', content: '<p>旧内容</p>', tags: '', is_encrypted: false }
    const ok = await saveBeforeSwitch(prev)
    expect(ok).toBe(true)
  })
})

describe('useAutoSave markDirty 自动保存调度', () => {
  it('markDirty 后 800ms 触发 saveNote（加密笔记经回调注入密码）', async () => {
    const ctx = makeCtx()
    const getPwd = vi.fn(() => 'auto-pwd')
    const { markDirty } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, getPwd)

    markDirty()
    expect(ctx.isDirty.value).toBe(true)
    await vi.advanceTimersByTimeAsync(900)
    expect(updateNote).toHaveBeenCalled()
    expect(updateNote.mock.calls[0][1].password).toBe('auto-pwd')
  })
})

describe('useAutoSave checkDraft 草稿恢复竞态（v21.66 P1-2）', () => {
  it('确认草稿时当前笔记已切换，不得覆盖新笔记', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value = { id: 1, title: 'A', content: '<p>A</p>', tags: '', is_encrypted: false }
    const { checkDraft } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {})

    // 注入旧草稿（比 server 版本新）
    idbGet.mockResolvedValueOnce({ title: '旧草稿', content: '<p>旧内容</p>', tags: '', saved_at: Date.now() + 100000 })

    // mock 全局 ElMessageBox.confirm：返回可手动 resolve 的 promise，模拟用户未点击
    let resolveConfirm
    globalThis.ElMessageBox = {
      confirm: vi.fn(() => new Promise((res) => { resolveConfirm = res }))
    }

    const p = checkDraft({ id: 1, updated_at: new Date(0).toISOString(), is_encrypted: false })

    // 等待 checkDraft 推进到 confirm 调用（此时 resolveConfirm 已被赋值）
    await vi.waitFor(() => expect(globalThis.ElMessageBox.confirm).toHaveBeenCalled())

    // 弹窗未确认时切换到笔记 B
    ctx.currentNote.value = { id: 2, title: 'B', content: '<p>B内容</p>', tags: '', is_encrypted: false }

    // 用户此时才点击「恢复草稿」
    resolveConfirm('confirm')
    await p

    // B 不得被 A 的旧草稿覆盖
    expect(ctx.currentNote.value.id).toBe(2)
    expect(ctx.currentNote.value.content).toBe('<p>B内容</p>')
  })
})

describe('useAutoSave version 同步（v21.75 修复 409 bug）', () => {
  it('保存成功后同步 resp.data.version 到 currentNote', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    ctx.currentNote.value.version = 1
    updateNote.mockResolvedValueOnce({ data: { message: '更新成功', version: 2 } })
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {})

    await saveNote(false)
    expect(ctx.currentNote.value.version).toBe(2)
  })

  it('保存时携带 version 到 payload', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    ctx.currentNote.value.version = 3
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {})

    await saveNote(false)
    expect(updateNote).toHaveBeenCalledWith(1, expect.objectContaining({ version: 3 }))
  })
})

describe('useAutoSave 列表 version 同步（v21.84 修复 409：切走再切回不丢最新 version）', () => {
  it('saveNote 成功后把新 version + 服务端 updated_at 传给 applySavedToLists', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    ctx.currentNote.value.version = 1
    updateNote.mockResolvedValueOnce({ data: { message: '更新成功', version: 2, updated_at: '2026-09-24T05:00:00Z' } })
    const applied = []
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, (...a) => applied.push(a), () => {})

    await saveNote(false)
    // [id, title, content, tags, version, updated_at]
    expect(applied[0][4]).toBe(2)
    expect(applied[0][5]).toBe('2026-09-24T05:00:00Z') // v21.150 E7：排序键=服务端时间
  })

  it('saveBeforeSwitch 成功后把新 version + 服务端 updated_at 传给 applySavedToLists', async () => {
    const ctx = makeCtx()
    ctx.isDirty.value = true
    updateNote.mockResolvedValueOnce({ data: { message: '更新成功', version: 5, updated_at: '2026-09-24T06:00:00Z' } })
    const applied = []
    const { saveBeforeSwitch } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, (...a) => applied.push(a), () => {})

    const prev = { id: 5, title: '旧', content: '<p>旧内容</p>', tags: '', is_encrypted: false }
    const ok = await saveBeforeSwitch(prev)
    expect(ok).toBe(true)
    expect(applied[0][0]).toBe(5)
    expect(applied[0][4]).toBe(5)
    expect(applied[0][5]).toBe('2026-09-24T06:00:00Z')
  })
})

describe('useAutoSave 保存前内容刷新（v21.85 编辑器防抖回流）', () => {
  it('saveNote 先调 ensureFreshContent 并把返回值写入 content 再构造 payload', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    ctx.currentNote.value.content = '<p>过期内容</p>'
    const ensureFresh = vi.fn(() => '<p>最新内容</p>')
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '', ensureFresh)

    await saveNote(false)
    expect(ensureFresh).toHaveBeenCalledTimes(1)
    expect(updateNote).toHaveBeenCalledWith(1, expect.objectContaining({ content: '<p>最新内容</p>' }))
    expect(ctx.currentNote.value.content).toBe('<p>最新内容</p>')
  })

  it('ensureFreshContent 返回空（预览态/无编辑器）→ 保留现有 content', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    ctx.currentNote.value.content = '<p>现值</p>'
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '', () => '')

    await saveNote(false)
    expect(updateNote).toHaveBeenCalledWith(1, expect.objectContaining({ content: '<p>现值</p>' }))
  })

  it('未提供 ensureFreshContent（旧调用签名）→ 仍用 currentNote.content', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {})

    await saveNote(false)
    expect(updateNote).toHaveBeenCalledWith(1, expect.objectContaining({ content: '<p>c</p>' }))
  })

  it('saveBeforeSwitch：prevNote 即 currentNote → 同样刷新内容', async () => {
    const ctx = makeCtx()
    ctx.isDirty.value = true
    ctx.currentNote.value.is_encrypted = false
    ctx.currentNote.value.content = '<p>过期</p>'
    const ensureFresh = vi.fn(() => '<p>已刷新</p>')
    const { saveBeforeSwitch } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '', ensureFresh)

    const ok = await saveBeforeSwitch(ctx.currentNote.value)
    expect(ok).toBe(true)
    expect(ensureFresh).toHaveBeenCalled()
    expect(updateNote).toHaveBeenCalledWith(1, expect.objectContaining({ content: '<p>已刷新</p>' }))
  })

  it('saveBeforeSwitch：prevNote 是其他对象 → 不 flush 不覆盖（防误写）', async () => {
    const ctx = makeCtx()
    ctx.isDirty.value = true
    ctx.currentNote.value.is_encrypted = false
    const ensureFresh = vi.fn(() => '<p>已刷新</p>')
    const { saveBeforeSwitch } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '', ensureFresh)

    const prev = { id: 5, title: '旧', content: '<p>旧内容</p>', tags: '', is_encrypted: false }
    await saveBeforeSwitch(prev)
    expect(ensureFresh).not.toHaveBeenCalled()
    expect(updateNote).toHaveBeenCalledWith(5, expect.objectContaining({ content: '<p>旧内容</p>' }))
  })
})

// v21.106 P1-1：保存失败分类反馈（审查报告第二批：409 冲突出路 / 404 解除阻塞 / 网络错误去重提示）
describe('useAutoSave 保存失败分类反馈（v21.106 P1-1）', () => {
  it('409 → saveConflict 置位，不弹笼统「保存失败」（对话框由 NotesView 承担）', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    updateNote.mockRejectedValueOnce({ response: { status: 409 } })
    const { saveNote, saveConflict } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '')

    await saveNote(false)
    expect(saveConflict.value).toBe(true)
    expect(message.error).not.toHaveBeenCalled()
  })

  it('409 置位后 markDirty 暂停自动重试（防必然失败的连环 409），草稿照常写入', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    updateNote.mockRejectedValueOnce({ response: { status: 409 } })
    const { saveNote, markDirty } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '')

    await saveNote(false)
    updateNote.mockClear()
    markDirty()
    await vi.advanceTimersByTimeAsync(800)
    expect(updateNote).not.toHaveBeenCalled()   // 自动保存已暂停
    expect(idbSet).toHaveBeenCalled()           // 草稿保留最新编辑
  })

  it('成功保存复位 saveConflict/noticeShown（下一轮冲突可再弹）', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    updateNote.mockRejectedValueOnce({ response: { status: 409 } })
    const { saveNote, saveConflict } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '')
    await saveNote(false)
    expect(saveConflict.value).toBe(true)
    updateNote.mockResolvedValueOnce({ data: { version: 7 } })
    await saveNote(false)
    expect(saveConflict.value).toBe(false)
    expect(ctx.currentNote.value.version).toBe(7)
  })

  it('网络类失败只提示一次（去重），文案含本地草稿兜底说明', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    const { saveNote } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '')
    updateNote.mockRejectedValue({})
    await saveNote(false)
    await saveNote(false)
    await saveNote(false)
    expect(message.error).toHaveBeenCalledTimes(1)
    expect(message.error).toHaveBeenCalledWith(expect.stringContaining('本地草稿'))
  })

  it('saveBeforeSwitch 遇 404 → 返回 true 解除切换阻塞 + isDirty 清位 + 明确「回收站」文案', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    ctx.isDirty.value = true
    updateNote.mockRejectedValueOnce({ response: { status: 404 } })
    const { saveBeforeSwitch, remoteDeleted } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '')

    const ok = await saveBeforeSwitch(ctx.currentNote.value)
    expect(ok).toBe(true)
    expect(remoteDeleted.value).toBe(true)
    expect(ctx.isDirty.value).toBe(false)
    expect(message.error).toHaveBeenCalledWith(expect.stringContaining('回收站'))
  })

  it('saveBeforeSwitch 遇 409：用户确认放弃 → true 且清 dirty；选择留下 → false 保留现场', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value.is_encrypted = false
    ctx.isDirty.value = true
    updateNote.mockRejectedValue({ response: { status: 409 } })
    globalThis.ElMessageBox = { confirm: vi.fn().mockResolvedValue() }
    const { saveBeforeSwitch } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => '')

    expect(await saveBeforeSwitch(ctx.currentNote.value)).toBe(true)
    expect(ctx.isDirty.value).toBe(false)

    ctx.isDirty.value = true
    globalThis.ElMessageBox.confirm = vi.fn().mockRejectedValue(new Error('cancel'))
    expect(await saveBeforeSwitch(ctx.currentNote.value)).toBe(false)
    expect(ctx.isDirty.value).toBe(true) // 留在现场，未保存内容仍在
  })

  it('加密笔记放弃确认文案明示「永久丢失」风险', async () => {
    const ctx = makeCtx() // makeCtx 默认 is_encrypted: true
    ctx.isDirty.value = true
    updateNote.mockRejectedValueOnce({ response: { status: 409 } })
    globalThis.ElMessageBox = { confirm: vi.fn().mockResolvedValue() }
    const { saveBeforeSwitch } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, () => {}, () => 'pwd')

    await saveBeforeSwitch(ctx.currentNote.value)
    const msg = globalThis.ElMessageBox.confirm.mock.calls[0][0]
    expect(msg).toContain('永久丢失')
  })
})

// v21.150 E7：列表排序竞态——保存响应归来时用户已切换笔记，
// 补丁/复位不得打到新点击的（未编辑）笔记上。
describe('useAutoSave E7 排序竞态（v21.150）', () => {
  it('响应窗口内切换笔记 → 列表补丁仍指向被保存笔记；新笔记编辑态/冲突态不被污染', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value = { id: 1, title: 'A', content: '<p>A</p>', tags: '', is_encrypted: false, version: 1 }
    ctx.isDirty.value = true

    let resolveUpdate
    updateNote.mockImplementationOnce(() => new Promise((res) => { resolveUpdate = res }))
    let resolveDel
    idbDel.mockImplementationOnce(() => new Promise((res) => { resolveDel = res }))

    const applied = []
    const { saveNote, saveConflict } = useAutoSave(ctx.currentNote, ctx.currentTagInput, ctx.isDirty, (...a) => applied.push(a), () => '')

    const p = saveNote(false)
    await Promise.resolve()
    resolveUpdate({ data: { message: '更新成功', version: 2, updated_at: '2026-09-24T05:00:00Z' } })
    // 推进到 clearDraft 让渡窗口（isDirty 已置 false —— 正是可切换的窗口）
    for (let i = 0; i < 10 && idbDel.mock.calls.length === 0; i++) await Promise.resolve()
    expect(idbDel).toHaveBeenCalled()

    // 窗口内用户点击列表中下一条：currentNote 切换为 B；并模拟 B 的编辑态与待处理冲突态
    ctx.currentNote.value = { id: 2, title: 'B', content: '<p>B</p>', tags: '', is_encrypted: false, version: 7 }
    ctx.isDirty.value = true
    saveConflict.value = true

    resolveDel()
    await p

    // ① 列表补丁必须指向被保存的 A，且排序键 = 服务端 updated_at（单一时间源）
    expect(applied.length).toBe(1)
    expect(applied[0][0]).toBe(1)
    expect(applied[0][4]).toBe(2)
    expect(applied[0][5]).toBe('2026-09-24T05:00:00Z')
    // ② B 的编辑态与冲突态不得被 A 的迟到响应复位
    expect(ctx.isDirty.value).toBe(true)
    expect(saveConflict.value).toBe(true)
    // ③ B 的 version 不被顺手覆盖
    expect(ctx.currentNote.value.version).toBe(7)
  })
})
