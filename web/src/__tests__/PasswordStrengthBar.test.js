import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import PasswordStrengthBar from '../components/PasswordStrengthBar.vue'

// PasswordStrengthBar 组件测试（v21.77 P1：前端组件测试补全）
// 评分规则：长度≥8(+1)、≥12(+1)、大小写混合(+1)、含数字(+1)、含符号(+1)
// level：score≤2→1(弱)，≤3→2(中)，否则 3(强)

function mountBar(password) {
  return mount(PasswordStrengthBar, { props: { password } })
}

describe('PasswordStrengthBar', () => {
  it('空密码不渲染', () => {
    const w = mountBar('')
    expect(w.find('.pwd-strength').exists()).toBe(false)
  })

  it('弱密码：短纯字母 → level 1 弱 + 1 条激活 + 提示存在', () => {
    const w = mountBar('abc')
    const text = w.text()
    expect(text).toContain('弱')
    expect(w.findAll('.pwd-strength-bar.active').length).toBe(1)
    expect(w.find('.pwd-strength-tip').exists()).toBe(true)
  })

  it('中密码：8位+大小写+数字 → level 2 中 + 2 条激活', () => {
    const w = mountBar('Abcdef12')
    expect(w.text()).toContain('中')
    expect(w.findAll('.pwd-strength-bar.active').length).toBe(2)
  })

  it('强密码：8位+大小写+数字+符号 → level 3 强 + 3 条激活 + 无提示', () => {
    const w = mountBar('Abcdef12!')
    const text = w.text()
    expect(text).toContain('强')
    expect(w.findAll('.pwd-strength-bar.active').length).toBe(3)
    expect(w.find('.pwd-strength-tip').exists()).toBe(false)
  })

  it('超长密码仍为强（含 ≥12 加分）', () => {
    const w = mountBar('Abcdef123456!')
    expect(w.text()).toContain('强')
    expect(w.findAll('.pwd-strength-bar.active').length).toBe(3)
  })

  it('密码变化响应式更新强度', async () => {
    const w = mountBar('abc')
    expect(w.text()).toContain('弱')
    await w.setProps({ password: 'Abcdef12!' })
    expect(w.text()).toContain('强')
  })
})
