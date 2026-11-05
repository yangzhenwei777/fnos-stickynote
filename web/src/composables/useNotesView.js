// useNotesView —— NotesView 的全部视图编排逻辑（2026-11-03 巨型 Vue 拆分 刀4）。
// 抽取原则：零行为变化（setup 体机械搬移），模板与外置样式 notes-view.css 不动；
// 图标组件 / 展示子组件 / formatDate 留在 NotesView.vue（模板直接引用）。
// 历史修复点注释全部原样保留——改动前先读懂，别删。

import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useRouter, useRoute, onBeforeRouteLeave } from 'vue-router'
import { ElMessageBox } from 'element-plus'
import { exportNotePoster } from '@/utils/notePoster'
import { sanitizeNoteHtml, sanitizeHighlight } from '@/utils/sanitize'
import { installAutoContrast } from '@/utils/autoContrast'
import { useNotes } from '@/composables/useNotes'
import { useNoteSearch } from '@/composables/useNoteSearch'
import { useNoteEncrypt } from '@/composables/useNoteEncrypt'
import { useAutoSave } from '@/composables/useAutoSave'
import { useNoteImport } from '@/composables/useNoteImport'
import { extractNoteOutline, shouldBlockOutlineWheel } from '@/composables/useNoteOutline'
import { useFolderStore } from '@/stores/folder'
import { useFolderSecurityStore } from '@/stores/folderSecurity'
import { stripHtml, toggleTaskItemInHtml } from '@/utils/note'
import { message } from '@/utils/message'
// v21.220：代码块复制按钮（预览态挂载；编辑态由 TiptapEditor 内 CodeBlockCopy 扩展负责）
import { attachCodeCopyButtons } from '@/components/editor/codeBlockCopy'
import { moveNoteToFolder, copyNoteToFolder } from '@/api/note'
import { installSyncRefreshPolling } from '@/api/local/desktopApi'
import { IS_DESKTOP } from '@/api/local/transport'

export function useNotesView() {
const router = useRouter()
const route = useRoute()

// v21.86 文件夹状态（左侧菜单栏 AppSidebar 共享；v21.89 起移动端入口已迁 /notes-list，
// 主页不再需要 mobileFolderLabel/handleMobileFolder）
const folderStore = useFolderStore()
const folderSecurity = useFolderSecurityStore()
const customFolders = computed(() => folderStore.customFolders)

// ---- UI 状态 ----
const currentNote = ref(null)
const currentTagInput = ref([])
const editMode = ref(false)
const previewMode = ref(false)
const isMobile = ref(false)
const sidebarOpen = ref(false)
const sidebarCollapsed = ref(false)
const imageViewerVisible = ref(false)
const imageViewerList = ref([])
const imageViewerIndex = ref(0)
// v21.104（方案 C）：URL 带 note_id 时首帧即进入加载态，避免挂载后短暂渲染「欢迎回来」空态。
// 目标笔记与列表/文件夹/标签并行加载 + 骨架屏兜底，PC/移动端共用。
const openingNote = ref(!!route.query.note_id)
// v21.85：编辑器组件引用——内容防抖回流后，保存/切换/预览等关键路径
// 需要经 flushContent() 立即取最新 HTML（见 TiptapEditor.vue v21.85 注释）
const editorRef = ref(null)

function checkMobile() { isMobile.value = window.innerWidth <= 768 }
// v21.179：图片查看器打开入口（查看态 + 编辑内预览共用）——从点击位置反查
// 当前容器内全部图片（文档序）→ 以点击那张为起点打开查看器（可 ←→ / 缩略图切换）
function openImageViewer(e) {
  const img = e.target.closest('img')
  if (!img || !img.src) return
  const container = e.currentTarget
  const imgs = Array.from(container.querySelectorAll('img')).filter(i => i.src)
  const idx = imgs.indexOf(img)
  if (idx < 0) return
  imageViewerList.value = imgs.map(i => i.src)
  imageViewerIndex.value = idx
  imageViewerVisible.value = true
}

// ---- v21.113：预览/阅读态任务清单勾选 ----
// 预览为 v-html 静态渲染（无编辑器实例），勾选走 DOMParser 补丁：按文档序定位
// 第 N 个 taskItem → 翻转 data-checked/checked → 回写 content → markDirty 走
// 既有自动保存链路（防抖/加密密码注入/409 冲突处理全复用，不另起炉灶）。
function onPreviewCheckboxToggle(e) {
  const input = e.target
  if (!input || input.type !== 'checkbox' || !currentNote.value) return
  const item = input.closest('li[data-type="taskItem"]')
  if (!item || !e.currentTarget.contains(item)) return
  const items = e.currentTarget.querySelectorAll('ul[data-type="taskList"] li[data-type="taskItem"]')
  const idx = Array.prototype.indexOf.call(items, item)
  if (idx < 0) return
  const checked = !!input.checked
  const next = toggleTaskItemInHtml(currentNote.value.content, idx, checked)
  if (next === null) {
    // 补丁失败（内容与 DOM 不同步等）：回滚 UI，避免视觉与存储不一致
    input.checked = !checked
    return
  }
  currentNote.value.content = next
  markDirty()
}

// v21.86：笔记列表侧栏宽度（拖动分割线调整，localStorage 持久化）
const noteSidebarWidth = ref(parseInt(localStorage.getItem('note_sidebar_width'), 10) || 320)
function persistNoteSidebarWidth() {
  localStorage.setItem('note_sidebar_width', String(noteSidebarWidth.value))
}

// ---- composables ----
const { notes, recentNotes, loading, listError, loadingMore, total, hasMore, loadMoreNotes, loadNotes, loadTags, createNewNote: _create, selectNote: _select, openNoteFromQuery, removeNote: _remove, applySavedToLists, syncNoteInList } = useNotes(currentNote, currentTagInput)

// ---- 滚动触底续载（2026-10-15 用户需求：默认 20 条 + 触底加载后 20 条）----
// 触底判定留 60px 提前量；重入/无更多/首页在途的闸门都在 loadMoreNotes 内部。
const noteListRef = ref(null)
function onListScroll() {
  const el = noteListRef.value
  if (!el) return
  if (el.scrollTop + el.clientHeight < el.scrollHeight - 60) return
  loadMoreNotes()
}

// 标签筛选入口已按需求隐藏（activeTag 恒为空，仅作 loadNotes 参数占位保留）
const { searchQuery, activeTag, allTags, showShortQueryHint, debouncedSearch } = useNoteSearch((q, tag) => loadNotes(q, tag, folderStore.listParam))

const isDirty = ref(false)
// v21.46：useNoteEncrypt 提前声明——useAutoSave 需以回调注入加密密码
// （箭头函数延迟求值，但先声明 encryptPassword 避免 TDZ 与 ESLint 未定义告警）
const { encryptUnlocked, encryptPassword, pwdDialogVisible, pwdDialogMode, pwdDialogTitle, pwdPlaceholder, pwdInput, pwdConfirm, pwdLoading, showEncryptDialog, showPasswordDialog, handlePwdSubmit: _pwdSubmit, downloadNote, resetOnNoteChange } = useNoteEncrypt(currentNote, syncNoteInList, () => exportNoteAsImage())

// v21.85：保存前强制同步编辑器防抖窗口内的最新内容（回调注入模式同 v21.46 getEncryptPassword）
const ensureFreshContent = () => {
  if (editorRef.value && typeof editorRef.value.flushContent === 'function') {
    return editorRef.value.flushContent()
  }
  return null
}

const { saveStatus, markDirty, saveBeforeSwitch, checkDraft, clearDraft, clearSaveState, saveConflict, syncBaselineContent, resetBaseline, handleKeydown, handleBeforeUnload, cleanupTimers } = useAutoSave(currentNote, currentTagInput, isDirty, applySavedToLists, () => encryptPassword.value, ensureFreshContent)

const { fileInput, handleImportFile: _import } = useNoteImport()

// ---- 笔记正文消毒 hook 已迁至 @/utils/sanitize（幂等注册）----

const sanitizedContent = computed(() => sanitizeNoteHtml(currentNote.value?.content))

// 编辑内预览（v21.52 P7 审计修复）：此前 v-html="currentNote.content" 直接渲染
// 未经 DOMPurify 的编辑中内容（与 IndexedDB 草稿同源），恶意 HTML 有 DOM XSS 风险。
// 统一走与正常查看一致的 sanitizer。
const previewSanitizedContent = computed(() => sanitizedContent.value)

// 2.4.0 阅读态大纲：仅从已消毒 HTML 提取标题，点击按索引滚动到对应 h1~h3。
const noteOutline = computed(() => extractNoteOutline(sanitizedContent.value))

function scrollToOutlineHeading(index) {
  const root = viewContentRef.value
  if (!root) return
  const headings = root.querySelectorAll('h1, h2, h3')
  const target = headings[index]
  if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// 2.6.3 阅读态双滚动：大纲滚动条滚动时显示，停止 3 秒后隐藏。
const outlineScrolling = ref(false)
let outlineScrollTimer = null

function onOutlineScroll() {
  outlineScrolling.value = true
  if (outlineScrollTimer) clearTimeout(outlineScrollTimer)
  outlineScrollTimer = setTimeout(() => {
    outlineScrolling.value = false
    outlineScrollTimer = null
  }, 3000)
}

function onOutlineWheel(e) {
  const el = e.currentTarget
  if (!el) return
  if (shouldBlockOutlineWheel(e.deltaY, el.scrollTop, el.clientHeight, el.scrollHeight)) {
    e.preventDefault()
  }
}

// v21.220：预览态代码块复制按钮——查看态（.view-content）与编辑内预览（.note-preview-content）
// 都是 v-html 静态渲染（无编辑器实例），内容每次变更后重挂（attachCodeCopyButtons 幂等去重）。
// 编辑态对应实现在 TiptapEditor 内 CodeBlockCopy 扩展（widget decoration），两态同款按钮同款交互。
const viewContentRef = ref(null)
const notePreviewContentRef = ref(null)

// ---- 2.5.0 版本历史 ----
const historyDialogVisible = ref(false)
// ---- 2.6.0 分享链接 ----
const shareDialogVisible = ref(false)

function onHistoryRestored(note) {
  if (!note || !currentNote.value) return
  currentNote.value = { ...currentNote.value, ...note }
  applySavedToLists(note.id, note.title, note.content, note.tags, note.version, note.updated_at)
  resetBaseline()
  isDirty.value = false
  clearSaveState()
}

function attachPreviewCodeCopy() {
  nextTick(() => {
    attachCodeCopyButtons(viewContentRef.value)
    attachCodeCopyButtons(notePreviewContentRef.value)
  })
}

watch([sanitizedContent, previewSanitizedContent], attachPreviewCodeCopy)

// 列表预览：FTS 搜索命中时渲染 <mark> 高亮片段（P2-2），否则用纯文本摘要
// v21.85：按 id+updated_at 记忆化——stripHtml 是全文 DOMParser 解析，
// 保存后列表重排重渲染时不再对每个可见项重复解析（内容变更必经保存，
// 保存必更新 updated_at，缓存键天然失效）
const previewCache = new Map()
function noteHighlight(note) {
  const key = note.id + '|' + (note.updated_at || '')
  const hit = previewCache.get(key)
  if (hit !== undefined) return hit
  const html = note.highlight
    ? sanitizeHighlight(note.highlight)
    : stripHtml(note.content)
  if (previewCache.size >= 300) previewCache.clear()
  previewCache.set(key, html)
  return html
}

// ---- 业务方法（组合 composables）----
// v21.86：当前选中文件夹为自定义文件夹时，新建笔记归属该文件夹；
// 'all'（全部笔记）与 'default'（我的笔记）均落默认文件夹
const createFolderParam = computed(() => {
  const v = folderStore.activeFolderId
  return typeof v === 'number' ? v : null
})

async function createNewNote() {
  // v21.106 P1-4：新建前保护正在编辑的笔记——此前直接覆盖 currentNote，
  // 未保存内容丢失；加密笔记无本地草稿即永久丢失。
  ensureFreshContent()
  if (isDirty.value && currentNote.value) {
    const saved = await saveBeforeSwitch(currentNote.value)
    if (!saved) return
  }
  // v21.107 P2-5：创建失败（_create 返回 null）直接返回——此前失败也会把
  // 当前预览中的笔记误切进编辑态
  const created = await _create(createFolderParam.value)
  if (!created) return
  resetBaseline(currentNote.value)
  isDirty.value = false
  editMode.value = true
  previewMode.value = false
  sidebarOpen.value = false
  clearSaveState()
}

async function handleImportFile(e) {
  // v21.106 P1-4：导入前同样先保存当前编辑内容（失败/放弃则中止导入并还原 file input）
  ensureFreshContent()
  if (isDirty.value && currentNote.value) {
    const saved = await saveBeforeSwitch(currentNote.value)
    if (!saved) {
      e.target.value = ''
      return
    }
  }
  // 导入失败（_import 返回 null）不得切换编辑态——此前无条件置 editMode=false，
  // 会把用户从编辑中的笔记踢出去；成功才收口。
  //
  // ⭐ v21.213（211b 实机：导入的笔记一律落到「我的笔记」）：传入当前所在文件夹
  // `folderStore.listParam`（'all' 时为 undefined，正是「不指定文件夹」语义）。
  // 桌面端经 FormData 的 folder_id 落到 Go 侧；Web 端忽略该字段（行为不变）。
  const imported = await _import(e, notes, currentNote, currentTagInput, folderStore.listParam)
  if (!imported) return
  resetBaseline(currentNote.value)
  isDirty.value = false
  editMode.value = false
  sidebarOpen.value = false
  clearSaveState()
}

async function selectNote(note) {
  // v21.85：先强制同步编辑器防抖窗口内的内容——刚输入就切换笔记时
  // isDirty 才能正确置位，切换前保存不漏最后几个字
  ensureFreshContent()
  if (isDirty.value && currentNote.value) {
    // v21.106 P1-1：saveBeforeSwitch 内部已按失败分类给出明确提示与「放弃/留下」选择，
    // 此处只负责阻止切换，不再叠加笼统的「请检查网络」文案
    const saved = await saveBeforeSwitch(currentNote.value)
    if (!saved) return
  }
  await _select(note)
  resetBaseline(currentNote.value)
  isDirty.value = false
  editMode.value = false
  if (isMobile.value) sidebarOpen.value = false
  clearSaveState()
  await checkDraft(note)
}

function enterEditMode() { editMode.value = true; previewMode.value = false }

// 标签编辑输入框已按需求隐藏；currentTagInput 仍参与保存链路（useNotes/useAutoSave 引用）

// v21.85：切预览前同步防抖窗口内容（编辑器随即被 v-if 卸载；
// 编辑器 onDestroy 亦会兜底 flush，此处保证预览读到的必然是最新内容）
function togglePreview() {
  if (!previewMode.value) ensureFreshContent()
  previewMode.value = !previewMode.value
}

async function quickEditNote(note) {
  await selectNote(note)
  editMode.value = true
  if (isMobile.value) sidebarOpen.value = false
}

async function removeNote() {
  if (!currentNote.value) return
  const noteId = currentNote.value.id
  const ok = await _remove(noteId)
  if (ok) {
    await clearDraft(noteId)
    await loadNotes(searchQuery.value, activeTag.value, folderStore.listParam)
    allTags.value = await loadTags()
    // v21.86：删除笔记影响文件夹计数（回收站不占 active 计数）
    folderStore.loadFolders()
    // v21.107 P2-4：删除当前笔记后清掉 URL 残留的 note_id（否则刷新重报错一次）
    if (route.query.note_id) {
      const q = { ...route.query }
      delete q.note_id
      delete q.edit
      router.replace({ query: q })
    }
  }
}

// 预览标题行【更多】下拉命令分发：加密/解密/移动到/复制到/删除/下载/导出长图
function handleViewAction(cmd) {
  if (cmd === 'encrypt') showEncryptDialog()
  else if (cmd === 'decrypt') showPasswordDialog('decrypt')
  else if (cmd === 'move' || cmd === 'copy') openFolderPicker(cmd)
  else if (cmd === 'history') historyDialogVisible.value = true
  else if (cmd === 'share') shareDialogVisible.value = true
  else if (cmd === 'delete') confirmRemoveNote()
  else if (cmd === 'download') {
    // 加密且未解锁时，下载前需先验证密码（与旧版行为一致）
    if (currentNote.value?.is_encrypted && !encryptUnlocked.value) showPasswordDialog('download')
    else downloadNote()
  } else if (cmd === 'export-image') {
    // v2.2.2 导出长图：前端渲染需明文在内存，加密未解锁先走口令验证（image 分支解锁后回跳本函数）
    if (currentNote.value?.is_encrypted && !encryptUnlocked.value) showPasswordDialog('image')
    else exportNoteAsImage()
  }
}

// ---- v2.2.2 导出为长图 ----
// 纯前端渲染（离屏海报 → SnapDOM → PNG），不落盘、不占配额、不进备份

/**
 * 采集预览态实测宽度（v2.2.2 R3）：表格每列宽 / 每张图宽 / 正文容器宽。
 * 用途：海报固定 800px 宽，需按「预览 → 海报」的宽度比例等比投影，
 * 否则未拖过的表格列（只有 min-width，靠剩余空间撑开）在海报里会塌成竖排单字，
 * 图片也会因相对占比变化而显得变大（用户 R2 fpk 实测反馈）。
 * 匹配口径：两处容器内的表格/图片均来自同一份消毒后内容，文档顺序一一对应。
 * @returns {{contentWidth:number, tables:number[][], images:number[]}|null}
 */
function collectPreviewMetrics() {
  const el = viewContentRef.value
  if (!el || !el.clientWidth) return null
  // 注意：tables/images 数组**按文档顺序保留占位**（量测不到写 null），
  // 与海报容器一一对应；此处不可 filter 掉无效项，否则索引错位会把宽度套到别的表上。
  const tables = Array.from(el.querySelectorAll('table'))
    .map((t) => {
      const row = t.querySelector('tr')
      if (!row) return null
      const w = Array.from(row.children).map((c) => c.getBoundingClientRect().width)
      return w.length && w.every((n) => n > 0) ? w : null
    })
  const images = Array.from(el.querySelectorAll('img')).map((i) => i.getBoundingClientRect().width)
  return { contentWidth: el.clientWidth, tables, images }
}

async function exportNoteAsImage() {
  if (!currentNote.value?.content) {
    message.warning('笔记内容为空，无需导出')
    return
  }
  const loading = ElLoading.service({
    lock: true,
    text: '正在生成长图，长笔记可能需要几秒…',
    background: 'rgba(0, 0, 0, 0.35)'
  })
  try {
    // 确保预览 DOM 已按最新内容渲染后再量测（加密笔记口令解锁后内容刚注入）
    await nextTick()
    const previewMetrics = collectPreviewMetrics()
    const r = await exportNotePoster(currentNote.value, { previewMetrics })
    if (r.sizeMB > 20) message.success(`长图已导出（${r.sizeMB} MB，文件较大）`)
    else message.success(`长图已导出：${r.filename}`)
    // 捕获警告（如分窗降级、图片占位）仅记控制台，不产出半截图口径由失败分支兜底
    if (r.warnings.length) console.warn('[notePoster] capture warnings:', r.warnings)
  } catch (e) {
    console.error('[notePoster] export failed:', e)
    const msg = String(e?.message || '')
    message.error(
      msg.includes('CompressionStream')
        ? '当前浏览器内核较旧，不支持长图分窗编码，请升级浏览器后重试'
        : '导出长图失败，请重试'
    )
  } finally {
    loading.close()
  }
}

// ---- v21.87 移动到/复制到 ----
const folderPickerVisible = ref(false)
const folderPickerTitle = ref('')
const folderPickerLoading = ref(false)
let folderPickMode = 'move' // move | copy

function openFolderPicker(mode) {
  folderPickMode = mode
  folderPickerTitle.value = mode === 'move' ? '移动到' : '复制到'
  folderPickerVisible.value = true
}

async function handleFolderPick(folderId) {
  if (!currentNote.value) return
  // v2.3.0：目标是锁定文件夹时先解锁，取消则不移动/复制
  if (folderId !== null && folderId !== undefined) {
    const target = folderStore.folders.find(x => x.id === folderId)
    if (target && folderStore.isFolderLocked(target)) {
      folderSecurity.openUnlock(target, () => {
        folderPickerVisible.value = false
        handleFolderPick(folderId)
      })
      return
    }
  }
  const id = currentNote.value.id
  folderPickerLoading.value = true
  try {
    if (folderPickMode === 'move') {
      // P2 批次1（§57）：移动推进服务端 version——同步响应中的新 version，
      // 避免持有该笔记编辑态时下次保存用旧基点误报 409
      const resp = await moveNoteToFolder(id, folderId)
      message.success('移动成功')
      // v21.107 P1-7：同步当前笔记归属——此前不更新，再次打开「移动到/复制到」
      // 弹框默认勾选的还是旧文件夹，movedAway 判断也基于过期值
      if (currentNote.value && currentNote.value.id === id) {
        const newVersion = resp?.data?.version
        currentNote.value = {
          ...currentNote.value,
          folder_id: folderId,
          ...(Number.isFinite(newVersion) ? { version: newVersion } : {})
        }
      }
      // 笔记被移出当前文件夹视图 → 清除当前选中（回到欢迎页）
      const cur = folderStore.activeFolderId
      if (cur !== 'all') {
        const movedAway = cur === 'default' ? folderId !== null : cur !== folderId
        if (movedAway) currentNote.value = null
      }
    } else {
      await copyNoteToFolder(id, folderId)
      message.success('复制成功')
    }
    folderPickerVisible.value = false
    await loadNotes(searchQuery.value, activeTag.value, folderStore.listParam)
  } catch (e) {
    message.error(e.response?.data?.error || (folderPickMode === 'move' ? '移动失败' : '复制失败'))
  } finally {
    folderPickerLoading.value = false
  }
}

// 下拉菜单内不便嵌套 popconfirm，删除确认改为弹窗
async function confirmRemoveNote() {
  const ok = await message.confirm('确定删除这个笔记吗？', {
    title: '删除笔记',
    confirmButtonText: '删除',
    cancelButtonText: '取消',
    type: 'warning',
    danger: true
  })
  if (ok) removeNote()
}


async function handlePwdSubmit() {
  await _pwdSubmit(enterEditMode)
}

function loadAllNotes() { router.push('/notes-list') }

// ---- 生命周期 ----
// v21.106 P1-3：跨路由离开统一为「先保存再离开」——与应用内切换笔记同策略
// （此前只弹「未保存确定离开吗」、确认即丢弃，与 selectNote 的切换前保存双标）。
// saveBeforeSwitch 成功→直接放行；失败→其内部已弹分类原因 +「放弃并继续/留下重试」，
// 用户选择留下则取消导航。
onBeforeRouteLeave(async (to, from, next) => {
  if (!currentNote.value) return next()
  ensureFreshContent()
  if (!isDirty.value) return next()
  const ok = await saveBeforeSwitch(currentNote.value)
  return ok ? next() : next(false)
})

const notesRootRef = ref(null)
let stopAutoContrast = null
let stopSyncRefresh = null

onMounted(async () => {
  checkMobile()
  window.addEventListener('resize', checkMobile)
  window.addEventListener('keydown', handleKeydown)
  window.addEventListener('beforeunload', handleBeforeUnload)
  // v21.104（方案 C）：目标笔记与文件夹/列表/标签并行加载，消除「欢迎回来」空态闪现。
  // openingNote 初值已按 route.query.note_id 置位；此处并行发起，finally 统一收口防骨架屏卡死。
  const noteId = route.query.note_id
  try {
    await Promise.all([
      noteId ? openNoteFromQuery(noteId) : Promise.resolve(null),
      folderStore.loadFolders(),
      loadNotes('', '', folderStore.listParam),
      loadTags().then((tags) => { allTags.value = tags })
    ])
  } finally {
    openingNote.value = false
  }
  // v21.107 P2-4：深链指向不存在/已删笔记——打开失败时清掉 URL 残留，避免停在错误态链接上
  if (noteId && !currentNote.value) {
    const q = { ...route.query }
    delete q.note_id
    delete q.edit
    router.replace({ query: q })
  }
  // v21.107 P1-11：?edit=1 意图参数（移动端列表「新建笔记」直达编辑态，与 PC 行为对齐）
  if (currentNote.value && route.query.edit === '1') {
    editMode.value = true
    previewMode.value = false
    const q = { ...route.query }
    delete q.edit
    router.replace({ query: q })
  }
  // v21.106 P1-8：深链/移动端入口同样触发草稿恢复提示
  // （此前 checkDraft 仅挂在 selectNote 上，从 /notes-list 点卡片或 ?note_id= 直达永不提示）
  if (noteId && currentNote.value && String(currentNote.value.id) === String(noteId)) {
    resetBaseline(currentNote.value)
    await checkDraft(currentNote.value)
  }
  // v21.118 评审修复（问题①）：暗色下按 WCAG 4.5:1 保色相提亮低对比文字（仅显示层，不改存储/导出）
  await nextTick()
  stopAutoContrast = installAutoContrast(notesRootRef.value)
  // v21.153（R-1 通知接线）：桌面后台同步每完成一轮 → 重载列表与文件夹。
  // 背景：首登自动 Bootstrap 换库完成后前端零通知，列表/侧栏停留空态直到重启（192 真机实证）。
  // isDirty 时跳过本轮避免打断编辑（下一轮自然补上）；Web 构建下 install 内部自guards。
  if (IS_DESKTOP) {
    stopSyncRefresh = installSyncRefreshPolling(() => {
      if (isDirty.value) return
      folderStore.loadFolders()
      loadNotes(searchQuery.value, activeTag.value, folderStore.listParam)
    })
  }
})

onUnmounted(() => {
  window.removeEventListener('resize', checkMobile)
  window.removeEventListener('keydown', handleKeydown)
  window.removeEventListener('beforeunload', handleBeforeUnload)
  if (stopAutoContrast) { stopAutoContrast(); stopAutoContrast = null }
  if (stopSyncRefresh) { stopSyncRefresh(); stopSyncRefresh = null }
  if (outlineScrollTimer) { clearTimeout(outlineScrollTimer); outlineScrollTimer = null }
  cleanupTimers()
})

watch(() => route.query.note_id, async (newId) => {
  if (!newId) return
  if (currentNote.value && String(newId) === String(currentNote.value.id)) return
  // v21.106 P1-2：同路由 query 变更不会触发 onBeforeRouteLeave，此前 watch 直接
  // openNoteFromQuery 覆盖 currentNote——浏览器前进/后退/改地址栏切换笔记会
  // 静默丢弃未保存内容。统一补「先保存后切换」；用户选择留下时把 URL 拨回当前笔记。
  ensureFreshContent()
  if (isDirty.value && currentNote.value) {
    const saved = await saveBeforeSwitch(currentNote.value)
    if (!saved) {
      router.replace({ query: { ...route.query, note_id: String(currentNote.value.id) } })
      return
    }
  }
  await nextTick()
  openingNote.value = true
  try {
    await Promise.all([
      openNoteFromQuery(newId),
      loadNotes(searchQuery.value, activeTag.value, folderStore.listParam),
      loadTags().then((tags) => { allTags.value = tags })
    ])
  } finally {
    openingNote.value = false
  }
  clearSaveState()
  // v21.106 P1-8：URL 直达路径同样触发草稿恢复提示
  if (currentNote.value && String(currentNote.value.id) === String(newId)) {
    resetBaseline(currentNote.value)
    await checkDraft(currentNote.value)
  }
})

// v21.106 P1-1：409 冲突处理对话框——自动保存撞冲突后重试必再失败（本地 version 过期），
// 给用户一条明确出路：「重新加载服务器版本」（拉最新数据，未保存内容经本地草稿可恢复；
// 加密笔记无草稿则明示将丢失）或「继续编辑」（暂停自动保存，Ctrl+S 仍可手动重试）。
watch(saveConflict, (v) => {
  if (!v || !currentNote.value) return
  const enc = !!currentNote.value.is_encrypted
  ElMessageBox.confirm(
    enc
      ? '该笔记已在其他设备被修改，本地改动无法保存（继续重试也会冲突）。加密笔记没有本地草稿：重新加载将丢失当前未保存内容。'
      : '该笔记已在其他设备被修改，本地改动无法保存（继续重试也会冲突）。选择「重新加载」将获取服务器最新版本，你的未保存内容保留在本地草稿中，加载后可选择恢复。',
    '保存冲突',
    { confirmButtonText: '重新加载服务器版本', cancelButtonText: '继续编辑', type: 'warning' }
  ).then(async () => {
    saveConflict.value = false
    isDirty.value = false
    editMode.value = false
    const id = currentNote.value.id
    await _select(currentNote.value)
    if (currentNote.value && String(currentNote.value.id) === String(id)) {
      resetBaseline(currentNote.value)
      if (!currentNote.value.is_encrypted) await checkDraft(currentNote.value)
    }
  }).catch(() => { /* 继续编辑：自动保存已暂停，Ctrl+S 可再次尝试并重新弹出处理对话框 */ })
})

// v21.86：切换文件夹 → 按当前文件夹重新拉取笔记列表（搜索词与标签条件保留）
watch(() => folderStore.activeFolderId, async () => {
  await loadNotes(searchQuery.value, activeTag.value, folderStore.listParam)
})

// v2.3.0：文件夹加密/解密/解锁后刷新列表与标签（锁定笔记立即从「全部笔记」消失）
watch(() => folderStore.notesRefreshTick, async () => {
  await loadNotes(searchQuery.value, activeTag.value, folderStore.listParam)
  loadTags()
})

// v2.3.0：正在查看的笔记所属文件夹被锁定 → 立即关闭，避免继续展示已隐藏内容
watch(() => folderStore.lockSignal, (sig) => {
  if (!sig || !currentNote.value) return
  if (String(currentNote.value.folder_id ?? '') !== String(sig.folderId)) return
  isDirty.value = false
  editMode.value = false
  currentNote.value = null
  clearSaveState()
  message.warning('该文件夹已加密，当前笔记已关闭')
})

// v21.88：移动端 LOGO/便签点击（homeTick 自增）→ 关闭当前打开的笔记回到列表态。
// 复用 selectNote 的切换前保存逻辑（isDirty 时先保存，失败则阻止关闭防丢数据）。
watch(() => folderStore.homeTick, async () => {
  if (!currentNote.value) return
  ensureFreshContent()
  if (isDirty.value) {
    // v21.106 P1-1：saveBeforeSwitch 内部已分类提示并给用户「放弃/留下」选择
    const saved = await saveBeforeSwitch(currentNote.value)
    if (!saved) return
  }
  isDirty.value = false
  editMode.value = false
  currentNote.value = null
  clearSaveState()
})

watch(() => currentNote.value?.id, () => {
  resetOnNoteChange()
  outlineScrolling.value = false
  if (outlineScrollTimer) { clearTimeout(outlineScrollTimer); outlineScrollTimer = null }
})

  return {
    router, route, folderStore, folderSecurity, customFolders,
    currentNote, currentTagInput, editMode, previewMode, isMobile, sidebarOpen, sidebarCollapsed,
    imageViewerVisible, imageViewerList, imageViewerIndex, openingNote, editorRef,
    checkMobile, openImageViewer, onPreviewCheckboxToggle,
    noteSidebarWidth, persistNoteSidebarWidth,
    notes, recentNotes, loading, listError, loadingMore, total, hasMore, loadMoreNotes, loadNotes, loadTags,
    noteListRef, onListScroll,
    searchQuery, activeTag, allTags, showShortQueryHint, debouncedSearch,
    isDirty, encryptUnlocked, encryptPassword, pwdDialogVisible, pwdDialogMode, pwdDialogTitle,
    pwdPlaceholder, pwdInput, pwdConfirm, pwdLoading, showEncryptDialog, showPasswordDialog, downloadNote,
    resetOnNoteChange, ensureFreshContent,
    saveStatus, markDirty, saveBeforeSwitch, checkDraft, clearDraft, clearSaveState, saveConflict,
    syncBaselineContent, resetBaseline, handleKeydown, handleBeforeUnload, cleanupTimers,
    fileInput, sanitizedContent, previewSanitizedContent,
    noteOutline, scrollToOutlineHeading, outlineScrolling, onOutlineScroll, onOutlineWheel,
    historyDialogVisible, onHistoryRestored,
    shareDialogVisible,
    viewContentRef, notePreviewContentRef, noteHighlight,
    createNewNote, handleImportFile, selectNote, enterEditMode, togglePreview, quickEditNote,
    removeNote, handleViewAction, exportNoteAsImage,
    folderPickerVisible, folderPickerTitle, folderPickerLoading, handleFolderPick,
    confirmRemoveNote, handlePwdSubmit, loadAllNotes, notesRootRef
  }
}
