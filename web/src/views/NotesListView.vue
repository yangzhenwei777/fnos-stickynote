<!--
  文件行数豁免登记（开发纪律 §5.1）
  ─────────────────────────────────────────────────────────────
  当前行数：以机器门禁实测为准（2.3.2 基线实测 1506；原登记 1359 已失真，
           本注释不再手写行数——见 docs/技术债清单.md 2026-11-03 修正条）
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
                <!-- v21.97：图标对齐 PC 端侧栏（自定义文件夹=Folder；v2.3.0 锁定=Lock） -->
                <el-icon class="nl-f-new-icon">
                  <Lock v-if="folderStore.isFolderLocked(f)" />
                  <Folder v-else />
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
            >
              <el-icon
                v-if="folderStore.isFolderLocked(f)"
                style="vertical-align:-2px;margin-right:4px"
              >
                <Lock />
              </el-icon>{{ f.name }}
            </span>
            <el-button
              v-if="!f.is_default"
              size="small"
              text
              @click="f.is_encrypted ? folderSecurity.openDecrypt(f) : folderSecurity.openEncrypt(f)"
            >
              {{ f.is_encrypted ? '解密' : '加密' }}
            </el-button>
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
      <div
        class="search-box"
        title="搜索语法：tag:标签、folder:文件夹、sort:updated|created|title、since:2026-01-01、until:2026-12-31，多个条件用空格分隔"
      >
        <el-icon class="search-icon">
          <Search />
        </el-icon>
        <el-input
          v-model="searchQuery"
          placeholder="搜索笔记… 支持 tag: folder:"
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
// NotesListView —— 移动端/PC 笔记列表页（v21.89 起承载列表视图）。
// 2026-11-03 巨型 Vue 拆分 刀6：全部逻辑抽至 composables/useNotesListView.js
// （script 体机械搬移，零行为变化）；本文件保留模板 + scoped 样式 + 模板直接引用
// 的图标/子组件/工具函数。历史修复点见 useNotesListView.js 内注释。
import { Plus, Document, Search, Edit, Delete, Clock, Tickets, Download, Upload, Folder, Files, ArrowDown, Check, Setting, Lock } from '@element-plus/icons-vue'
import SyncInitBanner from '@/components/SyncInitBanner.vue'
import NotePager from '@/components/NotePager.vue'
import { stripHtml, splitTags, formatDate } from '@/utils/note'
import { useNotesListView } from '@/composables/useNotesListView'
import '@/styles/notes-list-view.css'

const {
  activeTag,
  allMode,
  batchDelete,
  batchExport,
  batchExporting,
  batchMode,
  createNewNote,
  creating,
  currentPage,
  debouncedSearch,
  deleteNoteItem,
  exportAll,
  exportSingle,
  exportingAll,
  fileInput,
  folderSecurity,
  folderStore,
  handleFolderChange,
  handleImportFile,
  isMobile,
  listError,
  loadNotes,
  loading,
  manageFoldersVisible,
  newFolderDialogVisible,
  newFolderLoading,
  newFolderName,
  nlFolderLabel,
  notes,
  onPageChange,
  onToggleAll,
  openNote,
  pageSize,
  removeFolderConfirm,
  renameFolder,
  sanitizeHighlight,
  searchQuery,
  selectAll,
  selectedIds,
  showShortQueryHint,
  sortBy,
  submitNewFolder,
  toggleBatchMode,
  toggleSelect,
  total
} = useNotesListView()
</script>
