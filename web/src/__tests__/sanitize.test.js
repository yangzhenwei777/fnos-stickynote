import { describe, it, expect } from 'vitest'
import { sanitizeNoteHtml, sanitizeHighlight } from '@/utils/sanitize'

// 评审 B2 护栏：笔记 HTML 消毒行为（迁至 @/utils/sanitize 后）不变量回归测试
describe('sanitizeNoteHtml — 安全不变量', () => {
  it('剥除 <script> 与内联事件', () => {
    const out = sanitizeNoteHtml('<p>正文</p><script>alert(1)</script><img src="/uploads/a.png" onerror="boom()">')
    expect(out).not.toContain('<script')
    expect(out).not.toContain('onerror')
    expect(out).toContain('正文')
  })

  it('拦截 javascript: 链接协议', () => {
    const out = sanitizeNoteHtml('<a href="javascript:alert(1)">点我</a>')
    expect(out.toLowerCase()).not.toContain('javascript:')
    expect(out).toContain('点我')
  })

  it('保留同源 /uploads 图片', () => {
    const out = sanitizeNoteHtml('<p><img src="/uploads/a.png" alt="x"></p>')
    expect(out).toContain('/uploads/a.png')
  })

  it('移除站外图片（img 禁外链 hook 生效）', () => {
    const out = sanitizeNoteHtml('<p><img src="https://evil.example.com/track.png"></p>')
    expect(out).not.toContain('evil.example.com')
  })

  it('链接强制 target=_blank rel=noopener（2026-10-15 用户需求：预览点链接新窗口打开）', () => {
    const out = sanitizeNoteHtml('<a href="https://example.com/x">外部链接</a>')
    expect(out).toContain('target="_blank"')
    expect(out).toContain('rel="noopener noreferrer"')
    // 存量已带 target 的链接同样被规范化，不产生重复属性
    const out2 = sanitizeNoteHtml('<a href="/x" target="_self">内部</a>')
    expect(out2.toLowerCase()).toContain('target="_blank"')
    expect(out2.toLowerCase()).not.toContain('_self')
  })

  it('放行任务清单结构（input/label/data-*）', () => {
    const out = sanitizeNoteHtml('<ul data-type="taskList"><li data-type="taskItem" data-checked="true"><label><input type="checkbox" checked><span>项</span></label></li></ul>')
    expect(out).toContain('data-type="taskItem"')
    expect(out).toContain('<input')
    expect(out).toContain('项')
  })

  // v2.2.2 表格列宽保持：编辑器 resizable 产出 <col style="width:Npx">，放行 colgroup/col
  // 并给带真实列宽的 table 打 data-has-colwidth 标记（CSS 据此启用 fixed 布局）
  // v2.2.2 R4：展示层把列宽投影为百分比（171 / 171 = 50%/50%），min-width 清除
  it('保留 colgroup/col 并把列宽投影为百分比（编辑器 resizable 产物）', () => {
    const out = sanitizeNoteHtml('<table><colgroup><col style="width: 171px"><col style="min-width: 25px"></colgroup><tbody><tr><td>A</td><td>B</td></tr></tbody></table>')
    expect(out).toContain('<colgroup>')
    expect(out).toContain('<col style=')
    expect(out).toContain('width: 50%')
    expect(out).not.toContain('width: 171px')
    expect(out).not.toContain('min-width')
  })
  it('存在真实列宽时 table 打 data-has-colwidth 标记；纯 min-width（未拖宽）不打', () => {
    const dragged = sanitizeNoteHtml('<table><colgroup><col style="width: 171px"><col style="min-width: 25px"></colgroup><tbody><tr><td>A</td></tr></tbody></table>')
    expect(dragged).toContain('data-has-colwidth')
    const pristine = sanitizeNoteHtml('<table><colgroup><col style="min-width: 25px"><col style="min-width: 25px"></colgroup><tbody><tr><td>A</td></tr></tbody></table>')
    expect(pristine).not.toContain('data-has-colwidth')
  })
  it('剥 <col> 外的恶意形态（col 不放行事件属性）', () => {
    const out = sanitizeNoteHtml('<table><colgroup><col style="width:10px" onclick="x()"></colgroup><tbody><tr><td>A</td></tr></tbody></table>')
    expect(out).not.toContain('onclick')
    expect(out).toContain('width: 100%')
  })
})

describe('sanitizeHighlight — 列表高亮片段', () => {
  it('仅保留 <mark>，剥其他标签', () => {
    const out = sanitizeHighlight('命中 <mark>关键词</mark><b>粗</b>')
    expect(out).toContain('<mark>关键词</mark>')
    expect(out).not.toContain('<b>')
  })
})
