<!--
  文件行数豁免登记（开发纪律 §5.1）
  ─────────────────────────────────────────────────────────────
  当前行数：938（.vue 软上限 800，超出 17%）
  豁免原因：2026-10-15 滚动续载轮增量（触底接线 + 尾部两态，+25 行）；
           本轮同时改 useNotes/单测，再拆本文件会叠加回归风险，维持 TD-003 挂账。
  拆分计划：非硬拆分。下个独立重构窗口把移动端抽屉侧栏或某段弹窗模板抽成子组件，
           优先级高于普通功能轮的顺手拆分。
  追踪：docs/技术债清单.md · TD-003
-->
<template>
  <div
    ref="notesRootRef"
    class="notes-container"
  >
    <div
      v-if="isMobile && sidebarOpen"
      class="sidebar-overlay"
      @click="sidebarOpen = false"
    />

    <div
      class="notes-sidebar"
      :class="{ 'sidebar-open': isMobile && sidebarOpen, 'sidebar-collapsed': sidebarCollapsed }"
      :style="!isMobile && !sidebarCollapsed ? { width: noteSidebarWidth + 'px' } : undefined"
    >
      <div class="sidebar-header">
        <el-button
          type="primary"
          class="create-btn"
          :icon="Plus"
          @click="createNewNote"
        >
          <span>新建笔记</span>
        </el-button>
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
          v-if="!isMobile"
          class="sidebar-toggle-btn"
          :icon="sidebarCollapsed ? ArrowRight : ArrowLeft"
          text
          title="收起笔记列表"
          @click="sidebarCollapsed = !sidebarCollapsed"
        />
      </div>
      <NoteSidebar
        :search-query="searchQuery"
        :show-short-query-hint="showShortQueryHint"
        @update:search="searchQuery = $event"
        @search="debouncedSearch"
      />
      <!-- 2026-10-15 用户需求：列表默认 20 条 + 触底续载（useNotes 内部三闸门防重） -->
      <div
        ref="noteListRef"
        class="note-list"
        @scroll.passive="onListScroll"
      >
        <div
          v-for="note in notes"
          :key="note.id"
          class="note-item"
          :class="{ active: currentNote?.id === note.id }"
          @click="selectNote(note)"
        >
          <div class="note-item-header">
            <div class="note-item-title">
              {{ note.title || '无标题' }}
            </div>
            <div class="note-item-dot" />
          </div>
          <div
            class="note-item-preview"
            v-html="noteHighlight(note)"
          />
        </div>
        <!-- 滚动续载尾部两态：仅已有数据时渲染（空态/错误态沿用下方既有分支） -->
        <div
          v-if="notes.length > 0 && loadingMore"
          class="list-tail-hint"
        >
          <el-icon class="is-loading">
            <Loading />
          </el-icon>
          <span>加载中…</span>
        </div>
        <div
          v-else-if="notes.length > 0 && !hasMore && !listError"
          class="list-tail-hint"
        >
          已全部加载（共 {{ total }} 条）
        </div>
        <!-- v21.107 P2-3：加载失败 ≠ 没有笔记——错误态与空态分离，给出重试入口 -->
        <div
          v-if="notes.length === 0 && !loading && listError"
          class="empty-list"
        >
          <h3>列表加载失败</h3>
          <p>{{ listError }}</p>
          <el-button
            size="small"
            @click="loadNotes(searchQuery, activeTag, folderStore.listParam)"
          >
            重试
          </el-button>
        </div>
        <div
          v-else-if="notes.length === 0 && !loading"
          class="empty-list"
        >
          <div class="empty-icon">
            <el-icon :size="56">
              <Document />
            </el-icon>
          </div>
          <h3>{{ searchQuery ? '没有找到笔记' : '还没有笔记' }}</h3>
          <p>{{ searchQuery ? '试试其他关键词' : '点击上方按钮创建新笔记' }}</p>
        </div>
      </div>
    </div>

    <!-- v21.86 笔记列表侧栏右缘分割线：拖动自定义宽度（收起态/移动端隐藏） -->
    <DragResizer
      v-if="!isMobile && !sidebarCollapsed"
      v-model="noteSidebarWidth"
      :min="240"
      :max="560"
      @resize-end="persistNoteSidebarWidth"
    />

    <div class="notes-editor">
      <el-button
        v-if="sidebarCollapsed && !isMobile"
        class="sidebar-expand-btn"
        :icon="ArrowRight"
        text
        title="展开笔记列表"
        @click="sidebarCollapsed = false"
      />

      <!-- v21.89 移动端文件夹栏已整个移除（用户确认）：文件夹入口迁移到
           /notes-list 卡片列表页标题旁（NotesListView），主页/预览/编辑均不再显示。
           相关样式与 handleMobileFolder/mobileFolderLabel 一并清理。 -->

      <template v-if="currentNote">
        <!-- View Mode -->
        <div
          v-if="!editMode"
          class="note-view"
          :class="{ 'has-expand-btn': sidebarCollapsed && !isMobile }"
        >
          <div class="view-header">
            <div class="view-title-row">
              <h1 class="view-title">
                <span
                  v-if="currentNote.is_encrypted && !encryptUnlocked"
                  class="encrypt-icon"
                >🔒</span>{{ currentNote.title || '无标题' }}
              </h1>
              <!-- 预览重设计：操作按钮从标题上方移入标题行右侧——
                   主按钮随状态切换（未加密/已解锁=编辑，加密未解锁=查看），
                   其余操作收入【更多】下拉；删除确认改用 message.confirm 弹窗 -->
              <div class="view-actions">
                <el-button
                  v-if="currentNote.is_encrypted && !encryptUnlocked"
                  type="primary"
                  :icon="View"
                  @click="showPasswordDialog('view')"
                >
                  查看
                </el-button>
                <el-button
                  v-else
                  type="primary"
                  :icon="EditPen"
                  @click="enterEditMode"
                >
                  编辑
                </el-button>
                <el-dropdown
                  trigger="click"
                  @command="handleViewAction"
                >
                  <el-button
                    class="more-btn"
                    :icon="MoreFilled"
                    aria-label="更多操作"
                  />
                  <template #dropdown>
                    <el-dropdown-menu>
                      <el-dropdown-item
                        v-if="!currentNote.is_encrypted"
                        command="encrypt"
                      >
                        <el-icon><Lock /></el-icon>加密
                      </el-dropdown-item>
                      <el-dropdown-item
                        v-else
                        command="decrypt"
                      >
                        <el-icon><Unlock /></el-icon>解密
                      </el-dropdown-item>
                      <!-- v21.87：移动到/复制到文件夹（弹文件夹选择框） -->
                      <el-dropdown-item
                        command="move"
                        divided
                      >
                        <el-icon><FolderOpened /></el-icon>移动到
                      </el-dropdown-item>
                      <el-dropdown-item
                        command="copy"
                        divided
                      >
                        <el-icon><CopyDocument /></el-icon>复制到
                      </el-dropdown-item>
                      <el-dropdown-item
                        command="delete"
                        class="danger-item"
                        divided
                      >
                        <el-icon><Delete /></el-icon>删除
                      </el-dropdown-item>
                      <!-- v2.2.2 导出为长图（预览页正文 → 离屏海报 → PNG）。
                           R2（用户实测）：导出长图与下载之间缺分隔带且两行视觉贴死
                           （其余各项均有 divided，唯独下载没有）→ 下载补 divided，
                           恢复整份菜单统一的行距节奏 -->
                      <el-dropdown-item
                        command="export-image"
                        divided
                      >
                        <el-icon><Picture /></el-icon>导出长图
                      </el-dropdown-item>
                      <el-dropdown-item
                        command="download"
                        divided
                      >
                        <el-icon><Download /></el-icon>下载
                      </el-dropdown-item>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
              </div>
            </div>
          </div>
          <div
            ref="viewContentRef"
            class="view-content"
            @click="openImageViewer"
            @change="onPreviewCheckboxToggle"
            v-html="sanitizedContent"
          />
        </div>

        <!-- 统一密码弹框（查看/编辑/解密）→ NotePasswordDialog.vue（NotesView 弹框组件化，逻辑仍在 useNoteEncrypt） -->
        <NotePasswordDialog
          v-model="pwdDialogVisible"
          v-model:input="pwdInput"
          v-model:confirm="pwdConfirm"
          :mode="pwdDialogMode"
          :title="pwdDialogTitle"
          :placeholder="pwdPlaceholder"
          :loading="pwdLoading"
          :is-mobile="isMobile"
          @submit="handlePwdSubmit"
        />

        <!-- Edit Mode -->
        <div
          v-if="editMode"
          class="editor-edit-area"
        >
          <div
            class="editor-header"
            :class="{ 'has-expand-btn': sidebarCollapsed && !isMobile }"
          >
            <el-input
              v-model="currentNote.title"
              placeholder="笔记标题"
              class="title-input"
              :readonly="previewMode"
              @input="markDirty"
            />
            <el-button @click="togglePreview">
              {{ previewMode ? '编辑' : '预览' }}
            </el-button>
          </div>
          <!-- 标签编辑输入框已按需求于前台隐藏（后端 tags 链路保留） -->

          <div class="editor-content">
            <TiptapEditor
              v-if="!previewMode"
              ref="editorRef"
              v-model="currentNote.content"
              @update:model-value="markDirty"
            />
            <div
              v-else
              ref="notePreviewContentRef"
              class="note-preview-content"
              @click="openImageViewer"
              @change="onPreviewCheckboxToggle"
              v-html="previewSanitizedContent"
            />
          </div>
        </div>
      </template>

      <div
        v-else-if="openingNote"
        class="editor-empty editor-loading"
      >
        <div class="dashboard-container">
          <el-skeleton
            :rows="8"
            animated
          />
        </div>
      </div>

      <div
        v-else
        class="editor-empty"
      >
        <div class="dashboard-container">
          <div class="dashboard-header">
            <h2>欢迎回来</h2>
            <p>开始记录您的灵感与想法</p>
          </div>

          <div class="recent-notes-section">
            <div class="section-header">
              <h3>最近笔记</h3>
              <el-button
                text
                class="more-link"
                @click="loadAllNotes"
              >
                <span>查看全部</span>
                <el-icon><ArrowRight /></el-icon>
              </el-button>
            </div>
            <div class="recent-notes-list">
              <div
                v-for="note in recentNotes"
                :key="note.id"
                class="recent-note-item"
                @click="selectNote(note)"
              >
                <div class="recent-note-title">
                  {{ note.title || '无标题' }}
                </div>
                <div
                  class="recent-note-preview"
                  v-html="noteHighlight(note)"
                />
                <div class="recent-note-footer">
                  <!-- v21.115（用户需求）：最近笔记时间隐藏；footer 内编辑按钮保留 -->
                  <div
                    v-if="false"
                    class="recent-note-time"
                  >
                    {{ formatDate(note.updated_at) }}
                  </div>
                  <el-button
                    class="recent-note-edit-btn"
                    size="small"
                    :icon="EditPen"
                    @click.stop="quickEditNote(note)"
                  >
                    <span>编辑</span>
                  </el-button>
                </div>
              </div>
              <div
                v-if="recentNotes.length === 0"
                class="no-recent-notes"
              >
                <p>还没有笔记，点击上方按钮创建第一个笔记</p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 右下角保存状态指示器 -->
    <div
      v-if="saveStatus"
      id="save-status"
      :class="'save-status-' + saveStatus"
    >
      <span v-if="saveStatus === 'editing'">编辑中...</span>
      <span v-else-if="saveStatus === 'saving'">保存中...</span>
      <span v-else-if="saveStatus === 'saved'">✓ 已保存</span>
      <span v-else-if="saveStatus === 'error'">✗ 保存失败</span>
    </div>
  </div>
  <NoteImagePreview
    v-model:visible="imageViewerVisible"
    v-model:index="imageViewerIndex"
    :list="imageViewerList"
    :title="currentNote?.title || ''"
  />
  <!-- v21.87 移动到/复制到文件夹选择弹框 -->
  <FolderPickerDialog
    v-model="folderPickerVisible"
    :title="folderPickerTitle"
    :folders="customFolders"
    :current-folder-id="currentNote?.folder_id ?? null"
    :loading="folderPickerLoading"
    @confirm="handleFolderPick"
  />
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useRouter, useRoute, onBeforeRouteLeave } from 'vue-router'
import { Plus, Delete, Document, EditPen, Lock, Unlock, View, Download, Upload, ArrowLeft, ArrowRight, MoreFilled, FolderOpened, CopyDocument, Loading, Picture } from '@element-plus/icons-vue'
import { exportNotePoster } from '@/utils/notePoster'
import { sanitizeNoteHtml, sanitizeHighlight } from '@/utils/sanitize'
import { installAutoContrast } from '@/utils/autoContrast'
import { useNotes } from '@/composables/useNotes'
import { useNoteSearch } from '@/composables/useNoteSearch'
import { useNoteEncrypt } from '@/composables/useNoteEncrypt'
import { useAutoSave } from '@/composables/useAutoSave'
import { useNoteImport } from '@/composables/useNoteImport'
import { useFolderStore } from '@/stores/folder'
import { useFolderSecurityStore } from '@/stores/folderSecurity'
import { stripHtml, formatDate, toggleTaskItemInHtml } from '@/utils/note'
import { message } from '@/utils/message'
import TiptapEditor from '@/components/TiptapEditor.vue'
// v21.220：代码块复制按钮（预览态挂载；编辑态由 TiptapEditor 内 CodeBlockCopy 扩展负责）
import { attachCodeCopyButtons } from '@/components/editor/codeBlockCopy'
import NoteSidebar from '@/components/NoteSidebar.vue'
import NoteImagePreview from '@/components/NoteImagePreview.vue'
import DragResizer from '@/components/DragResizer.vue'
import FolderPickerDialog from '@/components/FolderPickerDialog.vue'
import NotePasswordDialog from '@/components/NotePasswordDialog.vue'
import { moveNoteToFolder, copyNoteToFolder } from '@/api/note'
import { installSyncRefreshPolling } from '@/api/local/desktopApi'
import { IS_DESKTOP } from '@/api/local/transport'

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

const { saveStatus, markDirty, saveBeforeSwitch, checkDraft, clearDraft, clearSaveState, saveConflict, handleKeydown, handleBeforeUnload, cleanupTimers } = useAutoSave(currentNote, currentTagInput, isDirty, applySavedToLists, () => encryptPassword.value, ensureFreshContent)

const { fileInput, handleImportFile: _import } = useNoteImport()

// ---- 笔记正文消毒 hook 已迁至 @/utils/sanitize（幂等注册）----

const sanitizedContent = computed(() => sanitizeNoteHtml(currentNote.value?.content))

// 编辑内预览（v21.52 P7 审计修复）：此前 v-html="currentNote.content" 直接渲染
// 未经 DOMPurify 的编辑中内容（与 IndexedDB 草稿同源），恶意 HTML 有 DOM XSS 风险。
// 统一走与正常查看一致的 sanitizer。
const previewSanitizedContent = computed(() => sanitizedContent.value)

// v21.220：预览态代码块复制按钮——查看态（.view-content）与编辑内预览（.note-preview-content）
// 都是 v-html 静态渲染（无编辑器实例），内容每次变更后重挂（attachCodeCopyButtons 幂等去重）。
// 编辑态对应实现在 TiptapEditor 内 CodeBlockCopy 扩展（widget decoration），两态同款按钮同款交互。
const viewContentRef = ref(null)
const notePreviewContentRef = ref(null)

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
    if (currentNote.value && String(currentNote.value.id) === String(id) && !currentNote.value.is_encrypted) {
      await checkDraft(currentNote.value)
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
})
</script>

<style src="@/styles/notes-view.css" scoped></style>

