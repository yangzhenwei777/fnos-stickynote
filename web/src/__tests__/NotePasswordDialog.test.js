import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import NotePasswordDialog from '../components/NotePasswordDialog.vue'

// NotesView 弹框组件化：NotePasswordDialog 呈现层测试。
// 校验：encrypt 模式双输入框 + 不可恢复警示；非 encrypt 单输入框 + placeholder；
// 输入经 v-model:input / v-model:confirm 回传；确认→submit；取消→update:modelValue false。

function mountDialog(props = {}) {
  return mount(NotePasswordDialog, {
    props: {
      modelValue: true,
      mode: 'view',
      title: '查看密码',
      placeholder: '请输入笔记密码',
      loading: false,
      isMobile: false,
      input: '',
      confirm: '',
      ...props
    },
    global: {
      stubs: {
        'el-dialog': {
          template: '<div class="el-dialog"><slot /><div class="dlg-footer"><slot name="footer" /></div></div>'
        },
        'el-form': { template: '<form><slot /></form>' },
        'el-form-item': { template: '<div class="fi"><slot /></div>' },
        'el-input': {
          props: ['modelValue', 'placeholder'],
          emits: ['update:modelValue'],
          template: '<input class="ei" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" @keyup.enter="$emit(\'keyup.enter\')" />'
        },
        'el-button': {
          emits: ['click'],
          template: '<button class="eb" @click="$emit(\'click\')"><slot /></button>'
        }
      }
    }
  })
}

describe('NotePasswordDialog', () => {
  it('encrypt 模式：渲染两个输入框 + 加密不可恢复警示', () => {
    const w = mountDialog({ mode: 'encrypt', title: '加密笔记' })
    expect(w.findAll('.ei').length).toBe(2)
    expect(w.text()).toContain('无法恢复')
    expect(w.text()).toContain('管理员也无法找回')
  })

  it('非 encrypt 模式：单个输入框并透传 placeholder', () => {
    const w = mountDialog({ mode: 'decrypt', placeholder: '请输入原密码以解密' })
    expect(w.findAll('.ei').length).toBe(1)
    expect(w.find('.ei').attributes('placeholder')).toBe('请输入原密码以解密')
  })

  it('输入密码经 v-model:input 回传', () => {
    const w = mountDialog({ mode: 'view' })
    w.find('.ei').setValue('secret')
    expect(w.emitted('update:input')).toBeTruthy()
    expect(w.emitted('update:input')[0]).toEqual(['secret'])
  })

  it('encrypt 模式确认密码框经 v-model:confirm 回传', () => {
    const w = mountDialog({ mode: 'encrypt' })
    const inputs = w.findAll('.ei')
    inputs[1].setValue('again')
    expect(w.emitted('update:confirm')[0]).toEqual(['again'])
  })

  it('点【确认】触发 submit', () => {
    const w = mountDialog({ mode: 'view' })
    const btns = w.findAll('.eb')
    btns[btns.length - 1].trigger('click')
    expect(w.emitted('submit')).toBeTruthy()
  })

  it('点【取消】置 modelValue=false（关闭弹框）', () => {
    const w = mountDialog({ mode: 'view' })
    w.findAll('.eb')[0].trigger('click')
    expect(w.emitted('update:modelValue')).toBeTruthy()
    expect(w.emitted('update:modelValue')[0]).toEqual([false])
  })
})
