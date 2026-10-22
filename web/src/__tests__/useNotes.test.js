/**
 * useNotes 笔记列表 CRUD 测试（v21.58 B-1）
 * mock：api/note、api/request、utils/message
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ref } from 'vue'

vi.mock('@/api/note', () => ({
  getNotes: vi.fn(),
  createNote: vi.fn(),
  deleteNote: vi.fn(),
  getNote: vi.fn()
}))
vi.mock('@/api/request', () => ({
  default: { get: vi.fn().mockResolvedValue({ data: { tags: [] } }) }
}))
vi.mock('@/utils/message', () => ({
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), errorOnce: vi.fn() }
}))

import { getNotes, createNote, deleteNote, getNote } from '@/api/note'
import { useNotes } from '@/composables/useNotes'

function makeCtx() {
  const currentNote = ref(null)
  const currentTagInput = ref([])
  return { currentNote, currentTagInput }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('useNotes 列表 CRUD', () => {
  it('loadNotes 成功 → notes 填充 + recentNotes 取最新 5 篇', async () => {
    const ctx = makeCtx()
    getNotes.mockResolvedValue({ data: { notes: [
      { id: 1, title: 'a', updated_at: '2026-01-01T00:00:00Z' },
      { id: 2, title: 'b', updated_at: '2026-01-03T00:00:00Z' },
      { id: 3, title: 'c', updated_at: '2026-01-02T00:00:00Z' },
      { id: 4, title: 'd', updated_at: '2026-01-05T00:00:00Z' },
      { id: 5, title: 'e', updated_at: '2026-01-04T00:00:00Z' },
      { id: 6, title: 'f', updated_at: '2026-01-06T00:00:00Z' }
    ] } })
    const { loadNotes, notes, recentNotes } = useNotes(ctx.currentNote, ctx.currentTagInput)

    await loadNotes()
    expect(notes.value).toHaveLength(6)
    expect(recentNotes.value).toHaveLength(5)
    expect(recentNotes.value[0].id).toBe(6) // 最新（updated_at 最大）排最前
  })

  it('createNewNote 成功 → 新笔记插入列表头部并设为当前', async () => {
    const ctx = makeCtx()
    createNote.mockResolvedValue({ data: { id: 10, title: '新笔记', content: '<p></p>', tags: '' } })
    const { createNewNote, notes } = useNotes(ctx.currentNote, ctx.currentTagInput)

    await createNewNote()
    expect(notes.value[0].id).toBe(10)
    expect(ctx.currentNote.value.id).toBe(10)
  })

  it('applySavedToLists → 更新对应笔记标题/内容并重排序', () => {
    const ctx = makeCtx()
    const { notes, applySavedToLists } = useNotes(ctx.currentNote, ctx.currentTagInput)
    notes.value = [
      { id: 1, title: 'a', content: '', updated_at: '2026-01-01T00:00:00Z' },
      { id: 2, title: 'b', content: '', updated_at: '2026-01-02T00:00:00Z' }
    ]

    applySavedToLists(1, '新标题', '<p>新内容</p>', 'tagA')
    const updated = notes.value.find(n => n.id === 1)
    expect(updated.title).toBe('新标题')
    expect(updated.content).toBe('<p>新内容</p>')
  })

  it('applySavedToLists → 携带服务端 updated_at 时按其重排序（v21.150 E7 单一时间源）', () => {
    const ctx = makeCtx()
    const { notes, applySavedToLists } = useNotes(ctx.currentNote, ctx.currentTagInput)
    notes.value = [
      { id: 1, title: 'a', content: '', updated_at: '2026-01-01T00:00:00Z' },
      { id: 2, title: 'b', content: '', updated_at: '2026-01-02T00:00:00Z' }
    ]

    applySavedToLists(1, 'a', '', '', undefined, '2026-01-03T00:00:00Z')
    expect(notes.value[0].id).toBe(1)
    expect(notes.value[0].updated_at).toBe('2026-01-03T00:00:00Z')
  })

  it('applySavedToLists → 不传 updated_at 时不改时间戳（v21.150 E7：不再写客户端时钟）', () => {
    const ctx = makeCtx()
    const { notes, applySavedToLists } = useNotes(ctx.currentNote, ctx.currentTagInput)
    notes.value = [
      { id: 1, title: 'a', content: '', updated_at: '2026-01-01T00:00:00Z' }
    ]

    applySavedToLists(1, '新标题', '<p>x</p>', 'tagA')
    expect(notes.value[0].updated_at).toBe('2026-01-01T00:00:00Z')
  })

  it('syncNoteInList → 仅 patch 字段（v21.150 E7：不移动位置、不盖客户端时间戳）', () => {
    const ctx = makeCtx()
    const { notes, syncNoteInList } = useNotes(ctx.currentNote, ctx.currentTagInput)
    notes.value = [
      { id: 1, title: 'a', content: '', updated_at: '2026-01-01T00:00:00Z' },
      { id: 2, title: 'b', content: '', updated_at: '2026-01-02T00:00:00Z' }
    ]

    syncNoteInList(2, { is_encrypted: true, content: '' })
    expect(notes.value[0].id).toBe(1) // 顺序不变（加密/解密不改服务端 updated_at）
    expect(notes.value[1].is_encrypted).toBe(true)
    expect(notes.value[1].updated_at).toBe('2026-01-02T00:00:00Z')
  })

  it('removeNote 成功 → 清空 currentNote 并返回 true', async () => {
    const ctx = makeCtx()
    ctx.currentNote.value = { id: 1, title: 'a' }
    deleteNote.mockResolvedValue({})
    const { removeNote } = useNotes(ctx.currentNote, ctx.currentTagInput)

    const ok = await removeNote(1)
    expect(ok).toBe(true)
    expect(ctx.currentNote.value).toBeNull()
  })
})

describe('useNotes 乐观锁 version 同步（v21.84 修复 409）', () => {
  it('applySavedToLists 携带 version → 列表项 version 被同步', () => {
    const ctx = makeCtx()
    const { notes, applySavedToLists } = useNotes(ctx.currentNote, ctx.currentTagInput)
    notes.value = [
      { id: 1, title: 'a', content: '', version: 1, updated_at: '2026-01-01T00:00:00Z' }
    ]

    applySavedToLists(1, '新标题', '<p>新内容</p>', 'tagA', 2)
    expect(notes.value[0].version).toBe(2)
  })

  it('applySavedToLists 不传 version → 不改列表项 version（兼容旧调用）', () => {
    const ctx = makeCtx()
    const { notes, applySavedToLists } = useNotes(ctx.currentNote, ctx.currentTagInput)
    notes.value = [
      { id: 1, title: 'a', content: '', version: 1, updated_at: '2026-01-01T00:00:00Z' }
    ]

    applySavedToLists(1, '新标题', '<p>新内容</p>', 'tagA')
    expect(notes.value[0].version).toBe(1)
  })

  it('selectNote 重新拉取 → currentNote 拿到最新数据与 version', async () => {
    const ctx = makeCtx()
    const { selectNote } = useNotes(ctx.currentNote, ctx.currentTagInput)
    getNote.mockResolvedValue({ data: { id: 1, title: '最新', content: '<p>新</p>', tags: 'a,b', version: 3 } })

    await selectNote({ id: 1, title: '旧', content: '<p>旧</p>', tags: '', version: 1 })
    expect(ctx.currentNote.value.title).toBe('最新')
    expect(ctx.currentNote.value.version).toBe(3)
    expect(ctx.currentTagInput.value).toEqual(['a', 'b'])
  })

  it('selectNote 拉取失败 → 回退到传入列表项（不阻断）', async () => {
    const ctx = makeCtx()
    const { selectNote } = useNotes(ctx.currentNote, ctx.currentTagInput)
    getNote.mockRejectedValueOnce(new Error('network'))
    const fallback = { id: 1, title: '旧', content: '<p>旧</p>', tags: '', version: 1 }

    await selectNote(fallback)
    expect(ctx.currentNote.value.title).toBe('旧')
    expect(ctx.currentNote.value.version).toBe(1)
  })
})

// ---- 滚动触底续载（2026-10-15 用户需求：默认 20 条 + 触底加载后 20 条）----
// 受控 promise：竞态用例需要手工决定响应完成顺序。
function deferred() {
  let resolve, reject
  const p = new Promise((res, rej) => { resolve = res; reject = rej })
  return { p, resolve, reject }
}

function ids(from, to) {
  const arr = []
  for (let i = from; i <= to; i++) arr.push({ id: i, title: 'n' + i, updated_at: '2026-01-01T00:00:00Z' })
  return arr
}

describe('useNotes 滚动续载（2026-10-15）', () => {
  it('重置加载显式带 page=1&page_size=20；total=45 时 hasMore=true', async () => {
    const ctx = makeCtx()
    getNotes.mockResolvedValueOnce({ data: { notes: ids(1, 20), total: 45 } })
    const { loadNotes, notes, total, hasMore } = useNotes(ctx.currentNote, ctx.currentTagInput)

    await loadNotes('', '', '3')
    expect(getNotes).toHaveBeenCalledWith(expect.objectContaining({ page: 1, page_size: 20, folder_id: '3' }))
    expect(notes.value).toHaveLength(20)
    expect(total.value).toBe(45)
    expect(hasMore.value).toBe(true)
  })

  it('触底续载追加且携带同一过滤参数；续载页撞已加载 id 自动去重；拉满后 hasMore=false', async () => {
    const ctx = makeCtx()
    // 第二页故意含重复 id=20 与 39（各一条重复）→ 20 去重后净增 19
    getNotes.mockResolvedValueOnce({ data: { notes: ids(1, 20), total: 45 } })
    const { loadNotes, loadMoreNotes, notes, hasMore, total } = useNotes(ctx.currentNote, ctx.currentTagInput)
    await loadNotes('', '', '3')

    getNotes.mockResolvedValueOnce({ data: { notes: [...ids(20, 39), { id: 20, title: 'dup', updated_at: '2026-01-01T00:00:00Z' }], total: 45 } })
    const more1 = await loadMoreNotes()
    expect(more1).toBe(true)
    expect(getNotes).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2, page_size: 20, folder_id: '3' }))
    expect(notes.value).toHaveLength(39) // 20 + (40-20-1 重复)

    getNotes.mockResolvedValueOnce({ data: { notes: ids(40, 45), total: 45 } })
    await loadMoreNotes()
    expect(getNotes).toHaveBeenLastCalledWith(expect.objectContaining({ page: 3, page_size: 20, folder_id: '3' }))
    expect(notes.value).toHaveLength(45)
    expect(hasMore.value).toBe(false)
    expect(total.value).toBe(45)
    // 再触底不发请求
    expect(await loadMoreNotes()).toBe(false)
    expect(getNotes).toHaveBeenCalledTimes(3)
  })

  it('total<20（不足一页）→ 触底不发请求', async () => {
    const ctx = makeCtx()
    getNotes.mockResolvedValueOnce({ data: { notes: ids(1, 15), total: 15 } })
    const { loadNotes, loadMoreNotes, hasMore } = useNotes(ctx.currentNote, ctx.currentTagInput)

    await loadNotes()
    expect(hasMore.value).toBe(false)
    expect(await loadMoreNotes()).toBe(false)
    expect(getNotes).toHaveBeenCalledTimes(1)
  })

  it('首页在途时触底被闸门拦截（防 page=2 先于 page=1 落地）', async () => {
    const ctx = makeCtx()
    const d1 = deferred()
    getNotes.mockImplementationOnce(() => d1.p)
    const { loadNotes, loadMoreNotes } = useNotes(ctx.currentNote, ctx.currentTagInput)

    const p1 = loadNotes()
    await Promise.resolve() // 推进到 fetchPage 的 await 挂起点
    expect(await loadMoreNotes()).toBe(false)
    expect(getNotes).toHaveBeenCalledTimes(1)
    d1.resolve({ data: { notes: ids(1, 20), total: 45 } })
    await p1
    // 首页落地后触底放行
    getNotes.mockResolvedValueOnce({ data: { notes: ids(21, 40), total: 45 } })
    expect(await loadMoreNotes()).toBe(true)
  })

  it('竞态守护：切搜索词后，旧关键词的慢响应整包丢弃', async () => {
    const ctx = makeCtx()
    const dOld = deferred()
    const dNew = deferred()
    getNotes.mockImplementationOnce(() => dOld.p).mockImplementationOnce(() => dNew.p)
    const { loadNotes, notes, total } = useNotes(ctx.currentNote, ctx.currentTagInput)

    const pOld = loadNotes() // 旧：无过滤（全量 45）
    await Promise.resolve()
    const pNew = loadNotes('关键词') // 新：带搜索词
    await Promise.resolve()
    dNew.resolve({ data: { notes: ids(100, 105), total: 6 } })
    await pNew
    dOld.resolve({ data: { notes: ids(1, 20), total: 45 } }) // 旧响应这时才回来
    await pOld

    expect(notes.value.map(n => n.id)).toEqual([100, 101, 102, 103, 104, 105])
    expect(total.value).toBe(6)
    expect(getNotes).toHaveBeenLastCalledWith(expect.objectContaining({ q: '关键词', page: 1 }))
  })

  it('续载失败不丢已加载列表；错误态可见；再次触底可重试', async () => {
    const ctx = makeCtx()
    getNotes.mockResolvedValueOnce({ data: { notes: ids(1, 20), total: 45 } })
    const { loadNotes, loadMoreNotes, notes, listError } = useNotes(ctx.currentNote, ctx.currentTagInput)
    await loadNotes()

    getNotes.mockRejectedValueOnce(new Error('boom'))
    await loadMoreNotes()
    expect(notes.value).toHaveLength(20)
    expect(listError.value).toBe('加载笔记失败')

    getNotes.mockResolvedValueOnce({ data: { notes: ids(21, 40), total: 45 } })
    await loadMoreNotes()
    expect(notes.value).toHaveLength(40)
    expect(listError.value).toBe('')
  })

  it('重置加载会作废在途续载：续载慢响应在切文件夹后回来被丢弃', async () => {
    const ctx = makeCtx()
    getNotes.mockResolvedValueOnce({ data: { notes: ids(1, 20), total: 45 } })
    const { loadNotes, loadMoreNotes, notes, total } = useNotes(ctx.currentNote, ctx.currentTagInput)
    await loadNotes()

    const dMore = deferred()
    getNotes.mockImplementationOnce(() => dMore.p)
    const pMore = loadMoreNotes()
    await Promise.resolve()
    // 用户此刻切了文件夹 → 重置加载（page=1，folder_id=7）
    getNotes.mockResolvedValueOnce({ data: { notes: ids(500, 505), total: 6 } })
    await loadNotes('', '', '7')
    // 在途的 page=2（folder_id=3）响应此时才回来 → 必须整包丢弃
    dMore.resolve({ data: { notes: ids(21, 40), total: 45 } })
    await pMore

    expect(notes.value.map(n => n.id)).toEqual([500, 501, 502, 503, 504, 505])
    expect(total.value).toBe(6)
    expect(notes.value.some(n => n.id === 21)).toBe(false)
  })
})
