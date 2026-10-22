<template>
  <div class="admin-container">
    <div class="admin-header">
      <h2>系统备份</h2>
      <el-tag
        v-if="store.running"
        type="warning"
        size="small"
        effect="dark"
      >
        备份进行中…
      </el-tag>
    </div>

    <el-tabs v-model="tab">
      <el-tab-pane
        label="备份目标"
        name="targets"
      >
        <BackupTargets />
      </el-tab-pane>
      <el-tab-pane
        label="快照与下载"
        name="snapshots"
      >
        <BackupSnapshots />
      </el-tab-pane>
      <el-tab-pane
        label="执行记录"
        name="jobs"
      >
        <BackupJobs />
      </el-tab-pane>
      <el-tab-pane
        label="主密钥"
        name="key"
      >
        <BackupKey />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { useBackupStore } from '@/stores/backup'
import BackupTargets from './backup/BackupTargets.vue'
import BackupSnapshots from './backup/BackupSnapshots.vue'
import BackupJobs from './backup/BackupJobs.vue'
import BackupKey from './backup/BackupKey.vue'

const store = useBackupStore()
const tab = ref('targets')

let pollTimer = null
onMounted(() => {
  store.refresh()
  // 轻量轮询：running 时 5s 同步一次状态/记录（备份异步在后台跑）。
  pollTimer = setInterval(() => {
    store.loadStatus()
    if (store.running) store.loadJobs()
  }, 5000)
})
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })
</script>

<style scoped>
.admin-container {
  padding: 24px;
  overflow-y: auto;
  height: 100%;
  -webkit-overflow-scrolling: touch;
  background: var(--bg-primary);
  border-radius: var(--radius-xl);
  border: 1px solid var(--border-color);
}
.admin-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.admin-header h2 { margin: 0; }

@media (max-width: 768px) {
  .admin-container { padding: 16px; }
  .admin-header h2 { font-size: 18px; }
}
</style>
