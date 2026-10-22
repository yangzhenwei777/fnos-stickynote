/**
 * NotesListView 移动端「新建文件夹」测试（v21.96 需求）：
 * 菜单项在「全部笔记」下方 → 弹名称输入框 → 创建成功自动切换到新文件夹并刷新列表。
 *
 * mock 策略：api/note、api/request、stores/folder、utils/message 全部打桩，
 * el-dropdown / el-dialog / el-input / el-button 用轻量 stub 以便在 jsdom 中驱动交互。
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import NotesListView from '../views/NotesListView.vue'

// ---- mocks ----
vi.mock('@/api/note', () => ({
  getNotes: vi.fn().mockResolvedValue({ data: { notes: [], total: 0 } }),
  createNote: vi.fn(),
  deleteNote: vi.fn(),
  exportNote: vi.fn(),
  exportAllNotes: vi.fn(),
  batchDeleteNotes: vi.fn(),
  batchExportNotes: vi.fn()
}))
vi.mock('@/api/request', () => ({
  default: { get: vi.fn().mockResolvedValue({ data: {} }) }
}))

const store = {
  folders: [],
  activeFolderId: 'all',
  activeFolder: null,
  customFolders: [],
  listParam: undefined,
  homeTick: 0,
  loadFolders: vi.fn().mockResolvedValue([]),
  createNewFolder: vi.fn(),
  selectFolder: vi.fn(),
  goHomeTick: vi.fn()
}
vi.mock('@/stores/folder', () => ({
  useFolderStore: () => store
}))

vi.mock('@/utils/message', () => ({
  message: { success: vi.fn(), warning: vi.fn(), error: vi.fn() }
}))

import { message } from '@/utils/message'

// ---- stubs ----
const ElDropdownStub = {
  name: 'ElDropdown',
  // 同时渲染默认插槽与 #dropdown 命名插槽（Element Plus 真组件默认懒渲染 popper）
  template: '<div class="el-dropdown"><slot /><slot name="dropdown" /></div>'
}
const ElDialogStub = {
  name: 'ElDialog',
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: '<div class="el-dialog" v-if="modelValue"><slot /><slot name="footer" /></div>'
}
const ElInputStub = {
  name: 'ElInput',
  props: ['modelValue'],
  emits: ['update:modelValue', 'keyup:enter'],
  template:
    '<input class="el-input-stub" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" @keyup.enter="$emit(\'keyup:enter\')" />'
}
const ElButtonStub = {
  name: 'ElButton',
  props: ['loading'],
  emits: ['click'],
  template: '<button class="el-button-stub" @click="$emit(\'click\')"><slot /></button>'
}

function mountList() {
  return mount(NotesListView, {
    global: {
      stubs: {
        'el-dropdown': ElDropdownStub,
        'el-dropdown-menu': { template: '<div class="dd-menu"><slot /></div>' },
        'el-dropdown-item': { template: '<div class="dd-item"><slot /></div>' },
        'el-dialog': ElDialogStub,
        'el-input': ElInputStub,
        'el-button': ElButtonStub,
        'el-icon': { template: '<i class="el-icon"><slot /></i>' },
        'el-tag': { template: '<span class="el-tag"><slot /></span>' },
        'el-checkbox': { template: '<span class="el-checkbox" />' },
        Plus: true,
        Document: true,
        Search: true,
        Edit: true,
        Delete: true,
        Clock: true,
        Tickets: true,
        Download: true,
        Upload: true,
        Folder: true,
        Files: true,
        ArrowDown: true,
        Check: true
      }
    }
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  store.activeFolderId = 'all'
  store.folders = []
  store.customFolders = []
  store.createNewFolder.mockResolvedValue({ id: 7, name: '新夹', is_default: false, sort_order: 1, note_count: 0 })
})

describe('NotesListView 移动端新建文件夹（v21.96）', () => {
  it('下拉菜单中「新建文件夹」项位于「全部笔记」之下', () => {
    const w = mountList()
    const items = w.findAll('.dd-item')
    // 顺序：全部笔记 → 新建文件夹 → 我的笔记 → 自定义文件夹(v-for 空)
    expect(items[0].text()).toContain('全部笔记')
    expect(items[1].text()).toContain('新建文件夹')
    expect(items[2].text()).toContain('我的笔记')
  })

  it('v21.97：三项文件夹入口均带图标（对齐 PC 端 Files/Document/Folder）', () => {
    store.customFolders = [{ id: 9, name: '工作' }]
    const w = mountList()
    const items = w.findAll('.dd-item')
    // 全部笔记 → Files 图标
    expect(items[0].find('.el-icon').exists()).toBe(true)
    // 新建文件夹 → Plus 图标
    expect(items[1].find('.el-icon').exists()).toBe(true)
    // 我的笔记 → Document 图标
    expect(items[2].find('.el-icon').exists()).toBe(true)
    // 自定义文件夹 → Folder 图标
    expect(items[3].find('.el-icon').exists()).toBe(true)
  })

  it('点击「新建文件夹」→ 弹出名称输入对话框', async () => {
    const w = mountList()
    expect(w.find('.el-dialog').exists()).toBe(false)
    // 通过 el-dropdown 的 command 事件模拟选中「新建文件夹」
    const dd = w.findComponent(ElDropdownStub)
    dd.vm.$emit('command', '__new_folder__')
    await flushPromises()
    expect(w.find('.el-dialog').exists()).toBe(true)
  })

  it('输入名称提交 → 创建成功、自动切换到新文件夹、列表刷新', async () => {
    const w = mountList()
    w.findComponent(ElDropdownStub).vm.$emit('command', '__new_folder__')
    await flushPromises()
    const input = w.find('input.el-input-stub')
    await input.setValue('工作')
    // 点确定
    const btn = w.findAll('button.el-button-stub').find(b => b.text() === '确定')
    await btn.trigger('click')
    await flushPromises()
    expect(store.createNewFolder).toHaveBeenCalledWith('工作')
    expect(store.selectFolder).toHaveBeenCalledWith(7)
    expect(message.success).toHaveBeenCalled()
    // 对话框关闭
    expect(w.find('.el-dialog').exists()).toBe(false)
  })

  it('空名称提交 → warning 提示，不发起创建', async () => {
    const w = mountList()
    w.findComponent(ElDropdownStub).vm.$emit('command', '__new_folder__')
    await flushPromises()
    const btn = w.findAll('button.el-button-stub').find(b => b.text() === '确定')
    await btn.trigger('click')
    await flushPromises()
    expect(message.warning).toHaveBeenCalledWith('请输入文件夹名称')
    expect(store.createNewFolder).not.toHaveBeenCalled()
  })

  it('创建失败（重名）→ 不切换文件夹、不关闭对话框', async () => {
    store.createNewFolder.mockResolvedValue(null) // store 内已提示错误
    const w = mountList()
    w.findComponent(ElDropdownStub).vm.$emit('command', '__new_folder__')
    await flushPromises()
    const input = w.find('input.el-input-stub')
    await input.setValue('工作')
    const btn = w.findAll('button.el-button-stub').find(b => b.text() === '确定')
    await btn.trigger('click')
    await flushPromises()
    expect(store.createNewFolder).toHaveBeenCalledWith('工作')
    expect(store.selectFolder).not.toHaveBeenCalled()
    expect(w.find('.el-dialog').exists()).toBe(true) // 保持打开可重试
  })

  it('普通文件夹命令（非新建）仍走原切换逻辑', async () => {
    const w = mountList()
    w.findComponent(ElDropdownStub).vm.$emit('command', 'default')
    await flushPromises()
    expect(store.selectFolder).toHaveBeenCalledWith('default')
    expect(store.createNewFolder).not.toHaveBeenCalled()
  })
})
