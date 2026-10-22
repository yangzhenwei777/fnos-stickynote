// LocalTransport（桌面 /api→Wails Binding 路由）契约测试：P0-A 收官切片的回归门禁。
// mock window.go.main.LocalService，逐项钉住：参数归一规则、handler 同构响应形状、
// Go 哨兵错误→HTTP 状态映射、未接线端点 501。真实链路（exe 启动人工走查）另测。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { localAdapter } from '@/api/local/transport'

const F = () => window.go.main.LocalService

beforeEach(() => {
  window.go = { main: { LocalService: {
    LocalLogin: vi.fn().mockResolvedValue({ access_token: 't', refresh_token: 't', user: { id: 1, username: 'local', role: 'user' } }),
    SetupStatus: vi.fn().mockResolvedValue({ needs_setup: false }),
    GetPublicConfig: vi.fn().mockResolvedValue({ site_title: 'Sticky note' }),
    GetQuota: vi.fn().mockResolvedValue({ notes_count: 1, notes_limit: 1000, storage_bytes: 3, storage_limit_mb: 1024 }),
    ListNotes: vi.fn().mockResolvedValue({ notes: [{ id: 5, version: 6 }], total: 1 }),
    ListTags: vi.fn().mockResolvedValue(['A', 'B']),
    GetNote: vi.fn().mockResolvedValue({ id: 5, title: 'x' }),
    CreateNote: vi.fn().mockResolvedValue({ id: 6, version: 1 }),
    UpdateNote: vi.fn().mockResolvedValue({ id: 5, version: 7 }),
    UpdateEncrypted: vi.fn().mockResolvedValue({ id: 5, version: 8 }),
    DeleteNote: vi.fn().mockResolvedValue(undefined),
    MoveNote: vi.fn().mockResolvedValue(undefined),
    CopyNote: vi.fn().mockResolvedValue({ id: 7, version: 1 }),
    BatchDelete: vi.fn().mockResolvedValue({ message: '已删除 2 篇笔记', deleted: 2, total: 2 }),
    EncryptNote: vi.fn().mockResolvedValue(undefined),
    DecryptNote: vi.fn().mockResolvedValue('<p>明文</p>'),
    VerifyEncrypt: vi.fn().mockResolvedValue({ id: 5, content: '<p>明文</p>', is_encrypted: true }),
    ListTrash: vi.fn().mockResolvedValue({ notes: [], total: 0 }),
    TrashStats: vi.fn().mockResolvedValue({ count: 0, upload_bytes: 0 }),
    RestoreNote: vi.fn().mockResolvedValue({ message: '已恢复', folder: '我的笔记' }),
    PermanentDeleteNote: vi.fn().mockResolvedValue({ message: '已彻底删除' }),
    EmptyTrash: vi.fn().mockResolvedValue({ message: '回收站已清空', deleted: 3 }),
    ListFolders: vi.fn().mockResolvedValue([{ id: null, name: '我的笔记' }]),
    CreateFolder: vi.fn().mockResolvedValue({ id: 2, name: '夹' }),
    RenameFolder: vi.fn().mockResolvedValue(undefined),
    MoveFolder: vi.fn().mockResolvedValue(undefined),
    DeleteFolder: vi.fn().mockResolvedValue(undefined),
    UploadBytes: vi.fn().mockResolvedValue({ filename: 'u.png', url: '/uploads/u.png' }),
    ImportNote: vi.fn().mockResolvedValue({ id: 9, version: 1, title: '导入标题' })
  } } }
})

const cfg = (o) => ({ method: 'get', url: '/api/notes', params: {}, data: undefined, ...o })
const call = (o) => localAdapter(cfg(o))

describe('LocalTransport 路由与形状', () => {
  it('GET /notes 参数归一（page/page_size/q/tag/sort_by/folder_id=default→0）', async () => {
    const r = await call({ url: '/api/notes', params: { q: 'q1', tag: 'tA', folder_id: 'default' } })
    expect(F().ListNotes).toHaveBeenCalledWith(1, 20, 'q1', 'tA', 'updated_at', 0)
    expect(r.status).toBe(200)
    expect(r.data.total).toBe(1)
  })

  it('PUT /notes/:id 非加密 → handler 同形 {message,version}', async () => {
    const r = await call({ method: 'put', url: '/api/notes/5', data: JSON.stringify({ title: 't', content: '', tags: '', version: 1 }) })
    expect(F().UpdateNote).toHaveBeenCalledWith(5, 't', '', '', 1)
    expect(r.data).toEqual({ message: '更新成功', version: 7 })
  })

  it('PUT /notes/:id 带 password → 走 UpdateEncrypted 并透传 note', async () => {
    const r = await call({ method: 'put', url: '/api/notes/5', data: JSON.stringify({ title: 't', content: '<p>新</p>', tags: '', password: 'pw', version: 7 }) })
    expect(F().UpdateEncrypted).toHaveBeenCalledWith(5, 't', '', '<p>新</p>', 'pw', 7)
    expect(r.data.version).toBe(8)
  })

  it('POST /upload FormData → UploadBytes(name, bytes) 且响应 {url}', async () => {
    const fd = new FormData()
    fd.set('file', new File([new Uint8Array([1, 2, 3])], 'a.png'))
    const r = await call({ method: 'post', url: '/api/upload', data: fd })
    expect(F().UploadBytes).toHaveBeenCalledWith('a.png', [1, 2, 3])
    expect(r.data.url).toBe('/uploads/u.png')
  })

  it('GET /notes/tags 包 {tags}；GET /folders 包 {folders}；create 201', async () => {
    expect((await call({ url: '/api/notes/tags' })).data).toEqual({ tags: ['A', 'B'] })
    expect((await call({ url: '/api/folders' })).data).toEqual({ folders: [{ id: null, name: '我的笔记' }] })
    const c = await call({ method: 'post', url: '/api/folders', data: JSON.stringify({ name: '夹' }) })
    expect(c.status).toBe(201)
  })

  it('folders PUT {name,position} 双动作合成 {message:更新成功}', async () => {
    const r = await call({ method: 'put', url: '/api/folders/3', data: JSON.stringify({ name: 'n', position: 'top' }) })
    expect(F().RenameFolder).toHaveBeenCalledWith(3, 'n')
    expect(F().MoveFolder).toHaveBeenCalledWith(3, 'top')
    expect(r.data).toEqual({ message: '更新成功' })
  })

  it('delete/encrypt/decrypt/verify/trash 形状钉住', async () => {
    expect((await call({ method: 'delete', url: '/api/notes/5' })).data).toEqual({ message: '删除成功' })
    expect((await call({ method: 'put', url: '/api/notes/5/encrypt', data: JSON.stringify({ password: 'p', confirm_password: 'p' }) })).data).toEqual({ message: '加密成功', is_encrypted: true })
    const dec = await call({ method: 'delete', url: '/api/notes/5/encrypt', data: JSON.stringify({ password: 'p' }) })
    expect(dec.data).toEqual({ message: '解密成功', is_encrypted: false, content: '<p>明文</p>' })
    expect((await call({ method: 'post', url: '/api/notes/5/verify-encrypt', data: JSON.stringify({ password: 'p' }) })).data.is_encrypted).toBe(true)
    expect((await call({ url: '/api/trash/stats' })).data).toEqual({ count: 0, upload_bytes: 0 })
    expect((await call({ method: 'delete', url: '/api/trash/notes' })).data.deleted).toBe(3)
  })

  it('POST /auth/login → LocalLogin 透传（setTokens 可用）', async () => {
    const r = await call({ method: 'post', url: '/api/auth/login', data: JSON.stringify({ username: 'local', password: 'x', remember: true }) })
    expect(r.data.access_token).toBeTruthy()
    expect(r.data.user.id).toBe(1)
  })

  it('Go 哨兵错误 → 同构 HTTP 状态（409/403/409+code）', async () => {
    F().UpdateNote.mockRejectedValueOnce(new Error('version conflict'))
    await expect(call({ method: 'put', url: '/api/notes/5', data: '{}' })).rejects.toMatchObject({ response: { status: 409 } })
    F().VerifyEncrypt.mockRejectedValueOnce(new Error('wrong password'))
    await expect(call({ url: '/api/notes/5/verify-encrypt', method: 'post', data: '{}' })).rejects.toMatchObject({ response: { status: 403 } })
    F().DeleteFolder.mockRejectedValueOnce(new Error('folder not empty'))
    await expect(call({ method: 'delete', url: '/api/folders/3' })).rejects.toMatchObject({
      response: { status: 409, data: { code: 'FOLDER_NOT_EMPTY' } }
    })
  })

  it('未接线端点 → **reject** 501（绝不返回"成功形状的非 2xx"）', async () => {
    // 反向断言锁定历史缺陷：早期实现 `return {status:501}` 被 axios 自定义 adapter
    // 当成功交给调用方（自定义 adapter 不自动 settle），前端把错误对象当笔记插入列表
    // 并弹「导入成功」→ 用户看到空白笔记且随后消失。必须 reject。
    await expect(call({ url: '/api/backup/status' })).rejects.toMatchObject({
      response: { status: 501 }
    })
    await expect(call({ url: '/api/backup/status' })).rejects.toThrow(/尚未支持/)
  })

  it('POST /notes/import FormData → ImportNote(name, bytes, folderId)，201 透传 model.Note', async () => {
    const fd = new FormData()
    fd.set('file', new File([new Uint8Array([35, 32, 77, 68])], 'note.md'))
    const r = await call({ method: 'post', url: '/api/notes/import', data: fd })
    // v21.213：无 folder_id 字段 ⇒ 第三参为 null（=「我的笔记」，保持旧行为）
    expect(F().ImportNote).toHaveBeenCalledWith('note.md', [35, 32, 77, 68], null)
    expect(r.status).toBe(201)
    expect(r.data.id).toBe(9)
  })

  // ⭐ v21.213（211b 实机：导入的笔记一律落到「我的笔记」而非当前文件夹）
  it('POST /notes/import 带 folder_id → 透传给 ImportNote（导入落在当前文件夹）', async () => {
    const fd = new FormData()
    fd.set('file', new File([new Uint8Array([35, 32, 77, 68])], 'note.md'))
    fd.set('folder_id', '7')
    await call({ method: 'post', url: '/api/notes/import', data: fd })
    expect(F().ImportNote).toHaveBeenCalledWith('note.md', [35, 32, 77, 68], 7)
  })

  it('POST /notes/import folder_id 非法（如 abc）→ 归一为 null（不产生脏folder）', async () => {
    const fd = new FormData()
    fd.set('file', new File([new Uint8Array([35, 32, 77, 68])], 'note.md'))
    fd.set('folder_id', 'abc')
    await call({ method: 'post', url: '/api/notes/import', data: fd })
    expect(F().ImportNote).toHaveBeenCalledWith('note.md', [35, 32, 77, 68], null)
  })

  it('POST /notes/import folder_id 为空串 → 归一为 null', async () => {
    const fd = new FormData()
    fd.set('file', new File([new Uint8Array([35, 32, 77, 68])], 'note.md'))
    fd.set('folder_id', '')
    await call({ method: 'post', url: '/api/notes/import', data: fd })
    expect(F().ImportNote).toHaveBeenCalledWith('note.md', [35, 32, 77, 68], null)
  })

  it('POST /notes/import 缺文件 → reject 400（不产生幽灵笔记）', async () => {
    await expect(call({ method: 'post', url: '/api/notes/import', data: new FormData() }))
      .rejects.toMatchObject({ response: { status: 400 } })
  })

  it('GET /notes/:id 与 folder_id=null（缺省=全部）', async () => {
    expect((await call({ url: '/api/notes/5' })).data.id).toBe(5)
    await call({ url: '/api/notes', params: {} })
    expect(F().ListNotes).toHaveBeenLastCalledWith(1, 20, '', '', 'updated_at', null)
  })
})
