<template>
  <div class="panel-header">
    <el-icon
      :size="40"
      class="panel-icon"
    >
      <TrendCharts />
    </el-icon>
    <div>
      <h3>用量概览</h3>
      <p>您的笔记与存储使用情况</p>
    </div>
  </div>
  <div class="quota-section">
    <div class="quota-item">
      <div class="quota-label">
        <span>笔记数</span>
        <span class="quota-val">{{ quota.notes_count }} / {{ quota.notes_limit > 0 ? quota.notes_limit : '不限' }}</span>
      </div>
      <el-progress
        :percentage="notesPercent"
        :color="notesPercent > 80 ? '#e6a23c' : '#2563eb'"
        :stroke-width="12"
      />
    </div>
    <div class="quota-item">
      <div class="quota-label">
        <span>存储空间</span>
        <span class="quota-val">{{ formatBytes(quota.storage_bytes) }} / {{ quota.storage_limit_mb > 0 ? quota.storage_limit_mb + ' MB' : '不限' }}</span>
      </div>
      <el-progress
        :percentage="storagePercent"
        :color="storagePercent > 80 ? '#e6a23c' : '#2563eb'"
        :stroke-width="12"
      />
    </div>
    <p class="quota-note">
      提示：回收站中的笔记不计入配额。
    </p>
  </div>
</template>

<script setup>
import { reactive, computed, onMounted } from 'vue'
import { getQuota } from '../../api/note'
import { TrendCharts } from '@element-plus/icons-vue'

// 配额可视化（v19.1）
const quota = reactive({ notes_count: 0, notes_limit: 1000, storage_bytes: 0, storage_limit_mb: 1024 })
const notesPercent = computed(() => quota.notes_limit > 0 ? Math.min(100, Math.round(quota.notes_count / quota.notes_limit * 100)) : 0)
const storagePercent = computed(() => quota.storage_limit_mb > 0 ? Math.min(100, Math.round(quota.storage_bytes / 1024 / 1024 / quota.storage_limit_mb * 100)) : 0)

function formatBytes(n) {
  if (!n) return '0 B'
  if (n < 1024) return n + ' B'
  if (n < 1048576) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1073741824) return (n / 1048576).toFixed(1) + ' MB'
  return (n / 1073741824).toFixed(2) + ' GB'
}

onMounted(async () => {
  try {
    const { data } = await getQuota()
    Object.assign(quota, data)
  } catch { /* 静默失败，不影响页面其他功能 */ }
})
</script>

<style scoped>
.quota-section {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.quota-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.quota-label {
  display: flex;
  justify-content: space-between;
  font-size: 14px;
  font-weight: 500;
  /* v21.168 审计④：旧灰 #9ca3af 仅 2.54:1，改用 --text-muted token（自动随暗色主题） */
  color: var(--text-muted);
}

.quota-val {
  color: #6b7280;
  font-weight: 400;
}

.quota-note {
  font-size: 12px;
  color: var(--text-muted);
  margin: 0;
}
</style>
