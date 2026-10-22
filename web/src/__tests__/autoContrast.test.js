import { describe, it, expect } from 'vitest'
import { buildContrastRules } from '@/utils/autoContrast'

// v21.120 回归：暗色纯黑字仍不反白的根因是选择器构造 bug——旧实现把三态容器逗号拼成
// 前缀后，只在末尾追加一次 [style*=…]，导致属性限定只绑到最后一段(.view-content)。
// 编辑态(.ProseMirror)/预览态(.note-preview-content)于是变成给容器本身设色，容器色覆盖不了
// 子 span 自己的内联 color → 彩色文字没被提亮。以下断言确保 [style*=…] 逐作用域各绑一份。
describe('buildContrastRules 选择器结构（v21.120 修复）', () => {
  const SCOPES = ['.ProseMirror', '.note-preview-content', '.view-content']

  it('单个颜色条目：每个逗号分组选择器都必须自带 [style*=…] 限定（证明逐作用域绑定）', () => {
    const css = buildContrastRules([['#000000', '#e2e8f0']])
    const selectors = css.replace(/\{[^}]*\}$/, '').split(',')
    expect(selectors).toHaveLength(SCOPES.length)
    // 关键回归断言：每一段都命中属性限定，任何一段缺失即旧 bug 复现
    for (const seg of selectors) {
      expect(seg).toMatch(/\[style\*="#000000" i\]/)
    }
    for (const scope of SCOPES) {
      expect(css).toContain(`html[data-theme="dark"] ${scope} [style*="#000000" i]`)
    }
  })

  it('绝不产生“给容器本身设色”的裸选择器（旧写法：.ProseMirror, … .view-content [style*=…]）', () => {
    const css = buildContrastRules([['#000000', '#e2e8f0']])
    // 旧 bug 形态：某作用域后直接逗号、其后选择器才带属性
    expect(css).not.toMatch(/\.ProseMirror,/)
    expect(css).not.toMatch(/\.note-preview-content,/)
  })

  it('声明体使用 !important 覆盖显示（仅暗色作用域）', () => {
    const css = buildContrastRules([['#8b1a1a', '#f87171']])
    expect(css).toMatch(/\{color:#f87171!important\}$/)
  })

  it('多条目：为每个原色各生成一组逐作用域选择器', () => {
    const css = buildContrastRules([['#000000', '#e2e8f0'], ['#8b1a1a', '#f87171']])
    expect(css.split('{').length - 1).toBe(2) // 两条规则
    expect((css.match(/\[style\*=/g) || []).length).toBe(SCOPES.length * 2)
  })

  it('原色中的引号/反斜杠被转义，避免破坏属性选择器', () => {
    const css = buildContrastRules([['a"b\\c', '#ffffff']])
    expect(css).toContain('[style*="a\\"b\\\\c" i]')
  })
})
