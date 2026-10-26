<template>
  <div>
    <div class="panel-header">
      <h3>执行记录</h3>
      <div class="panel-actions">
        <el-button
          :icon="Refresh"
          @click="store.loadJobs()"
        >
          刷新
        </el-button>
        <!-- v2.2.1【清除记录】：只清已终结记录（后端保留运行中）；无记录时禁用 -->
        <el-button
          :icon="Delete"
          :disabled="!store.jobs.length"
          @click="handleClearJobs"
        >
          清除记录
        </el-button>
      </div>
    </div>

    <el-alert
      v-if="store.running && store.currentJob"
      type="info"
      :closable="false"
      show-icon
      class="running"
    >
      正在执行备份（阶段：{{ phaseText(store.currentJob.phase) }}）…
      <el-progress
        :percentage="phasePercent(store.currentJob.phase)"
        :stroke-width="6"
        striped
        striped-flow
        :indeterminate="false"
      />
    </el-alert>

    <el-table
      :data="store.jobs"
      stripe
      style="width: 100%"
    >
      <el-table-column
        prop="id"
        label="#"
        width="60"
      />
      <el-table-column
        label="类型"
        width="70"
      >
        <template #default="{ row }">
          {{ typeText(row.type) }}
        </template>
      </el-table-column>
      <el-table-column
        label="开始时间"
        width="160"
      >
        <template #default="{ row }">
          {{ new Date(row.started_at).toLocaleString() }}
        </template>
      </el-table-column>
      <el-table-column
        label="状态"
        width="90"
      >
        <template #default="{ row }">
          <el-tag
            :type="jobType(row.status)"
            size="small"
          >
            {{ statusText(row.status) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        label="阶段"
        width="100"
      >
        <template #default="{ row }">
          {{ phaseText(row.phase) }}
        </template>
      </el-table-column>
      <el-table-column
        label="错误"
        min-width="160"
        show-overflow-tooltip
      >
        <template #default="{ row }">
          <span :class="row.error_message ? 'err' : 'muted'">
            {{ row.error_message || row.error_code || '—' }}
          </span>
        </template>
      </el-table-column>
    </el-table>
    <p
      v-if="!store.jobs.length"
      class="muted"
    >
      暂无执行记录。
    </p>
  </div>
</template>

<script setup>
import { onMounted } from 'vue'
import { Refresh, Delete } from '@element-plus/icons-vue'
import { useBackupStore } from '@/stores/backup'
import { message } from '@/utils/message'

const store = useBackupStore()
onMounted(() => store.loadJobs())

// v2.2.1 清除执行记录：破坏性操作先确认（沿用 BackupTargets 删除目标惯例）。
// 后端 fail-closed 只清终态记录，运行中的保留——有运行中记录时提示口径如实相告。
async function handleClearJobs() {
  const runningCount = store.jobs.filter(j => j.status === 'RUNNING' || j.status === 'STARTING').length
  const suffix = runningCount > 0 ? `（${runningCount} 条正在执行的记录将保留）` : ''
  const ok = await message.confirm(`确定清除全部已结束的执行记录？此操作不可恢复。${suffix}`, {
    type: 'warning',
    danger: true
  })
  if (!ok) return
  const { ok: done, deleted } = await store.clearJobs()
  if (done) {
    message.success(deleted > 0 ? `已清除 ${deleted} 条执行记录` : '没有可清除的记录')
  }
}

const PHASES = { SNAPSHOT: 25, VERIFY: 45, UPLOAD: 70, ROTATE: 90, SUCCESS: 100 }
function phaseText(p) {
  return { SNAPSHOT: '快照', VERIFY: '自检', UPLOAD: '上传', ROTATE: '轮转', SUCCESS: '完成' }[p] || (p || '准备')
}
function phasePercent(p) { return PHASES[p] || 10 }
function jobType(s) {
  return { SUCCESS: 'success', FAILED: 'danger', INTERRUPTED: 'warning', RUNNING: 'primary', STARTING: 'info' }[s] || 'info'
}
// 类型/状态英文枚举 → 中文（执行记录表全中文化，需求4）。
function typeText(t) {
  return { backup: '备份', restore: '恢复' }[t] || (t || '—')
}
function statusText(s) {
  return { SUCCESS: '成功', FAILED: '失败', RUNNING: '运行中', INTERRUPTED: '已中断', STARTING: '启动中' }[s] || (s || '—')
}
</script>

<style scoped>
.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.panel-header h3 { margin: 0; }
.panel-actions { display: flex; gap: 8px; }
.running { margin-bottom: 14px; }
.err { color: var(--danger, #f56c6c); }
.muted { color: var(--text-secondary); }
</style>
