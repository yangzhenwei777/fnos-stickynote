<template>
  <el-dialog
    :model-value="modelValue"
    title="分享链接"
    width="min(92vw, 640px)"
    append-to-body
    @update:model-value="$emit('update:modelValue', $event)"
    @open="onOpen"
  >
    <p class="share-tip">
      生成只读链接，任何持有链接的人都能查看这篇笔记。可随时撤销，默认 7 天过期。
    </p>
    <div class="share-create">
      <el-select
        v-model="days"
        class="share-days"
        aria-label="有效期"
      >
        <el-option
          label="1 天"
          :value="1"
        />
        <el-option
          label="7 天"
          :value="7"
        />
        <el-option
          label="30 天"
          :value="30"
        />
      </el-select>
      <el-button
        type="primary"
        :loading="creating"
        @click="create"
      >
        生成分享链接
      </el-button>
    </div>

    <div
      v-if="newLink"
      class="share-new"
    >
      <el-input
        :model-value="newLink"
        readonly
        aria-label="新分享链接"
      />
      <el-button @click="copy(newLink)">
        复制
      </el-button>
    </div>

    <div class="share-list">
      <div
        v-if="loading"
        class="share-empty"
      >
        加载中…
      </div>
      <div
        v-else-if="shares.length === 0"
        class="share-empty"
      >
        还没有分享链接
      </div>
      <template v-else>
        <div
          v-for="item in shares"
          :key="item.id"
          class="share-item"
        >
          <div class="share-item-main">
            <div class="share-item-time">
              创建 {{ formatDate(item.created_at) }} · 到期 {{ formatDate(item.expires_at) }}
            </div>
            <div class="share-item-meta">
              <el-tag
                size="small"
                :type="shareStatus(item) === 'active' ? 'success' : 'info'"
              >
                {{ shareStatusLabel(shareStatus(item)) }}
              </el-tag>
              <span>访问 {{ item.access_count }} 次</span>
            </div>
          </div>
          <el-button
            v-if="shareStatus(item) === 'active'"
            size="small"
            text
            type="danger"
            @click="revoke(item)"
          >
            撤销
          </el-button>
        </div>
      </template>
    </div>
  </el-dialog>
</template>

<script setup>
import { ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import { createNoteShare, listNoteShares, revokeNoteShare } from '@/api/share'
import { formatDate } from '@/utils/note'
import { shareLink, shareStatus, shareStatusLabel } from '@/utils/share'
import { message } from '@/utils/message'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  note: { type: Object, default: null }
})
defineEmits(['update:modelValue'])

const days = ref(7)
const creating = ref(false)
const loading = ref(false)
const shares = ref([])
const newLink = ref('')

function onOpen() {
  newLink.value = ''
  load()
}

async function load() {
  if (!props.note?.id) return
  loading.value = true
  try {
    const { data } = await listNoteShares(props.note.id)
    shares.value = data.shares || []
  } catch (e) {
    message.error(e?.response?.data?.error || '获取分享链接失败')
  } finally {
    loading.value = false
  }
}

async function create() {
  if (!props.note?.id) return
  creating.value = true
  try {
    const { data } = await createNoteShare(props.note.id, days.value)
    newLink.value = shareLink(data.token)
    await load()
    message.success('分享链接已生成')
  } catch (e) {
    message.error(e?.response?.data?.error || '生成分享链接失败')
  } finally {
    creating.value = false
  }
}

async function revoke(item) {
  try {
    await ElMessageBox.confirm('撤销后该链接立即失效，确定撤销吗？', '撤销分享链接', {
      type: 'warning',
      confirmButtonText: '撤销',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  try {
    await revokeNoteShare(props.note.id, item.id)
    message.success('已撤销')
    await load()
  } catch (e) {
    message.error(e?.response?.data?.error || '撤销失败')
  }
}

async function copy(text) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    message.success('链接已复制')
  } catch {
    message.error('复制失败，请手动选择链接复制')
  }
}
</script>

<style scoped>
.share-tip {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--text-muted);
}

.share-create {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}

.share-days {
  width: 110px;
}

.share-new {
  display: flex;
  gap: 8px;
  padding: 10px;
  margin-bottom: 12px;
  background: var(--bg-secondary);
  border-radius: 8px;
}

.share-list {
  max-height: 42vh;
  overflow-y: auto;
}

.share-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px solid var(--border-color);
}

.share-item-main {
  min-width: 0;
}

.share-item-time {
  font-size: 13px;
  color: var(--text-primary);
  overflow-wrap: anywhere;
}

.share-item-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 4px;
  font-size: 12px;
  color: var(--text-muted);
}

.share-empty {
  padding: 24px 8px;
  text-align: center;
  font-size: 13px;
  color: var(--text-muted);
}
</style>
