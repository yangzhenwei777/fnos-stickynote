<template>
  <div class="trash-container">
    <div class="trash-header">
      <div class="header-left">
        <h1 class="page-title">
          <el-icon><Delete /></el-icon>
          <span>回收站</span>
        </h1>
        <span class="trash-count">共 {{ total }} 条已删除笔记</span>
        <!-- v18.8 回收站可视化：引用图片占用空间 -->
        <span
          v-if="stats.upload_bytes > 0"
          class="trash-count"
        >图片占用 {{ formatBytes(stats.upload_bytes) }}</span>
      </div>
      <div class="header-right">
        <span class="auto-purge-hint">{{ retentionText }}</span>
        <el-popconfirm
          v-if="notes.length > 0"
          title="彻底删除回收站中所有笔记，此操作不可恢复。确定继续？"
          confirm-button-text="删除全部"
          cancel-button-text="取消"
          cancel-button-type="default"
          confirm-button-type="danger"
          @confirm="handleEmptyTrash"
        >
          <template #reference>
            <el-button
              type="danger"
              :icon="DeleteFilled"
              :loading="emptying"
            >
              <span>删除全部</span>
            </el-button>
          </template>
        </el-popconfirm>
      </div>
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
      v-else-if="notes.length === 0"
      class="empty-container"
    >
      <div class="empty-content">
        <el-icon :size="80">
          <Delete />
        </el-icon>
        <h3>回收站是空的</h3>
        <p>删除的笔记会在这里保留，可随时恢复或手动彻底删除</p>
      </div>
    </div>

    <div
      v-else
      class="trash-list"
    >
      <div
        v-for="note in notes"
        :key="note.id"
        class="trash-card"
      >
        <div class="trash-card-main">
          <h3 class="trash-title">
            {{ note.title || '无标题' }}<span v-if="note.is_encrypted"> 🔒</span>
          </h3>
          <p class="trash-preview">
            {{ note.is_encrypted ? '🔒 已加密' : previewText(note.content) }}
          </p>
          <div class="trash-meta">
            <span>删除于 {{ formatDate(note.deleted_at) }}</span>
            <span
              v-if="false && note.tags"
              class="trash-tags"
            >{{ note.tags }}</span>
          </div>
        </div>
        <div class="trash-actions">
          <el-button
            type="primary"
            :icon="RefreshLeft"
            class="restore-btn"
            @click="handleRestore(note)"
          >
            <span>恢复</span>
          </el-button>
          <el-popconfirm
            title="彻底删除后无法恢复，确定吗？"
            confirm-button-text="彻底删除"
            cancel-button-text="取消"
            cancel-button-type="default"
            confirm-button-type="danger"
            @confirm="handlePermanentDelete(note)"
          >
            <template #reference>
              <el-button
                type="danger"
                plain
                :icon="DeleteFilled"
                class="delete-btn"
              >
                <span>彻底删除</span>
              </el-button>
            </template>
          </el-popconfirm>
        </div>
      </div>
      <div
        v-if="total > pageSize"
        class="trash-pagination"
      >
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50, 100]"
          layout="sizes, prev, pager, next"
          background
          @current-change="loadTrash"
          @size-change="handleSizeChange"
        />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, computed } from 'vue'
import { message } from '@/utils/message'
import { Delete, DeleteFilled, RefreshLeft } from '@element-plus/icons-vue'
import { getTrashNotes, getTrashStats, restoreNote, permanentDeleteNote, emptyTrash } from '@/api/note'
import { getPublicConfig } from '@/api/config'

const notes = ref([])
const loading = ref(true)
// 回收站分页（v18.7）：永久保留只增不减，全量加载迟早撑不住
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
// 回收站可视化（v18.8）：条数 + 引用图片占用
const stats = ref({ count: 0, upload_bytes: 0 })
const retentionDays = ref(0) // 0=永久保留
const emptying = ref(false)

const retentionText = computed(() => {
  if (retentionDays.value <= 0) return '回收站内容永久保留，可随时恢复或手动彻底删除'
  return `回收站内容保留 ${retentionDays.value} 天，超期自动清理，可随时恢复或手动彻底删除`
})

function formatBytes(n) {
  if (!n) return '0 B'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

async function loadStats() {
  try {
    const { data } = await getTrashStats()
    stats.value = data
  } catch { /* 统计失败不阻断列表 */ }
}

function previewText(html) {
  if (!html) return ''
  const text = html.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()
  return text.length > 80 ? text.slice(0, 80) + '…' : text
}

function formatDate(dt) {
  if (!dt) return ''
  const d = new Date(dt)
  if (isNaN(d.getTime())) return String(dt)
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

async function loadTrash() {
  loading.value = true
  try {
    const { data } = await getTrashNotes({ page: page.value, page_size: pageSize.value })
    notes.value = data.notes || []
    total.value = data.total || 0
  } catch {
    message.error('获取回收站失败')
  } finally {
    loading.value = false
  }
  loadStats() // v18.8：列表刷新时同步刷新统计
}

function handleSizeChange() {
  page.value = 1
  loadTrash()
}

async function handleRestore(note) {
  try {
    const { data } = await restoreNote(note.id)
    // v21.107 P2-9：明示恢复去向（后端返回 folder；原文件夹已删时回「我的笔记」）
    message.success(data?.folder ? `已恢复到「${data.folder}」` : '已恢复到笔记列表')
    // 分页场景下就地移除会导致本页缺一条，直接重载当前页
    if (notes.value.length === 1 && page.value > 1) page.value -= 1
    await loadTrash()
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '恢复失败，请重试')
  }
}

async function handlePermanentDelete(note) {
  try {
    await permanentDeleteNote(note.id)
    message.success('已彻底删除')
    if (notes.value.length === 1 && page.value > 1) page.value -= 1
    await loadTrash()
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '删除失败，请重试')
  }
}

async function handleEmptyTrash() {
  emptying.value = true
  try {
    const res = await emptyTrash()
    message.success(`回收站已清空（${res.data.deleted || 0} 条）`)
    page.value = 1
    await loadTrash()
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '清空回收站失败，请重试')
  } finally {
    emptying.value = false
  }
}

async function loadRetentionConfig() {
  try {
    // v21.107 P2-8：保留天数改从 /public-config 读取——/configs 仅管理员可访问，
    // 此前普通用户永远静默回退显示「永久保留」，与管理员设置的真实策略不符。
    const res = await getPublicConfig()
    const configs = res.data || {}
    if (configs.trash_retention_days) {
      retentionDays.value = parseInt(configs.trash_retention_days) || 0
    }
  } catch {
    // 读取失败保持默认值
  }
}

onMounted(() => {
  loadTrash()
  loadRetentionConfig()
})
</script>

<style scoped>
.trash-container {
  max-width: 960px;
  margin: 0 auto;
  padding: 24px 20px;
}

.trash-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
  flex-wrap: wrap;
  gap: 8px;
}

.header-left {
  display: flex;
  align-items: baseline;
  gap: 12px;
}

.page-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 20px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
}

.trash-count {
  font-size: 13px;
  color: var(--text-secondary);
}

.auto-purge-hint {
  font-size: 12px;
  color: var(--text-secondary);
  background: var(--bg-secondary, #f3f4f6);
  padding: 6px 12px;
  border-radius: var(--radius-sm);
}

.loading-container {
  padding: 40px 0;
}

.empty-container {
  display: flex;
  justify-content: center;
  padding: 80px 0;
}

.empty-content {
  text-align: center;
  color: var(--text-secondary);
}

.empty-content h3 {
  margin: 16px 0 8px;
  color: var(--text-primary);
}

.trash-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.trash-card {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  background: var(--bg-primary);
  border: 1px solid var(--border-color, #e5e7eb);
  border-radius: var(--radius-lg);
  padding: 16px 20px;
}

.trash-card-main {
  flex: 1;
  min-width: 0;
}

.trash-title {
  margin: 0 0 6px;
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.trash-preview {
  margin: 0 0 8px;
  font-size: 13px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.trash-meta {
  display: flex;
  gap: 12px;
  font-size: 12px;
  color: var(--text-secondary);
}

.trash-tags {
  color: var(--primary-color);
}

.trash-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.trash-actions .el-button {
  border-radius: var(--radius-md);
  margin-left: 0;
}

.restore-btn {
  background: var(--primary-color);
  border: none;
}

.restore-btn:hover {
  opacity: 0.9;
}

.trash-pagination {
  display: flex;
  justify-content: center;
  margin-top: 20px;
}

@media (max-width: 640px) {
  .trash-card {
    flex-direction: column;
    align-items: stretch;
  }

  .trash-actions {
    width: 100%;
  }

  .trash-actions > * {
    flex: 1;
  }

  .trash-actions .el-button {
    width: 100%;
  }
}
</style>
