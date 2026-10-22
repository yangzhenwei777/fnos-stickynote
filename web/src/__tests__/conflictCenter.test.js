// 冲突中心桌面绑定封装（desktopApi 冲突面）契约测试 —— 批次 8-Alt 刀 2 回归门禁。
// 钉住：三入口参数类型与顺序（wails 绑定形状）、无桥即明确报错（禁静默）、
// kind 文案与 Go 侧 synclocal 常量**逐一对齐**（新增 kind 未登记文案时此测必红）、
// 载荷摘要与"另存副本"门槛（加密件/文件夹件必须拒绝另存，不得造出空笔记）。
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import * as desktopApi from '@/api/local/desktopApi'

beforeEach(() => {
  window.go = { main: { LocalService: {
    SyncStatus: () => Promise.resolve({ pending_ops: 2, conflict_count: 1, conflict_total: 3, cursor: '42' }),
    ConflictList: () => Promise.resolve({ items: [], total: 0 }),
    OutboxList: () => Promise.resolve({ items: [], counts: {}, total: 0 }),
    ConflictResolve: () => Promise.resolve({ conflict_id: 7, resolution: 'keep-local', requeued_ops: 1 }),
    CreateNote: () => Promise.resolve({ id: 99, sync_id: 'new-uuid' })
  } } }
})
afterEach(() => { delete window.go })

describe('冲突中心绑定封装', () => {
  it('conflictList/outboxList/conflictResolve 调用参数逐项正确', async () => {
    const calls = []
    window.go.main.LocalService.ConflictList = (...a) => { calls.push(['ConflictList', a]); return Promise.resolve({ items: [], total: 0 }) }
    window.go.main.LocalService.OutboxList = (...a) => { calls.push(['OutboxList', a]); return Promise.resolve({ items: [], counts: {}, total: 0 }) }
    window.go.main.LocalService.ConflictResolve = (...a) => { calls.push(['ConflictResolve', a]); return Promise.resolve({}) }

    await desktopApi.conflictList(false, 20, 40)
    await desktopApi.outboxList('FAILED,CONFLICT', 10, 0)
    await desktopApi.conflictResolve('12', 'keep-remote', true)

    expect(calls[0]).toEqual(['ConflictList', [false, 20, 40]])
    expect(calls[1]).toEqual(['OutboxList', ['FAILED,CONFLICT', 10, 0]])
    // id 必须转成 number（Go 侧 int64），resolution 为 string，confirmed 为真值
    expect(calls[2]).toEqual(['ConflictResolve', [12, 'keep-remote', true]])
  })

  it('默认参数：只读未处置第一页；缺参不炸', async () => {
    const seen = []
    window.go.main.LocalService.ConflictList = (...a) => { seen.push(a); return Promise.resolve({ items: [], total: 0 }) }
    await desktopApi.conflictList()
    await desktopApi.conflictList(true, 0, -5) // 非法分页值降级为默认，不得把 0/负数丢给 Go
    expect(seen[0]).toEqual([true, 50, 0])
    expect(seen[1]).toEqual([true, 50, 0])
  })

  it('桥缺失 = 显式 DESKTOP_BRIDGE_UNAVAILABLE，绝不静默成功', async () => {
    delete window.go
    await expect(desktopApi.conflictList()).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
    await expect(desktopApi.outboxList()).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
    await expect(desktopApi.conflictResolve(1, 'keep-local')).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
    await expect(desktopApi.syncStatus()).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
  })

  it('Web 构建（VITE_DESKTOP 未置 1）桥判定为不可用', () => {
    expect(desktopApi.desktopBridgeAvailable()).toBe(false)
  })

  it('formatConflictError：桥哨兵与未就绪态转人话，Go 中文哨兵原样透传', () => {
    expect(desktopApi.formatConflictError(new Error('DESKTOP_BRIDGE_UNAVAILABLE'))).toBe('桌面桥不可用')
    expect(desktopApi.formatConflictError(new Error('同步引擎未初始化'))).toBe('同步引擎未就绪，请先进入工作区')
    expect(desktopApi.formatConflictError(new Error('该冲突已处置，无需重复操作（处置方式 keep-local，2026-09-21）')))
      .toContain('该冲突已处置')
    expect(desktopApi.formatConflictError(null)).toBe('操作失败')
  })

  it('KIND_LABELS 与 Go 侧 7 类 kind 逐一对齐（新增 kind 未登记文案必红）', () => {
    const goKinds = ['CONFLICT_ON_PULL', 'PUSH_CONFLICT', 'ENTITY_GONE', 'STALE',
      'RETRY_EXHAUSTED', 'ATTACHMENT_DIVERGED', 'OP_ID_REUSE']
    for (const k of goKinds) {
      expect(desktopApi.KIND_LABELS[k], `kind ${k} 缺中文文案`).toBeTruthy()
    }
    expect(Object.keys(desktopApi.KIND_LABELS).sort()).toEqual([...goKinds].sort())
  })

  it('处置标签四种齐备且键名与 Go 常量一致（1.0.218 增 recreate）', () => {
    expect(Object.keys(desktopApi.RESOLUTION_LABELS).sort())
      .toEqual(['drop-local', 'keep-local', 'keep-remote', 'recreate'])
  })
})

describe('冲突载荷摘要与另存副本', () => {
  it('笔记载荷：剥 HTML 标签、带版本、加密件不显示正文', () => {
    const note = JSON.stringify({ sync_id: 's1', title: '标题', content: '<p>正文 <strong>粗</strong></p>', version: 3 })
    const sum = desktopApi.summarizeConflictPayload(note)
    expect(sum).toContain('标题「标题」')
    expect(sum).toContain('正文 正文 粗')
    expect(sum).not.toContain('<p>')
    expect(sum).toContain('版本 3')
    const enc = JSON.stringify({ sync_id: 's2', title: '密文笔记', is_encrypted: true, version: 1 })
    expect(desktopApi.summarizeConflictPayload(enc)).toContain('加密内容（需口令才可读）')
    const tomb = JSON.stringify({ sync_id: 's3', title: '已删', content: '', deleted_at: '2026-09-21T00:00:00Z' })
    expect(desktopApi.summarizeConflictPayload(tomb)).toContain('处于回收站')
  })

  it('文件夹 / 排序 / 非 JSON / 空 载荷都有可读降级（不显示 undefined、不吞成空白）', () => {
    expect(desktopApi.summarizeConflictPayload(JSON.stringify({ sync_id: 'f1', name: '工作', sort_order: 2, version: 5 })))
      .toContain('文件夹「工作」')
    expect(desktopApi.summarizeConflictPayload(JSON.stringify({ order: ['a', 'b', 'c'], bases: {} })))
      .toContain('共 3 项')
    expect(desktopApi.summarizeConflictPayload('not json at all')).toBe('not json at all')
    expect(desktopApi.summarizeConflictPayload('')).toBe('（无内容）')
    expect(desktopApi.summarizeConflictPayload(undefined)).toBe('（无内容）')
  })

  it('另存副本门槛：普通笔记放行，加密/文件夹/坏载荷各自拒绝并给原因', () => {
    const ok = desktopApi.conflictCopyCandidate(JSON.stringify({ title: 't', content: '<p>c</p>', tags: 'a,b' }))
    expect(ok).toEqual({ ok: true, title: 't', content: '<p>c</p>', tags: 'a,b' })
    for (const [raw, kw] of [
      ['', '本机内容缺失'],
      ['{', '不是笔记载荷'],
      [JSON.stringify({ sync_id: 'f', name: '夹', sort_order: 1 }), '仅笔记支持'],
      [JSON.stringify({ title: 'x', content: 'y', is_encrypted: true }), '加密笔记']
    ]) {
      const r = desktopApi.conflictCopyCandidate(raw)
      expect(r.ok, `应拒绝：${raw}`).toBe(false)
      expect(r.reason).toContain(kw)
    }
  })

  it('conflictSaveCopy 走 ConflictSaveCopy 绑定（单参数 conflictID，number 化）——落点文件夹由 Go 侧解析', async () => {
    const args = []
    window.go.main.LocalService.ConflictSaveCopy = (...a) => {
      args.push(a)
      return Promise.resolve({ note: { id: 5 }, folder_name: '工作', folder_fallback: false })
    }
    const res = await desktopApi.conflictSaveCopy('7')
    expect(args[0]).toEqual([7])
    expect(res.folder_name).toBe('工作')
  })

  it('conflictSaveCopy 桥缺失 = 显式 DESKTOP_BRIDGE_UNAVAILABLE，绝不静默成功', async () => {
    delete window.go
    await expect(desktopApi.conflictSaveCopy(1)).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
  })
})
