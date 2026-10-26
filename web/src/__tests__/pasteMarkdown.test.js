import { describe, it, expect } from 'vitest'
import { isMarkdownPaste, mdToHtml } from '@/components/editor/pasteMarkdown'

describe('isMarkdownPaste 触发判定（v2.2.1 收紧）', () => {
  it('块级语法命中：标题/列表/引用/任务清单/有序列表', () => {
    expect(isMarkdownPaste('# 标题')).toBe(true)
    expect(isMarkdownPaste('## 二级\n正文')).toBe(true)
    expect(isMarkdownPaste('- 项目')).toBe(true)
    expect(isMarkdownPaste('* 项目')).toBe(true)
    expect(isMarkdownPaste('1. 第一项')).toBe(true)
    expect(isMarkdownPaste('> 引用')).toBe(true)
    expect(isMarkdownPaste('- [ ] 待办')).toBe(true)
  })

  it('整行行内代码命中，行内片段不命中', () => {
    expect(isMarkdownPaste('`code`')).toBe(true)
    expect(isMarkdownPaste('前缀 `code` 后缀')).toBe(false)
  })

  it('普通文本不误伤（用户反馈案例：减号连数字/星号在句中/纯多行文本）', () => {
    expect(isMarkdownPaste('-2026年10月26日')).toBe(false)
    expect(isMarkdownPaste('*重点内容在句中*')).toBe(false)
    expect(isMarkdownPaste('第一行\n第二行\n第三行')).toBe(false)
    expect(isMarkdownPaste('')).toBe(false)
    expect(isMarkdownPaste(null)).toBe(false)
  })
})

describe('mdToHtml 转换', () => {
  it('标题与粗体', () => {
    const html = mdToHtml('# 标题\n**粗体**')
    expect(html).toContain('<h1>标题</h1>')
    expect(html).toContain('<strong>粗体</strong>')
  })

  it('无序列表', () => {
    expect(mdToHtml('- 甲\n- 乙')).toContain('<ul><li>甲</li></ul>')
  })

  it('任务清单带勾选态', () => {
    const html = mdToHtml('- [x] 完成\n- [ ] 未完成')
    expect(html).toContain('data-type="taskItem"')
    expect(html).toContain('data-checked="true"')
    expect(html).toContain('data-checked="false"')
  })

  it('无 Markdown 语法时原样返回（调用方放行默认粘贴）', () => {
    const text = '普通文本第一行\n第二行'
    expect(mdToHtml(text)).toBe(text)
  })
})
