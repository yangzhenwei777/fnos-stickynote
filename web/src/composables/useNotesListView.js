// useNotesListView —— NotesListView 的全部列表页逻辑（2026-11-03 巨型 Vue 拆分 刀6）。
// 抽取原则：零行为变化（script 体机械搬移自 2.3.2 基线），模板与 scoped 样式原样不动；
// 图标组件 / 展示子组件留在 NotesListView.vue（模板直接引用）。
// 历史修复点注释全部原样保留——改动前先读懂，别删。
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { getNotes, createNote, deleteNote, exportNote, exportAllNotes, batchDeleteNotes, batchExportNotes } from '@/api/note'
import { useFolderStore } from '@/stores/folder'
import { useFolderSecurityStore } from '@/stores/folderSecurity'
import api from '@/api/request'
import { message } from '@/utils/message'
import DOMPurify from 'dompurify'
import { buildSkippedParts, joinSkipped, buildDesktopExportMessage, buildWebExportAllMessage } from '@/utils/exportSummary'
import { importNoteFile } from '@/composables/useNoteImport'
import { IS_DESKTOP } from '@/api/local/transport'
import { exportNoteMD, exportNotesZip } from '@/api/local/desktopApi'

export function useNotesListView() {

// 渲染搜索高亮片段（FTS5 snippet 返回的带 <mark> 标签 HTML）。只允许 mark 标签防 XSS。
function sanitizeHighlight(html) {
  return DOMPurify.sanitize(html, { ALLOWED_TAGS: ['mark'] })
}

const router = useRouter()

// v21.89 文件夹：与 AppSidebar/MainLayout 共享同一 store；标题旁下拉切换，
// 列表内容跟随 activeFolderId 过滤（后端 folder_id 参数 v21.86 已支持）。
const folderStore = useFolderStore()
const folderSecurity = useFolderSecurityStore()
const nlFolderLabel = computed(() => {
  if (folderStore.activeFolderId === 'all') return '全部笔记'
  return folderStore.activeFolder?.name || '我的笔记'
})

function handleFolderChange(cmd) {
  // v21.96 需求：拦截「新建文件夹」命令，弹出名称输入对话框
  if (cmd === '__new_folder__') {
    newFolderName.value = ''
    newFolderDialogVisible.value = true
    return
  }
  // v21.108 P2-10：拦截「管理文件夹」命令
  if (cmd === '__manage_folders__') {
    manageFoldersVisible.value = true
    return
  }
  // v2.3.0：锁定文件夹先验密，取消不切换
  if (typeof cmd === 'number') {
    const f = folderStore.folders.find(x => x.id === cmd)
    if (f && folderStore.isFolderLocked(f)) {
      folderSecurity.openUnlock(f, () => {
        folderStore.selectFolder(cmd)
        currentPage.value = 1
        loadNotes()
        loadTags()
      })
      return
    }
  }
  folderStore.selectFolder(cmd)
  currentPage.value = 1
  loadNotes()
}

// v21.108 P2-10：移动端文件夹管理（重命名/删除复用 folderStore 既有方法，删除非空有后端 409 保护）
const manageFoldersVisible = ref(false)

async function renameFolder(f) {
  let val
  try {
    const r = await ElMessageBox.prompt('请输入新的文件夹名称', '重命名文件夹', {
      inputValue: f.name,
      inputValidator: v => (v && v.trim() ? true : '名称不能为空'),
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    val = (r.value || '').trim()
  } catch { return }
  if (val && val !== f.name) await folderStore.renameFolderById(f.id, val)
}

async function removeFolderConfirm(f) {
  const ok = await message.confirm(
    `确定删除文件夹「${f.name}」吗？文件夹内还有笔记时无法删除。`,
    { title: '删除文件夹', confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning', danger: true }
  )
  if (!ok) return
  const res = await folderStore.removeFolder(f.id)
  if (res.ok) {
    message.success('文件夹已删除')
    await loadNotes()
  } else if (res.notEmpty) {
    message.error('文件夹内还有笔记，请先移出或删除其中的笔记后再删除文件夹')
  } else if (res.locked) {
    message.error('文件夹已加密，请先解密后再删除')
  }
}

// v21.96 需求：移动端新建文件夹
const newFolderDialogVisible = ref(false)
const newFolderName = ref('')
const newFolderLoading = ref(false)
const isMobile = ref(window.innerWidth <= 768)

async function submitNewFolder() {
  const name = newFolderName.value.trim()
  if (!name) {
    message.warning('请输入文件夹名称')
    return
  }
  newFolderLoading.value = true
  try {
    const created = await folderStore.createNewFolder(name)
    if (!created) return // store 内已提示错误
    message.success('文件夹创建成功')
    newFolderDialogVisible.value = false
    // 创建成功后自动切换到新文件夹并刷新列表
    folderStore.selectFolder(created.id)
    currentPage.value = 1
    loadNotes()
  } finally {
    newFolderLoading.value = false
  }
}

const notes = ref([])
const loading = ref(false)
// v21.107 P2-3 列表错误态与 P2-12 新建防重点
const listError = ref('')
const creating = ref(false)
const searchQuery = ref('')
const activeTag = ref('')
const sortBy = ref('updated_at')
const currentPage = ref(1)
const pageSize = ref(24)
// 1.0.218：「全部」模式（前端分批拉全）。安全上限防超大库把 DOM 拖垮，超出明示。
const ALL_NOTES_CAP = 1000
const allMode = ref(false)
const total = ref(0)
const allTags = ref([])
const fileInput = ref(null)

// v20.7 批量操作
const batchMode = ref(false)
const selectedIds = ref(new Set())
const batchExporting = ref(false)

let searchTimer = null

// v18.8: 短关键词提示（1-2 字走后端 LIKE 模糊匹配而非 FTS5 全文索引）
const showShortQueryHint = computed(() => {
  const q = searchQuery.value.trim()
  return q.length > 0 && q.length < 3
})

onMounted(async () => {
  await folderStore.loadFolders()
  loadNotes()
  loadTags()
})

// v21.89：其他入口（如 AppSidebar 文件夹树）切换文件夹时列表同步刷新
watch(() => folderStore.activeFolderId, () => {
  currentPage.value = 1
  loadNotes()
})

// v2.3.0：文件夹加密/解密/解锁后刷新列表与标签
watch(() => folderStore.notesRefreshTick, () => {
  loadNotes()
  loadTags()
})

// baseListParams 列表公共过滤参数（页码/页大小由调用方按模式追加）。
function baseListParams() {
  const params = { sort_by: sortBy.value }
  if (searchQuery.value) params.q = searchQuery.value
  if (activeTag.value) params.tag = activeTag.value
  // v21.89：文件夹过滤（'all' 时 listParam 为 undefined 不传 = 全部）
  if (folderStore.listParam) params.folder_id = folderStore.listParam
  return params
}

async function loadNotes() {
  if (allMode.value) return loadAllNotes()
  loading.value = true
  try {
    const { data } = await getNotes({ ...baseListParams(), page: currentPage.value, page_size: pageSize.value })
    notes.value = data.notes
    total.value = data.total || notes.value.length
    listError.value = ''
  } catch (e) {
    // v21.107 P2-3/P1-6：记录错误态并透传后端具体文案（不再伪装成空列表）
    listError.value = e?.response?.data?.error || '加载笔记失败'
    message.errorOnce(listError.value)
  } finally {
    loading.value = false
  }
}

// loadAllNotes 「全部」模式：服务端 page_size 上限 100，按 100/页逐页累积到 total；
// 达到安全上限 ALL_NOTES_CAP 即停并明示（防超大库一次性渲染卡顿）。
async function loadAllNotes() {
  loading.value = true
  try {
    const params = baseListParams()
    const acc = []
    let pageNo = 1
    let declared = 0
    while (true) {
      const { data } = await getNotes({ ...params, page: pageNo, page_size: 100 })
      const batch = data.notes || []
      declared = data.total || declared
      acc.push(...batch)
      if (batch.length === 0 || acc.length >= declared || acc.length >= ALL_NOTES_CAP) break
      pageNo++
    }
    total.value = declared || acc.length
    notes.value = acc.slice(0, ALL_NOTES_CAP)
    if ((declared || 0) > ALL_NOTES_CAP) {
      message.warning(`笔记较多，已显示前 ${ALL_NOTES_CAP} 条，其余请改用分页浏览`)
    }
    listError.value = ''
  } catch (e) {
    listError.value = e?.response?.data?.error || '加载笔记失败'
    message.errorOnce(listError.value)
  } finally {
    loading.value = false
  }
}

// onPageChange 分页窗点击某页。
function onPageChange(p) {
  if (p === currentPage.value) return
  currentPage.value = p
  loadNotes()
}

// onToggleAll 「全部 ⇄ 分页」切换（分页模式回到第 1 页，简单可预期）。
function onToggleAll(next) {
  allMode.value = Boolean(next)
  currentPage.value = 1
  loadNotes()
}

async function loadTags() {
  try {
    const { data } = await api.get('/notes/tags')
    allTags.value = data.tags || []
  } catch {}
}

function debouncedSearch() {
  clearTimeout(searchTimer)
  currentPage.value = 1
  searchTimer = setTimeout(() => loadNotes(), 300)
}

async function createNewNote() {
  if (creating.value) return // v21.107 P2-12：防双击重复创建
  creating.value = true
  try {
    // v21.89：新建笔记归属当前选中文件夹（与主页行为一致；'all'/'default' 时 null = 默认"我的笔记"）
    const fid = folderStore.listParam
    const payload = { title: '新笔记', content: '<p></p>', tags: '' }
    if (fid && fid !== 'default') payload.folder_id = Number(fid)
    const { data } = await createNote(payload)
    message.success('创建成功')
    // v21.107 P1-11：edit=1 直达编辑态——与 PC 端「新建即编辑」对齐（此前移动端还要再点一次编辑）
    router.push({ path: '/', query: { note_id: data.id, edit: '1' } })
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '创建笔记失败') // v21.107 P1-6 透传配额等具体文案
  } finally {
    creating.value = false
  }
}

async function handleImportFile(e) {
  const file = e.target.files?.[0]
  try {
    // v21.166 计划项4：改复用共用底层（原为手写重复实现，与阅读页分叉）。
    // 返回体校验/提示/错误处理统一在 importNoteFile 内，网格页只保留交互差异（跳转）。
    //
    // ⭐ v21.213（211b 实机：导入的笔记一律落到「我的笔记」）：传入当前所在文件夹
    // `folderStore.listParam`（'all' 时为 undefined = 不指定文件夹）。
    const data = await importNoteFile(file, folderStore.listParam)
    if (data) router.push({ path: '/', query: { note_id: data.id } })
  } finally {
    if (fileInput.value) fileInput.value.value = ''
  }
}

function openNote(note) {
  router.push({ path: '/', query: { note_id: note.id } })
}

async function deleteNoteItem(note) {
  try {
    await deleteNote(note.id)
    message.success('删除成功')
    await loadNotes()
    await loadTags()
    folderStore.loadFolders() // v21.107 P2-17：与主页/回收站删除入口统一刷新文件夹计数
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '删除失败') // v21.107 P1-6 透传
  }
}

// ---- 导出（v17）----
const exportingAll = ref(false)

// 触发浏览器下载：优先取 Content-Disposition 里的 filename*（中文文件名），取不到用兜底名
function saveBlob(resp, fallbackName) {
  const dispo = resp.headers?.['content-disposition'] || ''
  let filename = fallbackName
  const m = dispo.match(/filename\*=UTF-8''([^;]+)/i)
  if (m) {
    try { filename = decodeURIComponent(m[1]) } catch {}
  }
  const blob = resp.data instanceof Blob ? resp.data : new Blob([resp.data])
  const blobUrl = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = blobUrl
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(blobUrl)
}

async function exportSingle(note) {
  try {
    let pwd = ''
    if (note.is_encrypted) {
      // v21.107 P1-5：加密笔记导出后端仅接受 POST+密码——此前列表入口走 GET 必失败
      // 且只报笼统「导出失败」；补密码输入直达（与预览页「下载」入口行为一致）。
      try {
        const r = await ElMessageBox.prompt('这是加密笔记，请输入加密密码后导出', '导出加密笔记', {
          inputType: 'password',
          confirmButtonText: '导出',
          cancelButtonText: '取消'
        })
        pwd = r.value || ''
      } catch {
        return // 用户取消
      }
    }
    if (IS_DESKTOP) {
      // v2.2.2 R6：桌面壳内 /notes/:id/export 未接线（LocalTransport 501），直走 Go 绑定
      const md = await exportNoteMD(note.id, pwd)
      saveBlob(
        { headers: {}, data: new Blob([md], { type: 'text/markdown;charset=utf-8' }) },
        `${note.title || '笔记'}.md`
      )
      message.success('导出成功')
      return
    }
    const resp = note.is_encrypted
      ? await api.post('/notes/' + note.id + '/export', { password: pwd }, { responseType: 'blob' })
      : await exportNote(note.id)
    saveBlob(resp, `${note.title || '笔记'}.md`)
    message.success('导出成功')
  } catch (e) {
    // v21.107 P1-5：密码错（403）给出可行动原因（responseType=blob 时 data 为 Blob，按状态码判断）
    if (e.response?.status === 403) message.error('密码错误，无法导出')
    else message.errorOnce(e.response?.data?.error || e.message || '导出失败')
  }
}

async function exportAll() {
  if (exportingAll.value) return
  exportingAll.value = true
  try {
    if (IS_DESKTOP) {
      // v2.2.2 R6：原生另存为，Go 侧直接落盘 zip（避免浏览器静默下载不可达）
      const r = await exportNotesZip([], true)
      if (r?.cancelled) return
      // 开发纪律 A-15.2：桌面读 body（folders_skipped），Web 读 header，文案共用 exportSummary
      message.success(buildDesktopExportMessage({
        exported: r?.exported,
        skipped: r?.skipped,
        foldersSkipped: r?.folders_skipped,
        savedPath: r?.path
      }))
      return
    }
    const resp = await exportAllNotes()
    saveBlob(resp, 'Sticky-note-导出.zip')
    message.success(buildWebExportAllMessage({
      encryptedSkipped: resp.headers?.['x-encrypted-skipped'],
      folderSkipped: resp.headers?.['x-folder-skipped']
    }))
  } catch (e) {
    message.errorOnce(e.response?.data?.error || e?.message || '导出失败')
  } finally {
    exportingAll.value = false
  }
}

// ---- v20.7 批量操作 ----

function toggleBatchMode() {
  batchMode.value = !batchMode.value
  selectedIds.value = new Set()
}

function toggleSelect(id) {
  const s = new Set(selectedIds.value)
  if (s.has(id)) { s.delete(id) } else { s.add(id) }
  selectedIds.value = s
}

function selectAll() {
  if (selectedIds.value.size === notes.value.length) {
    selectedIds.value = new Set()
  } else {
    selectedIds.value = new Set(notes.value.map(n => n.id))
  }
}

async function batchDelete() {
  if (selectedIds.value.size === 0) return
  const confirmed = await message.confirm(
    `确定要删除 ${selectedIds.value.size} 篇笔记吗？删除后可在回收站中查看与恢复。`,
    { title: '批量删除', confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning', danger: true, customClass: 'msgbox-no-icon' }
  )
  if (!confirmed) return
  try {
    const ids = [...selectedIds.value]
    const { data } = await batchDeleteNotes(ids)
    // v21.107 P2-18：部分失败明示（后端已返回 deleted/total，此前只显示成功条数用户无感）
    const deleted = typeof data.deleted === 'number' ? data.deleted : ids.length
    if (deleted < ids.length) {
      message.warning(`已删除 ${deleted} 篇，${ids.length - deleted} 篇删除失败（可能已被删除），请刷新后重试`)
    } else {
      message.success(data.message || `已删除 ${ids.length} 篇笔记`)
    }
    selectedIds.value = new Set()
    batchMode.value = false
    await loadNotes()
    await loadTags()
    folderStore.loadFolders() // v21.107 P2-17：统一刷新文件夹计数
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '批量删除失败')
  }
}

async function batchExport() {
  if (selectedIds.value.size === 0 || batchExporting.value) return
  batchExporting.value = true
  try {
    const ids = [...selectedIds.value]
    if (IS_DESKTOP) {
      // v2.2.2 R6：原生另存为，Go 侧直接落盘 zip
      const r = await exportNotesZip(ids, false)
      if (r?.cancelled) return
      message.success(buildDesktopExportMessage({
        exported: r?.exported,
        skipped: r?.skipped,
        foldersSkipped: r?.folders_skipped,
        savedPath: r?.path
      }))
      selectedIds.value = new Set()
      batchMode.value = false
      return
    }
    const resp = await batchExportNotes(ids)
    saveBlob(resp, 'Sticky-note-批量导出.zip')
    // v21.58 F-12 / v2.3.0：按后端 header 提示实际导出数量与跳过项（文案与桌面共用）
    const parts = buildSkippedParts(resp.headers?.['x-encrypted-skipped'], resp.headers?.['x-folder-skipped'])
    if (parts.length) {
      const exported = Number(resp.headers?.['x-exported-count'] || ids.length - Number(resp.headers?.['x-encrypted-skipped'] || 0))
      message.success(`已导出 ${exported} 篇笔记，${joinSkipped(parts)}`)
    } else {
      message.success(`已导出 ${ids.length} 篇笔记`)
    }
    selectedIds.value = new Set()
    batchMode.value = false
  } catch (e) {
    message.error(e.response?.data?.error || e.message || '批量导出失败')
  } finally {
    batchExporting.value = false
  }
}
  return {
    activeTag, allMode, batchDelete, batchExport, batchExporting, batchMode, createNewNote, creating, currentPage, debouncedSearch, deleteNoteItem, exportAll, exportSingle, exportingAll, fileInput, folderSecurity, folderStore, handleFolderChange, handleImportFile, isMobile, listError, loadNotes, loading, manageFoldersVisible, newFolderDialogVisible, newFolderLoading, newFolderName, nlFolderLabel, notes, onPageChange, onToggleAll, openNote, pageSize, removeFolderConfirm, renameFolder, sanitizeHighlight, searchQuery, selectAll, selectedIds, showShortQueryHint, sortBy, submitNewFolder, toggleBatchMode, toggleSelect, total
  }
}
