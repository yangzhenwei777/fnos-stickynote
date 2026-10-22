import { ref, computed } from 'vue'
import { message } from '@/utils/message'
import { getNotes, createNote, deleteNote, getNote } from '@/api/note'
import api from '@/api/request'

// 列表分页口径（2026-10-15 用户需求）：主页第二栏默认 20 条 + 滚动触底续载 20 条。
// 显式传参钉死两端语义（桌面 LocalTransport / 服务端 handler 缺省同为 20，但不依赖缺省值）。
const LIST_PAGE_SIZE = 20

/**
 * useNotes — 笔记列表 CRUD + 最近笔记（从 NotesView.vue 拆分，P2-1）
 * @param {import('vue').Ref<object|null>} currentNote - 当前选中笔记（双向）
 * @param {import('vue').Ref<string[]>} currentTagInput - 当前标签输入
 */
export function useNotes(currentNote, currentTagInput) {
  const notes = ref([])
  const recentNotes = ref([])
  const loading = ref(false)
  // v21.107 P2-3：列表加载错误态——错误不再伪装成「还没有笔记」空态，
  // 视图据 listError 显示错误态+重试，与真空态区分。
  const listError = ref('')
  // 滚动续载状态（视图据 loadingMore/hasMore 渲染尾部「加载中/已全部加载」）。
  const loadingMore = ref(false)
  const total = ref(0)
  const hasMore = computed(() => notes.value.length < total.value)

  // 请求序号：每次请求递增；响应落地前比对，非最新请求（切搜索/文件夹/排序后
  // 仍在途的旧响应、或竞态下后发的旧页）直接整包丢弃——杜绝旧过滤条件的尾巴追加进新列表。
  let requestSeq = 0
  // 重置请求在途标志：第一页未落地前禁止续载（防 page=2 先于 page=1 落地造成缺页）。
  let inflightReset = false
  // 下一页页码与最近一次重置加载的过滤参数（续载必须携带同一组过滤条件）。
  let nextPage = 1
  let lastParams = {}

  async function fetchPage(params, page) {
    const seq = ++requestSeq
    const isFirst = page === 1
    if (isFirst) {
      loading.value = true
      inflightReset = true
    } else {
      loadingMore.value = true
    }
    try {
      const { data } = await getNotes({ ...params, page, page_size: LIST_PAGE_SIZE })
      if (seq !== requestSeq) return // 过期响应：已有更新的请求，整包丢弃
      const batch = data.notes || []
      total.value = Number(data.total) || 0
      if (isFirst) {
        notes.value = batch
        // 最近笔记只随重置加载刷新（前 5 条必在第一页，续载不影响）
        recentNotes.value = [...notes.value]
          .sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))
          .slice(0, 5)
      } else {
        // id 去重：会话内编辑会让服务端重排（savedNote 置顶），续载页理论上
        // 可能撞到已加载条目——跳过即可，最多本页少 N 条、下页自动补齐。
        const seen = new Set(notes.value.map(n => n.id))
        notes.value.push(...batch.filter(n => !seen.has(n.id)))
      }
      nextPage = page + 1
      listError.value = ''
    } catch (e) {
      if (seq !== requestSeq) return
      // v21.107 P1-6：透传后端具体文案（配额/限流等），兜底通用语
      listError.value = e?.response?.data?.error || '加载笔记失败'
      message.errorOnce(listError.value)
    } finally {
      // 只有仍是最新的请求才能动全局 loading 态；过期请求的 finally 不许碰
      // （否则旧慢响应会把新请求刚置位的 spinner 复位掉）。
      if (seq === requestSeq) {
        if (isFirst) {
          loading.value = false
          inflightReset = false
        } else {
          loadingMore.value = false
        }
      }
    }
  }

  // loadNotes 重置加载：第 1 页替换列表。签名保持不变（NotesView 各调用点零改动）。
  async function loadNotes(searchQuery = '', activeTag = '', folderParam) {
    const params = {}
    if (searchQuery) params.q = searchQuery
    if (activeTag) params.tag = activeTag
    // v21.86：文件夹过滤（undefined = 全部；'default' = 我的笔记；数字 = 自定义文件夹）
    if (folderParam) params.folder_id = folderParam
    // 作废在途请求（含旧续载），清续载态；spinner 立即置位防闪。
    requestSeq++
    loadingMore.value = false
    inflightReset = true
    lastParams = params
    nextPage = 1
    await fetchPage(params, 1)
  }

  // loadMoreNotes 滚动触底续载：下一页追加。三闸门（首页在途 / 续载在途 / 无更多）。
  // 返回是否真的发起了请求（视图无需关心，仅便于测试断言）。
  async function loadMoreNotes() {
    if (loading.value || inflightReset || loadingMore.value || !hasMore.value) return false
    await fetchPage(lastParams, nextPage)
    return true
  }

  async function loadTags() {
    try {
      const { data } = await api.get('/notes/tags')
      return data.tags || []
    } catch {
      return []
    }
  }

  // v21.86：folderID 可选——数字 = 归属该自定义文件夹；null = 默认"我的笔记"。
  // （'all'/'default' 由调用方归一化后再传入）
  async function createNewNote(folderID = null) {
    try {
      const payload = { title: '新笔记', content: '<p></p>', tags: '' }
      if (folderID) payload.folder_id = folderID
      const { data } = await createNote(payload)
      notes.value.unshift(data)
      currentNote.value = { ...data }
      currentTagInput.value = []
      return data
    } catch (e) {
      // v21.107 P1-6：透传后端具体文案（如配额满「已达到笔记数量上限…请先清理」），
      // 此前吞成「创建笔记失败」用户不知原因与出路
      message.errorOnce(e?.response?.data?.error || '创建笔记失败')
      return null
    }
  }

  async function selectNote(note) {
    // v21.84 B：点选时重新拉取最新数据（含 version），避免复用列表缓存里的过期乐观锁版本导致 409；
    // 拉取失败回退到传入的列表项，不阻断切换。
    try {
      const { data } = await getNote(note.id)
      currentNote.value = { ...data }
      currentTagInput.value = data.tags ? data.tags.split(',').map(t => t.trim()).filter(Boolean) : []
    } catch {
      currentNote.value = { ...note }
      currentTagInput.value = note.tags ? note.tags.split(',').map(t => t.trim()).filter(Boolean) : []
    }
  }

  async function openNoteFromQuery(noteId) {
    if (!noteId) return
    try {
      const { data } = await getNote(noteId)
      currentNote.value = { ...data }
      currentTagInput.value = data.tags ? data.tags.split(',').map(t => t.trim()).filter(Boolean) : []
      return data
    } catch (e) {
      // v21.107 P2-4：区分「笔记不存在/已在回收站」与一般加载失败
      if (e?.response?.status === 404) {
        message.error('笔记不存在或已被删除（可在回收站查看）')
      } else {
        message.errorOnce(e?.response?.data?.error || '加载笔记失败')
      }
      return null
    }
  }

  async function removeNote(noteId) {
    try {
      await deleteNote(noteId)
      currentNote.value = null
      message.success('删除成功')
      return true
    } catch {
      message.error('删除失败')
      return false
    }
  }

  // v21.150 E7：排序键只认服务端返回的 updated_at——此前用客户端时钟写键（与服务端权威时间
  // 双源），且响应窗口内切换笔记时补丁会盖到新笔记头上（未编辑项被置顶的根因）
  function applySavedToLists(id, title, content, tags, version, updatedAt) {
    const idx = notes.value.findIndex(n => n.id === id)
    if (idx !== -1) {
      // v21.84：同步成功保存后的乐观锁 version 到列表项，避免下次复用列表缓存时带过期版本导致 409
      const patch = { title, content, tags }
      if (typeof version === 'number') patch.version = version
      if (typeof updatedAt === 'string' && updatedAt) patch.updated_at = updatedAt
      notes.value[idx] = { ...notes.value[idx], ...patch }
      notes.value.sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))
    }
    recentNotes.value = [...notes.value]
      .sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at))
      .slice(0, 5)
  }

  // v21.150 E7：加密/解密在服务端不更新 updated_at（SetEncrypted/ClearEncrypted 均不改该列），
  // 故只就地补丁字段——不再盖客户端时钟、不再无条件置顶（否则刷新后位置会跳回）
  function syncNoteInList(id, patch) {
    const idx = notes.value.findIndex(n => n.id === id)
    if (idx !== -1) {
      notes.value[idx] = { ...notes.value[idx], ...patch }
    }
  }

  return {
    notes, recentNotes, loading, listError,
    loadingMore, total, hasMore, loadMoreNotes,
    loadNotes, loadTags, createNewNote, selectNote, openNoteFromQuery,
    removeNote, applySavedToLists, syncNoteInList
  }
}
