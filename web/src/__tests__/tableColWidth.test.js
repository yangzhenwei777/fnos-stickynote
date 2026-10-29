// tableColWidth（v2.2.2 R3）：存量 '>' 伪影清理 + 单元格列宽三通道解析 + 编辑往返幂等
//
// 这一步钉的是 R2 的 P0 缺陷：曾用正则改写 HTML 补写列宽属性，未吞掉标签收尾 `>`
// → 每列残留一个 '>' 文本，进入 colgroup 后被提升为表格前的 `<p>&gt;&gt;&gt;</p>`，
// 且每次「点编辑→离开」都会 flushContent 回写并累积（用户 R2 fpk 实测）。
// 现改为节点解析层三通道回读（tableExt.js），内容零改写；本文件同时钉住
// 存量伪影的清理与「连续编辑往返绝不改变内容」。
import { describe, it, expect } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import TiptapEditor from '@/components/TiptapEditor.vue'
import { stripLegacyColWidthArtifacts } from '@/components/editor/tableColWidth'
import { parseCellColWidth, readColWidthFromCol } from '@/components/editor/tableExt'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
async function waitEditor (w) {
  await flushPromises()
  let tries = 0
  while (!w.vm.editor && tries++ < 200) {
    await sleep(10)
    await flushPromises()
  }
  return w.vm.editor
}

// 存量形态：只有 col 的 style 宽度，单元格无 colwidth（R2 之前入库）
const TBL_LEGACY = '<table style="min-width: 205px;"><colgroup><col style="width: 300px;"><col style="width: 240px;"><col style="width: 260px;"></colgroup><tbody><tr><th><p>A</p></th><th><p>B</p></th><th><p>C</p></th></tr><tr><td><p>a</p></td><td><p>b</p></td><td><p>c</p></td></tr></tbody></table>'

describe('stripLegacyColWidthArtifacts 存量 > 伪影清理', () => {
  it('连续多行「表格前纯 > 段落」全部剥除，正文与表格保留', () => {
    const dirty = `<p>正文</p><p>&gt;&gt;&gt;</p><p>&gt;&gt;</p>${TBL_LEGACY}<p>尾</p>`
    const out = stripLegacyColWidthArtifacts(dirty)
    expect(out).toContain('<p>正文</p>')
    expect(out).toContain('<p>尾</p>')
    expect(out).toContain('<table')
    expect(out).not.toContain('&gt;')
  })

  it('真实正文里的 > 文本（非紧邻表格）保留', () => {
    const keep = '<p>&gt; 引用式文本</p><p>段落</p>'
    expect(stripLegacyColWidthArtifacts(keep)).toBe(keep)
    // 表格前但中间隔了其他段落 → 不剥（不属于该缺陷形态）
    const far = `<p>&gt;</p><p>中间段</p>${TBL_LEGACY}`
    expect(stripLegacyColWidthArtifacts(far)).toBe(far)
  })

  it('无 table / 空值 / 非字符串安全直通', () => {
    expect(stripLegacyColWidthArtifacts('<p>&gt;&gt;</p>')).toBe('<p>&gt;&gt;</p>')
    expect(stripLegacyColWidthArtifacts('')).toBe('')
    expect(stripLegacyColWidthArtifacts(null)).toBe(null)
  })

  it('幂等：清理后的内容再清理无变化', () => {
    const out = stripLegacyColWidthArtifacts(`<p>&gt;</p>${TBL_LEGACY}`)
    expect(stripLegacyColWidthArtifacts(out)).toBe(out)
  })
})

describe('parseCellColWidth 列宽三通道', () => {
  const doc = new DOMParser().parseFromString(
    '<table><colgroup><col width="180"><col style="width: 240px"><col style="min-width: 25px"></colgroup>' +
      '<tbody><tr><th colwidth="180">A</th><th>B</th><th>C</th></tr><tr><td colspan="2">D</td><td>E</td></tr></tbody></table>',
    'text/html'
  )

  it('通道1：单元格 colwidth 属性优先', () => {
    const cells = doc.querySelectorAll('th')
    expect(parseCellColWidth(cells[0])).toEqual([180])
  })

  it('通道2：col 的 width 属性', () => {
    const cols = doc.querySelectorAll('col')
    expect(readColWidthFromCol(cols[0])).toBe(180)
  })

  it('通道3：col 的 style width（存量入库形态）', () => {
    const cols = doc.querySelectorAll('col')
    expect(readColWidthFromCol(cols[1])).toBe(240)
    // min-width 不是显式宽度
    expect(readColWidthFromCol(cols[2])).toBeNull()
  })

  it('跨合单元格 colwidth="180,120" 解析为数组', () => {
    const d2 = new DOMParser().parseFromString(
      '<table><tbody><tr><td colwidth="180,120">x</td></tr></tbody></table>', 'text/html')
    expect(parseCellColWidth(d2.querySelector('td'))).toEqual([180, 120])
  })
})

describe('TiptapEditor 列宽往返（R2 缺陷回归钉）', () => {
  it('存量只带 col style 的内容：载入即恢复列宽、无杂字符、图片宽度保留', async () => {
    const w = mount(TiptapEditor, { props: { modelValue: `<p>x</p><img src="/uploads/x.png" alt="p" width="240">${TBL_LEGACY}` } })
    await waitEditor(w)
    const html = w.vm.flushContent()

    expect(html).not.toContain('&gt;')
    expect(html).not.toMatch(/<p>>+<\/p>/)
    expect(html).toContain('colwidth="300"')
    expect(html).toContain('width="240"')
    w.unmount()
  })

  it('连续 5 次编辑往返：内容长度与文本幂等（绝不累积 > 行）', async () => {
    let cur = `<p>x</p>${TBL_LEGACY}`
    const rounds = []
    for (let i = 0; i < 5; i++) {
      const w = mount(TiptapEditor, { props: { modelValue: cur } })
      await waitEditor(w)
      cur = w.vm.flushContent()
      w.unmount()
      rounds.push(cur)
    }
    expect(rounds[1]).toBe(rounds[0])
    expect(rounds[4]).toBe(rounds[0])
    expect(cur).not.toContain('&gt;')
  })

  // v2.2.2 R4（方案 A）：编辑器渲染层把 px 列宽投影为百分比 + 固定布局 + 100% 宽，
  // 与预览/长图/md 同一比例（消除「未拖列吃剩余空间」的容器宽度依赖）
  it('编辑器渲染：col 为百分比、表格 fixed + 100% 宽', async () => {
    const w = mount(TiptapEditor, { props: { modelValue: `<p>x</p>${TBL_LEGACY}` } })
    await waitEditor(w)
    const table = w.element.querySelector('.ProseMirror table')
    expect(table.style.tableLayout).toBe('fixed')
    expect(table.style.width).toBe('100%')
    const cols = Array.from(table.querySelectorAll('colgroup > col')).map((c) => c.style.width)
    expect(cols).toEqual(['37.5%', '30%', '32.5%'])
    w.unmount()
  })

  it('序列化（getHTML）：colgroup 输出百分比，单元格保留 colwidth px', async () => {
    const w = mount(TiptapEditor, { props: { modelValue: TBL_LEGACY } })
    await waitEditor(w)
    const html = w.vm.flushContent()
    expect(html).toContain('width: 37.5%')
    expect(html).toContain('width: 32.5%')
    expect(html).toContain('colwidth="300"')
    expect(html).not.toContain('width: 300px')
    w.unmount()
  })
})
