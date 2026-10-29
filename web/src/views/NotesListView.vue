<!--
  文件行数豁免登记（开发纪律 §5.1）
  ─────────────────────────────────────────────────────────────
  当前行数：1359（.vue 软上限 800，超出 69%）
  豁免原因：笔记列表视图承担列表/搜索/排序/筛选/多选/批量操作/拖拽/
           移动端适配等多重职责，功能耦合度较高；纯重构轮拆分风险
           大于收益（历史 v21.121~123 拆分时因样式遗漏引发 v21.125
           工具栏损毁事故）。
  拆分计划：下次涉及本文件的功能性改动时同步拆分，候选切分为
           NoteListHeader / NoteListItem / NoteListSidebar 三个子组件，
           保持状态在父组件，通过 v-model 与 emits 通信。
  追踪：docs/技术债清单.md · TD-001
-->
<template>
  <div class="notes-list-container">
    <div class="notes-list-header">
      <div class="header-left">
        <h1 class="page-title">
          <el-icon><Document /></el-icon>
          <!-- v21.208：标题跟随当前文件夹（点击顶栏应用名进入时为「全部笔记」） -->
          <span>{{ nlFolderLabel }}</span>
        </h1>
        <!-- v21.89 文件夹选择（用户确认：移动端点 LOGO 跳此页，文件夹选项在标题旁）。
             项目标准边框样式：10px 圆角 + var(--border-light)，与页面其他控件一致 -->
        <el-dropdown
          trigger="click"
          class="nl-folder-dropdown"
          @command="handleFolderChange"
        >
          <button
            class="nl-folder-trigger"
            :title="nlFolderLabel"
            :aria-label="nlFolderLabel"
          >
            <el-icon><Folder /></el-icon>
            <el-icon class="nl-folder-caret">
              <ArrowDown />
            </el-icon>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="all">
                <!-- v21.97：图标对齐 PC 端侧栏（全部笔记=Files） -->
                <el-icon class="nl-f-new-icon">
                  <Files />
                </el-icon>
                <span class="nl-f-item">全部笔记</span>
                <el-icon
                  v-if="folderStore.activeFolderId === 'all'"
                  class="nl-f-check"
                >
                  <Check />
                </el-icon>
              </el-dropdown-item>
              <!-- v21.96 需求：移动端新建文件夹入口，菜单放置在「全部笔记」下方 -->
              <el-dropdown-item
                command="__new_folder__"
                divided
              >
                <el-icon class="nl-f-new-icon">
                  <Plus />
                </el-icon>
                <span class="nl-f-item">新建文件夹</span>
              </el-dropdown-item>
              <!-- v21.108 P2-10：移动端文件夹管理入口（此前只能新建，建错无法改名/删除/排序） -->
              <el-dropdown-item
                v-if="folderStore.folders.length"
                command="__manage_folders__"
              >
                <el-icon class="nl-f-new-icon">
                  <Setting />
                </el-icon>
                <span class="nl-f-item">管理文件夹</span>
              </el-dropdown-item>
              <el-dropdown-item command="default">
                <!-- v21.97：图标对齐 PC 端侧栏（我的笔记=Document） -->
                <el-icon class="nl-f-new-icon">
                  <Document />
                </el-icon>
                <span class="nl-f-item">我的笔记</span>
                <el-icon
                  v-if="folderStore.activeFolderId === 'default'"
                  class="nl-f-check"
                >
                  <Check />
                </el-icon>
              </el-dropdown-item>
              <el-dropdown-item
                v-for="f in folderStore.customFolders"
                :key="f.id"
                :command="f.id"
              >
                <!-- v21.97：图标对齐 PC 端侧栏（自定义文件夹=Folder） -->
                <el-icon class="nl-f-new-icon">
                  <Folder />
                </el-icon>
                <span class="nl-f-item">{{ f.name }}</span>
                <el-icon
                  v-if="folderStore.activeFolderId === f.id"
                  class="nl-f-check"
                >
                  <Check />
                </el-icon>
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <!-- v21.96 需求：移动端新建文件夹弹窗（与 PC 端 AppSidebar 同款交互，maxlength 32） -->
        <el-dialog
          v-model="newFolderDialogVisible"
          title="新建文件夹"
          :width="isMobile ? '85%' : '400px'"
          append-to-body
        >
          <el-input
            v-model="newFolderName"
            placeholder="请输入文件夹名称"
            maxlength="32"
            show-word-limit
            clearable
            @keyup.enter="submitNewFolder"
          />
          <template #footer>
            <el-button @click="newFolderDialogVisible = false">
              取消
            </el-button>
            <el-button
              type="primary"
              :loading="newFolderLoading"
              @click="submitNewFolder"
            >
              确定
            </el-button>
          </template>
        </el-dialog>
        <!-- v21.108 P2-10：移动端文件夹管理弹窗（重命名/删除/上下移动，复用 folderStore 全套方法） -->
        <el-dialog
          v-model="manageFoldersVisible"
          title="管理文件夹"
          :width="isMobile ? '92%' : '480px'"
          append-to-body
        >
          <div
            v-if="!folderStore.folders.length"
            style="color:var(--el-text-color-secondary);text-align:center;padding:12px 0"
          >
            暂无自定义文件夹
          </div>
          <div
            v-for="(f, i) in folderStore.folders"
            :key="f.id"
            class="mf-row"
            style="display:flex;align-items:center;gap:6px;padding:8px 0;border-bottom:1px solid var(--el-border-color-lighter)"
          >
            <span
              class="mf-name"
              style="flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap"
            >{{ f.name }}</span>
            <el-button
              size="small"
              text
              :disabled="i === 0"
              @click="folderStore.moveFolderTo(f.id, 'up')"
            >
              上移
            </el-button>
            <el-button
              size="small"
              text
              :disabled="i === folderStore.folders.length - 1"
              @click="folderStore.moveFolderTo(f.id, 'down')"
            >
              下移
            </el-button>
            <el-button
              size="small"
              text
              @click="renameFolder(f)"
            >
              重命名
            </el-button>
            <el-button
              size="small"
              text
              type="danger"
              @click="removeFolderConfirm(f)"
            >
              删除
            </el-button>
          </div>
          <template #footer>
            <el-button
              type="primary"
              @click="manageFoldersVisible = false"
            >
              完成
            </el-button>
          </template>
        </el-dialog>
        <span class="notes-count">共 {{ total }} 条笔记</span>
      </div>
      <div class="header-right">
        <input
          ref="fileInput"
          type="file"
          accept=".md,.txt,.html,.htm"
          style="display:none"
          @change="handleImportFile"
        >
        <el-button
          class="import-btn"
          :icon="Upload"
          @click="$refs.fileInput.click()"
        >
          <span>导入</span>
        </el-button>
        <el-button
          type="primary"
          class="create-btn"
          :icon="Plus"
          :loading="creating"
          @click="createNewNote"
        >
          <span>新建笔记</span>
        </el-button>
        <el-button
          class="batch-btn"
          :icon="Tickets"
          @click="toggleBatchMode"
        >
          <span>{{ batchMode ? '取消' : '批量' }}</span>
        </el-button>
        <el-button
          class="export-all-btn"
          :icon="Download"
          :loading="exportingAll"
          @click="exportAll"
        >
          <span>导出全部</span>
        </el-button>
      </div>
    </div>

    <!-- v21.166 计划项2（X5 延伸）：换代/全量同步窗口提示。
         首登 Bootstrap 换代 10~60s，期间列表可能暂空 —— 明确告知"在同步、别重复操作"，
         避免用户误判为 205 刚修的"首装列表恒空"复发。组件自判桌面端，Web 端不渲染。 -->
    <SyncInitBanner />

    <div class="notes-list-toolbar">
      <div class="search-box">
        <el-icon class="search-icon">
          <Search />
        </el-icon>
        <el-input
          v-model="searchQuery"
          placeholder="搜索笔记..."
          clearable
          class="search-input"
          @input="debouncedSearch"
        />
        <!-- v18.8: 短关键词提示（<3 字走 LIKE 模糊匹配，与全文索引行为不同） -->
        <div
          v-if="showShortQueryHint"
          class="short-query-hint"
        >
          少于 3 个字：使用模糊匹配，结果可能较多
        </div>
      </div>
      
      <div class="filter-group">
        <!-- 标签筛选下拉已按需求于前台隐藏 -->

        <div class="sort-filter">
          <el-select
            v-model="sortBy"
            placeholder="排序方式"
            class="sort-select"
            @change="loadNotes"
          >
            <el-option
              label="最近修改"
              value="updated_at"
            />
            <el-option
              label="最近创建"
              value="created_at"
            />
            <el-option
              label="标题A-Z"
              value="title"
            />
          </el-select>
        </div>
      </div>
    </div>

    <!-- v20.7 批量操作栏 -->
    <div
      v-if="batchMode"
      class="batch-bar"
    >
      <el-button
        size="small"
        text
        @click="selectAll"
      >
        {{ selectedIds.size === notes.length ? '取消全选' : '全选' }}
      </el-button>
      <span
        v-if="selectedIds.size > 0"
        class="batch-count"
      >已选 {{ selectedIds.size }} 篇</span>
      <span
        v-else
        class="batch-hint"
      >点击笔记卡片选择</span>
      <el-button
        size="small"
        type="primary"
        :icon="Download"
        :disabled="selectedIds.size === 0"
        :loading="batchExporting"
        @click="batchExport"
      >
        导出
      </el-button>
      <el-button
        size="small"
        type="danger"
        :icon="Delete"
        :disabled="selectedIds.size === 0"
        @click="batchDelete"
      >
        删除
      </el-button>
    </div>

    <div
      v-if="loading"
      class="loading-container"
    >
      <el-skeleton
        :rows="6"
        animated
      />
    </div>

    <div
      v-else-if="notes.length === 0 && listError"
      class="empty-container"
    >
      <!-- v21.107 P2-3：加载失败错误态（不再显示「还没有笔记+创建引导」误导用户） -->
      <div class="empty-content">
        <h3>笔记加载失败</h3>
        <p>{{ listError }}</p>
        <el-button
          type="primary"
          @click="loadNotes()"
        >
          <span>重试</span>
        </el-button>
      </div>
    </div>

    <div
      v-else-if="notes.length === 0"
      class="empty-container"
    >
      <div class="empty-content">
        <el-icon :size="80">
          <Document />
        </el-icon>
        <h3>{{ searchQuery || activeTag ? '没有找到笔记' : '还没有笔记' }}</h3>
        <p>{{ searchQuery || activeTag ? '试试其他关键词或筛选条件' : '点击上方按钮创建新笔记' }}</p>
        <el-button
          v-if="!searchQuery && !activeTag"
          type="primary"
          :icon="Plus"
          @click="createNewNote"
        >
          <span>创建第一个笔记</span>
        </el-button>
      </div>
    </div>

    <div
      v-else
      class="notes-grid"
    >
      <div 
        v-for="note in notes" 
        :key="note.id" 
        class="note-card"
        :class="{ 'batch-selected': batchMode && selectedIds.has(note.id) }"
        @click="batchMode ? toggleSelect(note.id) : openNote(note)"
      >
        <div class="note-card-header">
          <h3 class="note-title">
            {{ note.title || '无标题' }}<span
              v-if="note.is_encrypted"
              class="lock-mark"
            > 🔒</span>
          </h3>
          <div class="note-actions">
            <el-button
              text
              :icon="Edit"
              @click.stop="openNote(note)"
            />
            <el-button
              text
              :icon="Download"
              @click.stop="exportSingle(note)"
            />
            <el-popconfirm
              title="确定删除这个笔记吗？"
              confirm-button-text="删除"
              cancel-button-text="取消"
              cancel-button-type="default"
              confirm-button-type="danger"
              @confirm.stop="deleteNoteItem(note)"
            >
              <template #reference>
                <el-button
                  text
                  :icon="Delete"
                />
              </template>
            </el-popconfirm>
          </div>
        </div>
        
        <div
          v-if="note.highlight"
          class="note-preview highlight"
          v-html="sanitizeHighlight(note.highlight)"
        />
        <div
          v-else-if="note.is_encrypted"
          class="note-preview locked"
        >
          🔒 已加密，需密码查看
        </div>
        <div
          v-else
          class="note-preview"
        >
          {{ stripHtml(note.content, 100) }}
        </div>
        
        <!-- v21.115（用户需求）：笔记条目内修改时间栏前台隐藏（含已隐藏的标签区，
             footer 整体不再渲染；后端 updated_at/tags 链路全保留） -->
        <div
          v-if="false"
          class="note-footer"
        >
          <div class="note-meta">
            <span class="note-time">
              <el-icon><Clock /></el-icon>
              {{ formatDate(note.updated_at) }}
            </span>
          </div>
          <div
            v-if="false && note.tags"
            class="note-tags"
          >
            <el-tag
              v-for="t in splitTags(note.tags).slice(0, 3)"
              :key="t"
              size="small"
            >
              {{ t }}
            </el-tag>
          </div>
        </div>
      </div>
    </div>

    <!-- 1.0.218 视觉改版（用户参考图）：数字窗三段式 + 「全部」，替换 el-pagination
         （每页条数/跳页/总数移除——总数在页面头部「共 N 条笔记」已可见）。
         allMode 时保留容器以便「分页」按钮可点回来（否则减到单页会困在全部模式）。 -->
    <div
      v-if="notes.length > 0 && (total > pageSize || allMode)"
      class="pagination-container"
    >
      <NotePager
        :total="total"
        :page-size="pageSize"
        :current="currentPage"
        :all-mode="allMode"
        @page-change="onPageChange"
        @toggle-all="onToggleAll"
      />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Document, Search, Edit, Delete, Clock, Tickets, Download, Upload, Folder, Files, ArrowDown, Check, Setting } from '@element-plus/icons-vue'
import { getNotes, createNote, deleteNote, exportNote, exportAllNotes, batchDeleteNotes, batchExportNotes } from '@/api/note'
import SyncInitBanner from '@/components/SyncInitBanner.vue'
import NotePager from '@/components/NotePager.vue'
import { useFolderStore } from '@/stores/folder'
import api from '@/api/request'
import { message } from '@/utils/message'
import DOMPurify from 'dompurify'
import { stripHtml, splitTags, formatDate } from '@/utils/note'
import { importNoteFile } from '@/composables/useNoteImport'
import { IS_DESKTOP } from '@/api/local/transport'
import { exportNoteMD, exportNotesZip } from '@/api/local/desktopApi'

// 渲染搜索高亮片段（FTS5 snippet 返回的带 <mark> 标签 HTML）。只允许 mark 标签防 XSS。
function sanitizeHighlight(html) {
  return DOMPurify.sanitize(html, { ALLOWED_TAGS: ['mark'] })
}

const router = useRouter()

// v21.89 文件夹：与 AppSidebar/MainLayout 共享同一 store；标题旁下拉切换，
// 列表内容跟随 activeFolderId 过滤（后端 folder_id 参数 v21.86 已支持）。
const folderStore = useFolderStore()
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
      const skipped = Number(r?.skipped || 0)
      const base = `已导出 ${r?.exported ?? 0} 篇笔记`
      message.success(skipped > 0 ? `${base}，跳过 ${skipped} 篇加密笔记` : `${base}，已保存到 ${r?.path || '所选位置'}`)
      return
    }
    const resp = await exportAllNotes()
    saveBlob(resp, 'Sticky-note-导出.zip')
    const skipped = resp.headers['x-encrypted-skipped']
    if (skipped && parseInt(skipped) > 0) {
      message.success('导出成功，已跳过 ' + skipped + ' 个加密笔记')
    } else {
      message.success('导出成功，已导出全部笔记')
    }
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
      const skipped = Number(r?.skipped || 0)
      const base = `已导出 ${r?.exported ?? 0} 篇笔记`
      message.success(skipped > 0 ? `${base}，跳过 ${skipped} 篇加密笔记` : `${base}，已保存到 ${r?.path || '所选位置'}`)
      selectedIds.value = new Set()
      batchMode.value = false
      return
    }
    const resp = await batchExportNotes(ids)
    saveBlob(resp, 'Sticky-note-批量导出.zip')
    // v21.58 F-12：按后端 header 提示实际导出数量与跳过的加密笔记数
    const skipped = Number(resp.headers?.['x-encrypted-skipped'] || 0)
    if (skipped > 0) {
      const exported = Number(resp.headers?.['x-exported-count'] || ids.length - skipped)
      message.success(`已导出 ${exported} 篇笔记，跳过 ${skipped} 篇加密笔记`)
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

</script>

<style scoped>
.notes-list-container {
  padding: 24px 32px;
}

.notes-list-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 24px;
  flex-wrap: wrap;
  gap: 16px;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 16px;
}

.page-title {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 26px;
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--text-primary);
  margin: 0;
}

.page-title .el-icon {
  color: #2563eb;
}

/* v21.89 文件夹下拉按钮：项目标准边框（10px 圆角 + var(--border-light)，
   与页面搜索框/下拉控件一致；用户反馈 v21.87 胶囊 999px 样式不一致） */
.nl-folder-trigger {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border-radius: var(--radius-md);
  background: var(--bg-primary);
  border: 1px solid var(--border-light);
  cursor: pointer;
  color: var(--text-secondary);
  font-size: 13px;
  max-width: 180px;
  transition: border-color 0.2s, color 0.2s;
}

.nl-folder-trigger:hover {
  border-color: var(--primary-color);
  color: var(--primary-color);
}


.nl-folder-caret {
  color: var(--text-muted);
  font-size: 12px;
  flex-shrink: 0;
}

/* 下拉项（Teleport 到 body，需全局选择器） */
:global(.nl-f-item) {
  flex: 1;
}

:global(.nl-f-check) {
  color: var(--primary-color);
  margin-left: auto;
}

/* v21.96 需求：新建文件夹菜单项图标（与 PC 端侧栏 + 图标风格一致） */
:global(.nl-f-new-icon) {
  color: var(--primary-color);
  margin-right: 6px;
}

.notes-count {
  font-size: 14px;
  /* v21.97：硬编码亮色改主题变量（暗色自动切换，修复审计/用户反馈） */
  color: var(--text-muted);
  padding: 4px 12px;
  background: var(--bg-secondary);
  border-radius: var(--radius-sm);
}

.header-right {
  display: flex;
  gap: 12px;
}

.export-all-btn {
  border-radius: var(--radius-md);
  font-weight: 600;
  padding: 12px 24px;
  background: var(--bg-primary);
  border-color: var(--border-color);
}

/* v21.164 视觉改版（图1 基线）：按钮统一——「新建笔记」实心主色（去渐变，
   回归 EP type=primary 由 --el-color-primary 驱动），次要操作白底细边框，
   四按钮高度/圆角/字重一致 */
.create-btn {
  border-radius: var(--radius-md);
  font-weight: 600;
  padding: 12px 24px;
  background: var(--primary-color);
  border: none;
}

.import-btn {
  border-radius: var(--radius-md);
  font-weight: 600;
  padding: 12px 24px;
  background: var(--bg-primary);
  border-color: var(--border-color);
}

.batch-btn {
  border-radius: var(--radius-md);
  font-weight: 600;
  padding: 12px 24px;
  background: var(--bg-primary);
  border-color: var(--border-color);
}

.create-btn:hover {
  background: var(--primary-hover);
}

.import-btn:hover,
.export-all-btn:hover,
.batch-btn:hover {
  border-color: var(--primary-color);
  color: var(--primary-color);
}

.notes-list-toolbar {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 24px;
  flex-wrap: wrap;
}

.search-box {
  position: relative;
  flex: 1;
  min-width: 280px;
}

.search-icon {
  position: absolute;
  left: 14px;
  top: 50%;
  transform: translateY(-50%);
  /* v21.82：与主页侧栏笔记列表搜索框一致，使用主题变量——
     原硬编码 #9ca3af 在深色模式下对比失衡 */
  color: var(--text-muted);
  font-size: 18px;
  z-index: 1;
}

/* v21.82 bug 修复：深色模式下搜索框发白——原样式硬编码 #f9fafb/#e5e7eb/#2563eb，
   不随主题切换；现与主页（NotesView）侧栏笔记列表搜索框完全一致，全部走主题变量，
   亮色下取值与原值等价（--bg-secondary=#f9fafb、--border-color=#e5e7eb） */
.search-input :deep(.el-input__wrapper) {
  padding-left: 44px !important;
  border-radius: var(--radius-md);
  box-shadow: 0 0 0 1px var(--border-color);
  background: var(--bg-secondary);
  transition: all 0.3s;
}

.search-input :deep(.el-input__wrapper:hover) {
  box-shadow: 0 0 0 1px var(--primary-color);
  background: var(--bg-primary);
}

.search-input :deep(.el-input__wrapper.is-focus) {
  box-shadow: 0 0 0 2px var(--primary-color);
  background: var(--bg-primary);
}

.short-query-hint {
  position: absolute;
  top: 100%;
  left: 0;
  margin-top: 4px;
  font-size: 12px;
  color: var(--text-muted);
  padding-left: 14px;
}

.filter-group {
  display: flex;
  gap: 12px;
}

.tag-select,
.sort-select {
  width: 160px;
}

.loading-container {
  padding: 40px;
  background: var(--bg-primary);
  border-radius: var(--radius-lg);
  border: 1px solid var(--border-color);
}

/* v21.164：空态容器去重投影，改细边框分区 */
.empty-container {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 400px;
  background: var(--bg-primary);
  border-radius: var(--radius-lg);
  border: 1px solid var(--border-color);
}

.empty-content {
  text-align: center;
  padding: 40px;
}

/* 评审 B2-4：空态文字/图标改用主题变量，暗色模式随 :root 变量自动适配（亮色取值与原硬编码等价） */
.empty-content .el-icon {
  color: var(--text-muted);
  margin-bottom: 24px;
}

.empty-content h3 {
  font-size: 20px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0 0 8px;
}

.empty-content p {
  font-size: 14px;
  color: var(--text-muted);
  margin: 0 0 24px;
}

/* v20.7 批量操作 */
.batch-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 16px;
  margin-bottom: 12px;
  background: var(--bg-tertiary);
  border-radius: var(--radius-lg);
  flex-wrap: wrap;
}
.batch-count {
  font-size: 13px;
  color: var(--text-secondary);
  margin-right: auto;
}
.batch-hint {
  font-size: 13px;
  color: var(--text-muted);
  margin-right: auto;
}
.note-card.batch-selected,
.note-card.batch-selected:hover {
  /* v21.212：批量选中改为「蓝色加粗边框」，取消圆形勾选框（原勾选框绝对定位会盖住卡片内容）。
     1px 边框 + 1px inset 圈 = 视觉 2px，且不引起布局位移；
     同时覆盖 :hover——触屏点按后 :hover 会「粘住」，否则最后点的那张会丢失蓝框 */
  background: var(--bg-primary);
  border-color: var(--primary-color);
  box-shadow: inset 0 0 0 1px var(--primary-color);
}

.notes-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
}

/* v21.164 视觉改版（图1 基线）：卡片白底 + 细边框 + 去投影，弱化厚重感；
   v21.207：hover 不再上浮/加阴影，改为浅蓝底 + 浅蓝边（颜色过渡）；圆角 12px */
.note-card {
  position: relative;
  background: var(--bg-primary);
  border-radius: var(--radius-lg);
  padding: 20px;
  border: 1px solid var(--border-color);
  cursor: pointer;
  transition: background-color 0.15s, border-color 0.15s;
  min-width: 0;
}

.note-card:hover {
  background: var(--primary-light);
  border-color: var(--primary-tint);
}

.note-card-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 12px;
  gap: 12px;
}

.note-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
  min-width: 0;
}

.note-actions {
  display: flex;
  gap: 4px;
  opacity: 0;
  transition: opacity 0.3s;
}

.note-card:hover .note-actions {
  opacity: 1;
}

.note-preview {
  font-size: 14px;
  color: var(--text-secondary);
  line-height: 1.6;
  /* v21.115：note-footer 已整体隐藏（时间栏需求），预览底部 margin 失去意义，归零避免卡片底部留白 */
  margin-bottom: 0;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-all;
}

.note-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.note-meta {
  display: flex;
  align-items: center;
}

.note-time {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--text-muted);
}

.note-tags {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.pagination-container {
  display: flex;
  justify-content: center;
  margin-top: 32px;
  padding: 24px 0;
}

@media (max-width: 768px) {
  .notes-list-container {
    padding: 12px;
  }

  .notes-list-header {
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
    margin-bottom: 12px; /* F-8：移动端标题栏下间距统一 12px（覆盖 PC 24px） */
  }

  .header-left {
    justify-content: space-between;
    gap: 12px; /* F-8：统一 12px（覆盖 PC 16px） */
    flex-wrap: nowrap; /* F-7：标题/文件夹/数量保持一行均布，不换行 */
  }

  .page-title {
    font-size: 13px; /* F-7：与文件夹字体同大小（覆盖移动端 20px / PC 28px） */
    font-weight: 600;
  }

  .notes-count {
    font-size: 13px; /* F-7：与文件夹字体同大小（覆盖 PC 14px） */
  }

  .header-right {
    width: 100%;
    /* v21.98（用户需求）：4 个操作按钮改横向一行等宽均分
       （替代 v21.80 的 2×2 均布——用户反馈单行更符合操作习惯，最长按钮 4 字在
       ≥320px 屏宽下可容纳；极窄屏如有溢出再评估） */
    flex-wrap: nowrap;
  }

  .header-right .el-button {
    /* 等宽均分：4 按钮一行，flex-basis 0 让宽度完全由 flex 分配 */
    flex: 1 1 0;
    margin-left: 0;
    /* v21.98：字号与「共 X 条笔记」统一为 13px；padding 紧凑化让文字垂直居中、
       横向不浪费（原 PC 12px 24px 在均分布局下挤压文字空间） */
    font-size: 13px;
    padding: 8px 4px;
  }

  .create-btn {
    justify-content: center;
  }

  .notes-list-toolbar {
    flex-direction: column;
    gap: 12px;
    margin-bottom: 12px; /* F-8：移动端工具栏下间距统一 12px（覆盖 PC 24px） */
  }

  .search-box {
    width: 100%;
    min-width: auto;
  }

  /* v21.98（用户需求）：搜索框文字与「共 X 条笔记」统一 13px
     （Element Plus el-input 默认 14px，与 notes-count 13px 不一致） */
  .search-input :deep(.el-input__inner) {
    font-size: 13px;
  }

  .filter-group {
    width: 100%;
    gap: 12px; /* F-8：移动端筛选组间距统一 12px（原 8px 与全局不一致） */
  }

  /* 移动端隐藏排序筛选（屏幕空间紧张，保持默认排序即可） */
  .sort-filter {
    display: none;
  }

  .tag-select,
  .sort-select {
    flex: 1;
    width: auto;
  }

  .notes-grid {
    grid-template-columns: 1fr;
    /* v21.115（用户需求）：移动端卡片间距压缩 12→8px，提升单屏信息密度 */
    gap: 8px;
  }

  .note-card {
    /* v21.115（用户需求）：移动端条目压缩，内边距 16→12px */
    padding: 12px;
  }

  .note-title {
    font-size: 14px;
  }

  /* 1.0.218（用户需求）：移动端不再显示行内操作按钮（编辑/下载/删除）。
     触屏没有 hover，点按后的粘滞 :hover 会让三个图标"常亮"，既拥挤又易误触删除；
     功能可达性：编辑=点卡片进入；下载/删除=批量模式选择后操作。 */
  .note-actions {
    display: none;
  }

  /* v21.115（用户需求）：标题与内容间距压缩 12→6px（覆盖 PC 值） */
  .note-card-header {
    margin-bottom: 6px;
  }

  .note-preview {
    font-size: 13px;
    -webkit-line-clamp: 2;
  }

  .pagination-container {
    margin-top: 12px; /* F-8：移动端分页间距统一 12px（覆盖 16px） */
    padding: 12px 0;
  }

  :deep(.el-pagination) {
    flex-wrap: wrap;
    justify-content: center;
  }
}
</style>
