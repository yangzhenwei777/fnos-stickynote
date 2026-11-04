<template>
  <el-dialog
    :model-value="modelValue"
    title="历史版本"
    width="min(92vw, 860px)"
    append-to-body
    @update:model-value="$emit('update:modelValue', $event)"
    @open="load(1)"
  >
    <div
      v-if="encrypted"
      class="history-empty"
    >
      加密笔记暂不支持版本历史。可在解密后继续使用历史版本功能。
    </div>
    <div
      v-else
      class="history-layout"
    >
      <div class="history-list">
        <div
          v-if="loading"
          class="history-loading"
        >
          加载中…
        </div>
        <div
          v-else-if="list.length === 0"
          class="history-empty"
        >
          暂无历史版本。编辑保存后会自动生成。
        </div>
        <template v-else>
          <button
            v-for="item in list"
            :key="item.id"
            type="button"
            class="history-item"
            :class="{ active: detail && detail.id === item.id }"
            @click="openDetail(item)"
          >
            <span class="history-item-time">{{ formatDate(item.created_at) }}</span>
            <span class="history-item-meta">
              <el-tag
                size="small"
                :type="item.source === 'auto' ? 'info' : 'primary'"
              >
                {{ historySourceLabel(item.source) }}
              </el-tag>
              <span class="history-item-size">{{ formatHistorySize(item.size_bytes) }}</span>
            </span>
          </button>
          <el-pagination
            v-if="total > pageSize"
            small
            layout="prev, pager, next"
            :total="total"
            :page-size="pageSize"
            :current-page="page"
            class="history-pager"
            @current-change="load"
          />
        </template>
      </div>

      <div class="history-preview">
        <div
          v-if="!detail"
          class="history-empty"
        >
          选择左侧版本查看内容
        </div>
        <template v-else>
          <div class="history-preview-header">
            <h4>{{ detail.title || '无标题' }}</h4>
            <el-button
              type="primary"
              size="small"
              :loading="restoring"
              @click="restore"
            >
              恢复此版本
            </el-button>
          </div>
          <div
            class="history-preview-content"
            v-html="sanitizedContent"
          />
        </template>
      </div>
    </div>
  </el-dialog>
</template>

<script setup>
import { ref, computed } from 'vue'
import { ElMessageBox } from 'element-plus'
import { getNoteHistory, getNoteHistoryDetail, restoreNoteHistory } from '@/api/history'
import { sanitizeNoteHtml } from '@/utils/sanitize'
import { formatDate } from '@/utils/note'
import { formatHistorySize, historySourceLabel } from '@/utils/history'
import { message } from '@/utils/message'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  note: { type: Object, default: null }
})
const emit = defineEmits(['update:modelValue', 'restored'])

const loading = ref(false)
const list = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const encrypted = ref(false)
const detail = ref(null)
const restoring = ref(false)

const sanitizedContent = computed(() => sanitizeNoteHtml(detail.value?.content || ''))

async function load(p = 1) {
  if (!props.note?.id) return
  loading.value = true
  page.value = p
  try {
    const { data } = await getNoteHistory(props.note.id, { page: p, page_size: pageSize })
    list.value = data.history || []
    total.value = data.total || 0
    encrypted.value = !!data.encrypted
    if (list.value.length && (!detail.value || !list.value.some((x) => x.id === detail.value.id))) {
      await openDetail(list.value[0])
    }
  } catch (e) {
    message.error(e?.response?.data?.error || '获取历史版本失败')
  } finally {
    loading.value = false
  }
}

async function openDetail(item) {
  if (!props.note?.id || !item) return
  try {
    const { data } = await getNoteHistoryDetail(props.note.id, item.id)
    detail.value = data
  } catch (e) {
    message.error(e?.response?.data?.error || '获取历史版本失败')
  }
}

async function restore() {
  if (!props.note?.id || !detail.value) return
  try {
    await ElMessageBox.confirm(
      '恢复后当前内容会被覆盖，且当前内容会先保存为一条历史。确定恢复此版本吗？',
      '恢复历史版本',
      { type: 'warning', confirmButtonText: '恢复', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  restoring.value = true
  try {
    const { data } = await restoreNoteHistory(props.note.id, detail.value.id, props.note.version)
    emit('restored', data.note)
    emit('update:modelValue', false)
    message.success('已恢复历史版本')
  } catch (e) {
    message.error(e?.response?.data?.error || '恢复历史版本失败')
  } finally {
    restoring.value = false
  }
}
</script>

<style scoped>
.history-layout {
  display: flex;
  gap: 16px;
  min-height: 320px;
}

.history-list {
  flex: 0 0 240px;
  max-height: 56vh;
  overflow-y: auto;
  border-right: 1px solid var(--border-color);
  padding-right: 12px;
}

.history-item {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  width: 100%;
  padding: 8px;
  margin-bottom: 4px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.history-item:hover {
  background: var(--bg-secondary);
}

.history-item.active {
  border-color: var(--primary-color);
  background: var(--bg-secondary);
}

.history-item-time {
  font-size: 13px;
  color: var(--text-primary);
}

.history-item-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-muted);
}

.history-pager {
  margin-top: 8px;
  justify-content: center;
}

.history-preview {
  flex: 1 1 auto;
  min-width: 0;
  max-height: 56vh;
  overflow-y: auto;
}

.history-preview-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border-color);
}

.history-preview-header h4 {
  margin: 0;
  font-size: 15px;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.history-preview-content {
  padding: 12px 4px;
  line-height: 1.7;
  color: var(--text-primary);
  overflow-wrap: anywhere;
}

.history-loading,
.history-empty {
  padding: 24px 8px;
  text-align: center;
  font-size: 13px;
  color: var(--text-muted);
}

@media (max-width: 640px) {
  .history-layout {
    flex-direction: column;
  }

  .history-list {
    flex: 1 1 auto;
    max-height: 30vh;
    border-right: none;
    border-bottom: 1px solid var(--border-color);
    padding-right: 0;
    padding-bottom: 8px;
  }

  .history-preview {
    max-height: 32vh;
  }
}
</style>
