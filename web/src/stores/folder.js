import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  getFolders, createFolder, renameFolder, moveFolder, deleteFolder,
  encryptFolder, decryptFolder, unlockFolder
} from '@/api/folder'
import { message } from '@/utils/message'
import { IS_DESKTOP } from '@/api/local/transport'
import { saveGrant, removeGrant, clearGrants, grantedFolderIds } from '@/utils/folderGrants'

/**
 * useFolderStore — v21.86 文件夹全局状态；v2.3.0 增加访问锁。
 * activeFolderId 取值：'all'（全部笔记）| 'default'（我的笔记）| <number>（自定义文件夹 id）。
 * 列表页读 listParam 即可得到后端 folder_id 查询参数（'all' 时为 undefined 不传）。
 */
export const useFolderStore = defineStore('folder', () => {
  const folders = ref([]) // [{id, name, sort_order, note_count, is_default, is_encrypted, created_at}]
  const activeFolderId = ref('all')
  const loaded = ref(false)
  // 本会话已解锁的文件夹 id（Web 由 sessionStorage 令牌支撑；桌面由 Go 进程内存支撑）
  // Web 端同步初始化，保证首屏 loadFolders 前解锁态已就绪；桌面端异步回填（Go 是权威）。
  const unlockedFolderIds = ref(IS_DESKTOP ? [] : grantedFolderIds())
  // 锁定/解锁后通知列表与笔记视图刷新
  const notesRefreshTick = ref(0)
  // 锁定事件：{ folderId, tick }，NotesView 据此关闭正在查看的笔记
  const lockSignal = ref(null)

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

  function isFolderLocked(f) {
    if (!f || !f.is_encrypted) return false
    return !unlockedFolderIds.value.includes(f.id)
  }

  function bumpNotesRefresh() {
    notesRefreshTick.value++
  }

  function markUnlocked(id) {
    if (!unlockedFolderIds.value.includes(id)) {
      unlockedFolderIds.value = [...unlockedFolderIds.value, id]
    }
  }

  function forgetUnlocked(id) {
    unlockedFolderIds.value = unlockedFolderIds.value.filter(v => v !== id)
    removeGrant(id)
  }

  async function restoreUnlocks() {
    if (IS_DESKTOP) {
      try {
        const { listFolderUnlockStatus } = await import('@/api/local/desktopApi')
        const ids = await listFolderUnlockStatus()
        unlockedFolderIds.value = Array.isArray(ids) ? ids.map(Number).filter(n => n > 0) : []
      } catch {
        unlockedFolderIds.value = []
      }
      return
    }
    unlockedFolderIds.value = grantedFolderIds()
  }

  function clearFolderUnlocks() {
    unlockedFolderIds.value = []
    clearGrants()
  }

  async function loadFolders() {
    try {
      const { data } = await getFolders()
      folders.value = data.folders || []
      loaded.value = true
    } catch {
      message.error('加载文件夹失败')
    }
    // 当前选中的文件夹若已锁定且本会话未解锁，回退到「全部笔记」，避免停在不可见视图
    const active = activeFolder.value
    if (active && isFolderLocked(active)) {
      activeFolderId.value = 'all'
      bumpNotesRefresh()
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
      forgetUnlocked(id)
      return { ok: true }
    } catch (e) {
      if (e.response?.status === 409 && e.response?.data?.code === 'FOLDER_NOT_EMPTY') {
        return { ok: false, notEmpty: true }
      }
      if (e.response?.status === 423 && e.response?.data?.code === 'FOLDER_LOCKED') {
        return { ok: false, locked: true }
      }
      message.error(e.response?.data?.error || '删除文件夹失败')
      return { ok: false }
    }
  }

  // v2.3.0 文件夹访问锁：加密（设置密码并锁定）
  async function encryptFolderById(id, password, confirmPassword) {
    const { data } = await encryptFolder(id, password, confirmPassword)
    forgetUnlocked(id)
    const wasActive = activeFolderId.value === id
    await loadFolders()
    if (wasActive) {
      activeFolderId.value = 'all'
    }
    lockSignal.value = { folderId: id, tick: Date.now() }
    bumpNotesRefresh()
    return data
  }

  // v2.3.0：永久解密（移除文件夹密码）
  async function decryptFolderById(id, password) {
    const { data } = await decryptFolder(id, password)
    forgetUnlocked(id)
    await loadFolders()
    bumpNotesRefresh()
    return data
  }

  // v2.3.0：本会话解锁（密码校验通过后写入 grant / 桌面内存）
  async function unlockFolderById(id, password) {
    const { data } = await unlockFolder(id, password)
    if (data?.unlock_token) saveGrant(id, data.unlock_token)
    markUnlocked(id)
    // 解锁后计数与锁定态可能变化，刷新一次文件夹列表
    await loadFolders()
    bumpNotesRefresh()
    return data
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
    unlockedFolderIds, notesRefreshTick, lockSignal,
    isFolderLocked, restoreUnlocks, clearFolderUnlocks,
    loadFolders, createNewFolder, renameFolderById, moveFolderTo, removeFolder,
    encryptFolderById, decryptFolderById, unlockFolderById,
    bumpNotesRefresh, selectFolder, goHomeTick
  }
})
