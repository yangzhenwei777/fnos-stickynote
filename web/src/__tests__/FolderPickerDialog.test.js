import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import FolderPickerDialog from '../components/FolderPickerDialog.vue'

// FolderPickerDialog 组件测试（v21.87：移动到/复制到文件夹弹框）
// 关键行为（用户需求 Q2）：打开时默认选中「笔记当前所在文件夹」；
// 确认时 'default' → null（后端 folder_id IS NULL 语义），数字 → Number。

function mountDialog(options = {}) {
  return mount(FolderPickerDialog, {
    props: {
      modelValue: true,
      title: '移动到',
      folders: [
        { id: 1, name: '工作' },
        { id: 2, name: '生活' }
      ],
      currentFolderId: null, // 笔记当前在默认"我的笔记"
      loading: false,
      ...options
    },
    global: {
      stubs: {
        'el-dialog': { template: '<div class="el-dialog"><slot /><slot name="footer" /></div>' },
        'el-icon': { template: '<i class="el-icon"><slot /></i>' },
        'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' }
      }
    }
  })
}

describe('FolderPickerDialog', () => {
  it('笔记在默认"我的笔记"时，默认选中"我的笔记"（Q2 需求）', () => {
    const w = mountDialog()
    const active = w.findAll('.fp-item.active')
    expect(active.length).toBe(1)
    expect(active[0].text()).toContain('我的笔记')
  })

  it('笔记在自定义文件夹时，默认选中对应文件夹', () => {
    const w = mountDialog({ currentFolderId: 2 })
    const active = w.findAll('.fp-item.active')
    expect(active.length).toBe(1)
    expect(active[0].text()).toContain('生活')
  })

  it('点击"我的笔记"→ confirm 发出 null（folder_id IS NULL 语义）', async () => {
    const w = mountDialog()
    await w.findAll('.fp-item')[1].trigger('click') // 工作
    await w.findAll('.fp-item')[0].trigger('click') // 回到我的笔记
    const btn = w.findAll('button').find(b => b.text() === '确定')
    await btn.trigger('click')
    expect(w.emitted('confirm')[0]).toEqual([null])
  })

  it('点击自定义文件夹 → confirm 发出数字 id', async () => {
    const w = mountDialog()
    await w.findAll('.fp-item')[1].trigger('click') // 工作
    const btn = w.findAll('button').find(b => b.text() === '确定')
    await btn.trigger('click')
    expect(w.emitted('confirm')[0]).toEqual([1])
  })

  it('自定义文件夹列表为空时显示空态提示', () => {
    const w = mountDialog({ folders: [] })
    expect(w.text()).toContain('暂无自定义文件夹')
  })

  it('弹框关闭再打开时重置选中项', async () => {
    const w = mountDialog({ currentFolderId: 1 })
    await w.findAll('.fp-item')[0].trigger('click') // 切到我的笔记
    expect(w.findAll('.fp-item.active')[0].text()).toContain('我的笔记')
    // 关闭 → 重新打开（currentFolderId=2）
    await w.setProps({ modelValue: false })
    await w.setProps({ modelValue: true, currentFolderId: 2 })
    const active = w.findAll('.fp-item.active')
    expect(active.length).toBe(1)
    expect(active[0].text()).toContain('生活')
  })
})
