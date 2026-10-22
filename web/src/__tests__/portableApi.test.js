// 便携备份桌面绑定封装（desktopApi）契约测试 —— 批次 6 刀 3 回归门禁。
// 钉住：桥函数透传口令、无桥即明确报错（禁静默）、Go 哨兵错误→中文文案映射、
// Web 构建 IS_DESKTOP=false 时入口判定不暴露（ProfileView 依此隐藏卡片）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import * as desktopApi from '@/api/local/desktopApi'

const F = () => window.go.main.LocalService

beforeEach(() => {
  window.go = { main: { LocalService: {
    PortableExport: vi.fn().mockResolvedValue({ path: 'C:/x/a.snbak', backup_id: 'b1', out_bytes: 2048 }),
    PortableImport: vi.fn().mockResolvedValue({ generation: 'g-000002', rebind_required: false })
  } } }
})
afterEach(() => { delete window.go })

describe('desktopApi 便携备份', () => {
  it('PortableExport/Import 透传口令并回传 Go 摘要', async () => {
    const exp = await desktopApi.portableExport('pw123456')
    expect(F().PortableExport).toHaveBeenCalledWith('pw123456')
    expect(exp.backup_id).toBe('b1')
    const imp = await desktopApi.portableImport('pw123456')
    expect(F().PortableImport).toHaveBeenCalledWith('pw123456')
    expect(imp.generation).toBe('g-000002')
  })

  it('桥缺失 = 显式 DESKTOP_BRIDGE_UNAVAILABLE，绝不静默成功', async () => {
    delete window.go
    await expect(desktopApi.portableExport('x')).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
    await expect(desktopApi.portableImport('x')).rejects.toThrow('DESKTOP_BRIDGE_UNAVAILABLE')
  })

  it('Web 构建（VITE_DESKTOP 未置 1）入口判定为不可用', () => {
    expect(desktopApi.desktopBridgeAvailable()).toBe(false)
  })

  it('Go 哨兵错误映射为人话（口令/损坏/缺附件/超帽/非便携/篡改）', () => {
    const cases = [
      ['portable: password too short (min 6)', '备份口令至少 6 位'],
      ['portable: open: BACKUP_DECRYPT_FAILED: cipher: message authentication failed', '口令错误或备份文件已损坏'],
      ['portable: referenced attachments missing on disk: a.png', '本地附件缺失，已中止导出（不会产出不完整备份）'],
      ['portable: seal: BACKUP_TOO_LARGE: archive 300 bytes > cap 100', '数据超过 256MB 便携备份上限，请精简附件后重试'],
      ['portable: archive is not a PORTABLE backup', '所选文件不是 Sticky note 便携备份（.snbak）'],
      ['portable: archive contents do not match manifest: uploads/x missing', '备份包内容校验失败（可能被篡改或不完整），已拒绝导入']
    ]
    for (const [go, zh] of cases) {
      expect(desktopApi.formatPortableError(new Error(go))).toBe(zh)
    }
    expect(desktopApi.formatPortableError('custom-text')).toBe('custom-text')
  })
})
