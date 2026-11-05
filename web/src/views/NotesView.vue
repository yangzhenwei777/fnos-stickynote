<!--
  文件行数豁免登记（开发纪律 §5.1）
  ─────────────────────────────────────────────────────────────
  当前行数：以机器门禁实测为准（2.3.2 基线实测 1049；原登记 835、注释 938 均已失真，
           本注释不再手写行数——见 docs/技术债清单.md 2026-11-03 修正条）
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
                  placement="bottom-end"
                  popper-class="view-actions-popper"
                  @command="handleViewAction"
                >
                  <el-button
                    class="more-btn"
                    :icon="MoreFilled"
                    aria-label="更多操作"
                  />
                  <template #dropdown>
                    <el-dropdown-menu>
                      <!-- 2026-11-05 用户指令：加密且未输入查看密码时仅保留【解密】；
                           输入查看密码（encryptUnlocked）或未加密后展示完整操作。
                           完整顺序固定为 加密/解密 → 移动到 → 复制到 → 历史版本 → 删除
                           → 下载 → 导出长图 → 分享链接，除首项外每项 divided 统一分隔节奏
                           （覆盖 2.2.2 导出族置顶旧序）。
                           历史版本/分享链接在加密笔记上后端不支持（无历史快照/禁止分享），
                           解锁后仍可见但置灰，提示解密后可用。 -->
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
                      <template v-if="!currentNote.is_encrypted || encryptUnlocked">
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
                        <!-- 2.5.0 版本历史（加密笔记无历史快照，解锁后置灰） -->
                        <el-dropdown-item
                          command="history"
                          divided
                          :disabled="currentNote.is_encrypted"
                          :title="currentNote.is_encrypted ? '解密后可用' : ''"
                        >
                          <el-icon><Clock /></el-icon>历史版本
                        </el-dropdown-item>
                        <el-dropdown-item
                          command="delete"
                          class="danger-item"
                          divided
                        >
                          <el-icon><Delete /></el-icon>删除
                        </el-dropdown-item>
                        <el-dropdown-item
                          command="download"
                          divided
                        >
                          <el-icon><Download /></el-icon>下载
                        </el-dropdown-item>
                        <!-- v2.2.2 导出为长图（预览页正文 → 离屏海报 → PNG） -->
                        <el-dropdown-item
                          command="export-image"
                          divided
                        >
                          <el-icon><Picture /></el-icon>导出长图
                        </el-dropdown-item>
                        <!-- 2.6.0 分享链接（桌面端走网页版，入口隐藏；加密笔记后端禁止分享，解锁后置灰） -->
                        <el-dropdown-item
                          v-if="!IS_DESKTOP"
                          command="share"
                          divided
                          :disabled="currentNote.is_encrypted"
                          :title="currentNote.is_encrypted ? '解密后可用' : ''"
                        >
                          <el-icon><Share /></el-icon>分享链接
                        </el-dropdown-item>
                      </template>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
              </div>
            </div>
          </div>
          <div class="view-body">
            <div
              ref="viewContentRef"
              class="view-content"
              @click="openImageViewer"
              @change="onPreviewCheckboxToggle"
              v-html="sanitizedContent"
            />
            <aside
              v-if="!isMobile && noteOutline.length >= 2"
              class="note-outline"
              :class="{ 'is-scrolling': outlineScrolling }"
              aria-label="文章大纲"
              @wheel="onOutlineWheel"
              @scroll.passive="onOutlineScroll"
            >
              <div class="note-outline-title">
                大纲
              </div>
              <button
                v-for="(item, i) in noteOutline"
                :key="i"
                type="button"
                class="note-outline-item"
                :class="'level-' + item.level"
                @click="scrollToOutlineHeading(i)"
              >
                {{ item.text }}
              </button>
            </aside>
          </div>
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
              @ready="syncBaselineContent"
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
  <NoteHistoryDialog
    v-model="historyDialogVisible"
    :note="currentNote"
    @restored="onHistoryRestored"
  />
  <ShareDialog
    v-model="shareDialogVisible"
    :note="currentNote"
  />
</template>

<script setup>
// NotesView —— 主工作区（查看/编辑/预览 + 列表侧栏）。
// 2026-11-03 巨型 Vue 拆分 刀4：全部视图编排逻辑抽至 composables/useNotesView.js
// （setup 体机械搬移，零行为变化）；本文件保留模板 + 模板直接引用的图标/子组件/fn。
// 样式外置于 styles/notes-view.css（历史既定），历史修复点见 useNotesView.js 内注释。
import { Plus, Delete, Document, EditPen, Lock, Unlock, View, Download, Upload, ArrowLeft, ArrowRight, MoreFilled, FolderOpened, CopyDocument, Loading, Picture, Clock, Share } from '@element-plus/icons-vue'
import { formatDate } from '@/utils/note'
import { IS_DESKTOP } from '@/api/local/transport'
import TiptapEditor from '@/components/TiptapEditor.vue'
import NoteSidebar from '@/components/NoteSidebar.vue'
import NoteImagePreview from '@/components/NoteImagePreview.vue'
import DragResizer from '@/components/DragResizer.vue'
import FolderPickerDialog from '@/components/FolderPickerDialog.vue'
import NotePasswordDialog from '@/components/NotePasswordDialog.vue'
import NoteHistoryDialog from '@/components/NoteHistoryDialog.vue'
import ShareDialog from '@/components/ShareDialog.vue'
import { useNotesView } from '@/composables/useNotesView'

const {
  folderStore, customFolders,
  currentNote, editMode, previewMode, isMobile, sidebarOpen, sidebarCollapsed,
  imageViewerVisible, imageViewerList, imageViewerIndex, openingNote, editorRef,
  openImageViewer, onPreviewCheckboxToggle,
  noteSidebarWidth, persistNoteSidebarWidth,
  notes, recentNotes, loading, listError, loadingMore, total, hasMore,
  noteListRef, onListScroll,
  searchQuery, activeTag, showShortQueryHint, debouncedSearch, loadNotes,
  encryptUnlocked, pwdDialogVisible, pwdDialogMode, pwdDialogTitle,
  pwdPlaceholder, pwdInput, pwdConfirm, pwdLoading,
  saveStatus, markDirty, syncBaselineContent,
  sanitizedContent, previewSanitizedContent,
  noteOutline, scrollToOutlineHeading, outlineScrolling, onOutlineScroll, onOutlineWheel,
  viewContentRef, notePreviewContentRef, noteHighlight,
  createNewNote, handleImportFile, selectNote, enterEditMode, togglePreview, quickEditNote,
  handleViewAction, showPasswordDialog,
  folderPickerVisible, folderPickerTitle, folderPickerLoading, handleFolderPick,
  historyDialogVisible, onHistoryRestored,
  shareDialogVisible,
  handlePwdSubmit, loadAllNotes, notesRootRef
} = useNotesView()
</script>

<style src="@/styles/notes-view.css" scoped></style>
