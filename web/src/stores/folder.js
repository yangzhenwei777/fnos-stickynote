import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { getFolders, createFolder, renameFolder, moveFolder, deleteFolder } from '@/api/folder'
import { message } from '@/utils/message'

/**
 * useFolderStore — v21.86 文件夹全局状态。
 * activeFolderId 取值：'all'（全部笔记）| 'default'（我的笔记）| <number>（自定义文件夹 id）。
 * 列表页读 listParam 即可得到后端 folder_id 查询参数（'all' 时为 undefined 不传）。
 */
export const useFolderStore = defineStore('folder', () => {
  const folders = ref([]) // [{id, name, sort_order, note_count, is_default, created_at}]
  const activeFolderId = ref('all')
  const loaded = ref(false)

  const defaultFolder = computed(() => folders.value.find(f => f.is_default) || null)
  const customFolders = computed(() => folders.value.filter(f => !f.is_default))

  // 当前选中文件夹对象（'all' 时为 null）
  const activeFolder = computed(() => {
    if (activeFolderId.value === 'all') return null
    if (activeFolderId.value === 'default') return defaultFolder.value
    return folders.value.find(f => f.id === activeFolderId.value) || null
  })

  // ListNotes 的 folder_id 查询参数：'all' → 不传；'default' → 'default'；数字 → 字符串 id
  const listParam = computed(() => {
    const v = activeFolderId.value
    if (v === 'all') return undefined
    return String(v)
  })

  async function loadFolders() {
    try {
      const { data } = await getFolders()
      folders.value = data.folders || []
      loaded.value = true
    } catch {
      message.error('加载文件夹失败')
    }
    return folders.value
  }

  async function createNewFolder(name) {
    try {
      const { data } = await createFolder(name)
      folders.value.push(data)
      return data
    } catch (e) {
      message.error(e.response?.data?.error || '创建文件夹失败')
      return null
    }
  }

  async function renameFolderById(id, name) {
    try {
      await renameFolder(id, name)
      const f = folders.value.find(f => f.id === id)
      if (f) f.name = name.trim()
      return true
    } catch (e) {
      message.error(e.response?.data?.error || '重命名失败')
      return false
    }
  }

  async function moveFolderTo(id, position) {
    try {
      await moveFolder(id, position)
      // 乐观更新失败也无妨：重拉一次保证顺序与后端一致
      await loadFolders()
      return true
    } catch (e) {
      message.error(e.response?.data?.error || '移动失败')
      return false
    }
  }

  /**
   * 删除文件夹。返回：
   * - { ok: true } 删除成功
   * - { ok: false, notEmpty: true } 文件夹内还有笔记（后端 409 FOLDER_NOT_EMPTY）
   * - { ok: false } 其他错误（已提示）
   */
  async function removeFolder(id) {
    try {
      await deleteFolder(id)
      folders.value = folders.value.filter(f => f.id !== id)
      // 删除的是当前选中文件夹 → 回退到全部笔记
      if (activeFolderId.value === id) activeFolderId.value = 'all'
      return { ok: true }
    } catch (e) {
      if (e.response?.status === 409 && e.response?.data?.code === 'FOLDER_NOT_EMPTY') {
        return { ok: false, notEmpty: true }
      }
      message.error(e.response?.data?.error || '删除文件夹失败')
      return { ok: false }
    }
  }

  function selectFolder(key) {
    activeFolderId.value = key
  }

  // v21.88 「回到我的笔记页面」信号：移动端 LOGO 点击时自增，
  // NotesView watch 它关闭当前打开的笔记（含切换前保存），回到列表态。
  const homeTick = ref(0)
  function goHomeTick() {
    homeTick.value++
  }

  return {
    folders, activeFolderId, activeFolder, listParam, loaded, homeTick,
    defaultFolder, customFolders,
    loadFolders, createNewFolder, renameFolderById, moveFolderTo, removeFolder,
    selectFolder, goHomeTick
  }
})
