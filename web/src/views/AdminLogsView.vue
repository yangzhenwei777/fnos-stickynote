<template>
  <div class="admin-logs-container">
    <div class="logs-header">
      <h2>操作日志</h2>
      <span class="logs-count">共 {{ total }} 条记录</span>
    </div>

    <div class="filter-bar">
      <el-select
        v-model="filterAction"
        placeholder="操作类型"
        clearable
        style="width: 150px"
        @change="doSearch"
      >
        <el-option
          label="全部"
          value=""
        />
        <el-option
          label="用户管理"
          value="user_management"
        />
        <el-option
          label="系统配置"
          value="system_config"
        />
        <el-option
          label="系统管理"
          value="system_admin"
        />
        <el-option
          label="权限变更"
          value="permission_change"
        />
      </el-select>
      <el-input
        v-model="filterUsername"
        placeholder="操作人"
        clearable
        style="width: 160px"
        @clear="doSearch"
        @keyup.enter="doSearch"
      />
      <el-date-picker
        v-model="filterDateRange"
        type="daterange"
        range-separator="至"
        start-placeholder="开始日期"
        end-placeholder="结束日期"
        format="YYYY-MM-DD"
        value-format="YYYY-MM-DD"
        style="width: 280px"
        @change="doSearch"
      />
      <el-button
        type="primary"
        @click="doSearch"
      >
        查询
      </el-button>
      <el-button @click="resetFilter">
        重置
      </el-button>
    </div>

    <el-table
      v-loading="loading"
      :data="logs"
      stripe
      style="width: 100%"
    >
      <el-table-column
        prop="created_at"
        label="时间"
        width="180"
      />
      <el-table-column
        prop="username"
        label="操作人"
        width="120"
      />
      <el-table-column
        prop="action"
        label="操作类型"
        width="120"
      />
      <el-table-column
        prop="target"
        label="目标"
        width="150"
      />
      <el-table-column
        prop="detail"
        label="详情"
        min-width="200"
      />
      <el-table-column
        prop="ip"
        label="IP地址"
        width="140"
      />
    </el-table>

    <div
      v-if="total > pageSize"
      class="pagination-container"
    >
      <el-pagination
        v-model:current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="prev, pager, next"
        @current-change="loadLogs"
      />
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import api from '@/api/request'
import { message } from '@/utils/message'

const logs = ref([])
const loading = ref(false)
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)

const filterAction = ref('')
const filterUsername = ref('')
const filterDateRange = ref(null)

async function loadLogs() {
  loading.value = true
  try {
    const params = {
      page: page.value,
      page_size: pageSize.value,
    }
    if (filterAction.value) params.action = filterAction.value
    if (filterUsername.value) params.username = filterUsername.value
    if (filterDateRange.value && filterDateRange.value.length === 2) {
      params.start_date = filterDateRange.value[0]
      params.end_date = filterDateRange.value[1]
    }
    const { data } = await api.get('/logs', { params })
    logs.value = data.logs || []
    total.value = data.total || 0
  } catch (e) {
    // v21.107 P2-13：console.error 静默 → 用户可见提示
    message.errorOnce(e.response?.data?.error || '日志加载失败')
  } finally {
    loading.value = false
  }
}

function doSearch() {
  page.value = 1
  loadLogs()
}

function resetFilter() {
  filterAction.value = ''
  filterUsername.value = ''
  filterDateRange.value = null
  doSearch()
}

onMounted(() => loadLogs())
</script>

<style scoped>
.admin-logs-container {
  padding: 24px 32px;
}
.logs-header {
  display: flex;
  align-items: baseline;
  gap: 12px;
  margin-bottom: 20px;
}
.logs-header h2 {
  font-size: 20px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
}
.logs-count {
  font-size: 14px;
  color: var(--text-muted);
}
.filter-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.pagination-container {
  margin-top: 20px;
  display: flex;
  justify-content: center;
}
</style>
