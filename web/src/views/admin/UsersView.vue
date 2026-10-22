<template>
  <div class="admin-container">
    <div class="admin-header">
      <h2>用户管理</h2>
      <el-button
        type="primary"
        :icon="Plus"
        @click="showCreateDialog"
      >
        新建用户
      </el-button>
    </div>

    <!-- v21.73 P2-09：多管理员告警（应仅存在一个 administrator） -->
    <el-alert
      v-if="adminCount > 1"
      type="warning"
      :closable="false"
      show-icon
      class="multi-admin-alert"
      title="检测到多个管理员账号，请确认是否为历史数据遗留"
    />

    <!-- Desktop table -->
    <el-table
      v-if="!isMobile"
      :data="users"
      stripe
      style="width: 100%"
    >
      <el-table-column
        prop="id"
        label="ID"
        width="80"
      />
      <el-table-column
        prop="username"
        label="用户名"
      />
      <el-table-column
        prop="role"
        label="角色"
        width="120"
      >
        <template #default="{ row }">
          <el-tag
            :type="row.role === 'admin' ? 'danger' : 'info'"
            size="small"
          >
            {{ row.role === 'admin' ? '管理员' : '用户' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        label="二次验证"
        width="100"
      >
        <template #default="{ row }">
          <el-switch
            :model-value="row.totp_enabled"
            :disabled="!row.totp_enabled"
            :title="!row.totp_enabled ? '需用户在个人设置中自行开启' : ''"
            @click="toggleTotp(row)"
          />
        </template>
      </el-table-column>
      <el-table-column
        label="状态"
        width="90"
      >
        <template #default="{ row }">
          <el-tag
            :type="row.status === 'disabled' ? 'danger' : 'success'"
            size="small"
          >
            {{ row.status === 'disabled' ? '已禁用' : '正常' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        prop="created_at"
        label="创建时间"
        width="180"
      >
        <template #default="{ row }">
          {{ new Date(row.created_at).toLocaleString() }}
        </template>
      </el-table-column>
      <el-table-column
        label="操作"
        width="360"
      >
        <template #default="{ row }">
          <el-button
            size="small"
            @click="showResetPwdDialog(row)"
          >
            重置密码
          </el-button>
          <el-button
            v-if="row.totp_enabled"
            size="small"
            type="danger"
            plain
            @click="handleDisableTotp(row)"
          >
            关闭2FA
          </el-button>
          <el-popconfirm
            :title="row.status === 'disabled' ? '确定启用该用户？' : '确定禁用该用户？'"
            :confirm-button-text="row.status === 'disabled' ? '启用' : '禁用'"
            :confirm-button-type="row.status === 'disabled' ? 'primary' : 'danger'"
            cancel-button-text="取消"
            cancel-button-type="default"
            @confirm="handleToggleStatus(row)"
          >
            <template #reference>
              <el-button
                size="small"
                :type="row.status === 'disabled' ? 'primary' : 'danger'"
                :plain="row.status !== 'disabled'"
              >
                {{ row.status === 'disabled' ? '启用' : '禁用' }}
              </el-button>
            </template>
          </el-popconfirm>
          <el-popconfirm
            title="确定删除该用户？"
            confirm-button-text="删除"
            cancel-button-text="取消"
            cancel-button-type="default"
            confirm-button-type="danger"
            @confirm="handleDelete(row.id)"
          >
            <template #reference>
              <el-button
                size="small"
                type="danger"
              >
                删除
              </el-button>
            </template>
          </el-popconfirm>
        </template>
      </el-table-column>
    </el-table>

    <!-- Mobile card list -->
    <div
      v-else
      class="mobile-card-list"
    >
      <div
        v-for="user in users"
        :key="user.id"
        class="mobile-card"
      >
        <div class="mobile-card-header">
          <span class="mobile-card-title">{{ user.username }}</span>
          <el-tag
            :type="user.role === 'admin' ? 'danger' : 'info'"
            size="small"
          >
            {{ user.role === 'admin' ? '管理员' : '用户' }}
          </el-tag>
        </div>
        <div class="mobile-card-info">
          <span>ID: {{ user.id }}</span>
          <span>{{ new Date(user.created_at).toLocaleDateString() }}</span>
        </div>
        <div class="mobile-card-actions">
          <el-button
            size="small"
            @click="showResetPwdDialog(user)"
          >
            重置密码
          </el-button>
          <el-button
            size="small"
            :type="user.status === 'disabled' ? 'primary' : 'danger'"
            :plain="user.status !== 'disabled'"
            @click="handleToggleStatus(user)"
          >
            {{ user.status === 'disabled' ? '启用' : '禁用' }}
          </el-button>
          <el-button
            size="small"
            type="danger"
            @click="handleDelete(user.id)"
          >
            删除
          </el-button>
        </div>
      </div>
      <div
        v-if="users.length === 0"
        class="empty-list"
      >
        <p>暂无用户</p>
      </div>
    </div>

    <!-- Create Dialog -->
    <el-dialog
      v-model="createDialogVisible"
      title="新建用户"
      :width="isMobile ? '95%' : '400px'"
    >
      <el-form
        :model="createForm"
        :label-width="isMobile ? 'auto' : '80px'"
      >
        <el-form-item label="用户名">
          <el-input v-model="createForm.username" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="createForm.password"
            type="password"
            show-password
            placeholder="至少 6 位"
          />
          <PasswordStrengthBar :password="createForm.password" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDialogVisible = false">
          取消
        </el-button>
        <el-button
          type="primary"
          @click="handleCreate"
        >
          创建
        </el-button>
      </template>
    </el-dialog>

    <!-- v21.108 P2-15：原「编辑用户」对话框已删除——v21.27 用户决策隐藏编辑入口后，
         该对话框与 handleEdit 成为永不触达的死代码（误导测试与后续维护）。
         后端 PUT /users/:id（角色/密码修改）保留；如需恢复 UI 从 git 历史找回。 -->

    <!-- Reset Password Dialog -->
    <el-dialog
      v-model="resetPwdDialogVisible"
      title="重置密码"
      :width="isMobile ? '95%' : '400px'"
    >
      <el-form :label-width="isMobile ? 'auto' : '80px'">
        <el-form-item label="用户名">
          <el-input
            :model-value="resetPwdForm.username"
            disabled
          />
        </el-form-item>
        <el-form-item label="新密码">
          <el-input
            v-model="resetPwdForm.password"
            type="password"
            show-password
            placeholder="至少 6 位"
          />
          <PasswordStrengthBar :password="resetPwdForm.password" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="resetPwdDialogVisible = false">
          取消
        </el-button>
        <el-button
          type="primary"
          @click="handleResetPwd"
        >
          确认重置
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Plus } from '@element-plus/icons-vue'
import { getUsers, createUser, deleteUser } from '../../api/user' // v21.108 P2-15：updateUser 封装保留在 api/user.js（对应后端 PUT /users/:id），UI 死代码已移除
import { message } from '@/utils/message'
import api from '@/api/request'
import PasswordStrengthBar from '@/components/PasswordStrengthBar.vue'

const users = ref([])
// v21.73 P2-09：多管理员检测——DB 唯一索引失败时可能遗留多个 admin，页面显式告警
const adminCount = computed(() => users.value.filter(u => u.role === 'admin').length)
const createDialogVisible = ref(false)
const resetPwdDialogVisible = ref(false)
const isMobile = ref(false)
const createForm = ref({ username: '', password: '', role: 'user' })
const resetPwdForm = ref({ id: 0, username: '', password: '' })

onMounted(() => {
  checkMobile()
  window.addEventListener('resize', checkMobile)
  loadUsers()
})

onUnmounted(() => {
  window.removeEventListener('resize', checkMobile)
})

function checkMobile() {
  isMobile.value = window.innerWidth <= 768
}

async function loadUsers() {
  // v21.107 P2-13：补 catch——此前接口失败（500/权限被撤 403）unhandled rejection，
  // 页面卡加载态无任何提示（前台同类场景均有 toast，管理后台口径拉齐）
  try {
    const { data } = await getUsers()
    users.value = data.users
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '用户列表加载失败')
  }
}

function showCreateDialog() {
  createForm.value = { username: '', password: '', role: 'user' }
  createDialogVisible.value = true
}

async function handleCreate() {
  // v21.96：弱密码放行，仅保留 6 位硬下限 + 成功后的强度提示（不拦截）
  if (!createForm.value.username || !createForm.value.password) {
    message.warning('请填写用户名和密码')
    return
  }
  if (createForm.value.password.length < 6) {
    message.warning('密码至少 6 位')
    return
  }
  try {
    // v21.107 P2-2：解包 response（此前 data?.password_strength 恒 undefined，弱密码提示死代码）
    const { data } = await createUser({
      username: createForm.value.username,
      password: createForm.value.password,
      role: createForm.value.role
    })
    message.success('创建成功')
    if (data?.password_strength === 'weak') {
      message.warning('密码强度偏弱', '建议使用更强的密码')
    }
    createDialogVisible.value = false
    loadUsers()
  } catch (e) {
    message.error(e.response?.data?.error || '创建失败')
  }
}


// v21.108 P2-15：编辑用户对话框死代码（handleEdit/editForm/editDialogVisible）已随
// 模板块一并移除（v21.27 隐藏入口后永不可达）。改角色能力仍走后端 PUT /users/:id。

async function handleDelete(id) {
  try {
    await deleteUser(id)
    message.success('删除成功')
    loadUsers()
  } catch (e) {
    message.error(e.response?.data?.error || '删除失败')
  }
}

async function toggleTotp(user) {
  if (!user.totp_enabled) {
    message.info('需用户在个人设置中自行开启二次验证')
    return
  }
  try {
    await ElMessageBox.confirm(`确定关闭 ${user.username} 的两步验证？`, '关闭二次验证', {
      confirmButtonText: '确定', cancelButtonText: '取消', type: 'warning',
      // v21.211：关闭 2FA 属高风险操作，确认键用红色
      confirmButtonClass: 'el-button--danger',
      customClass: 'msgbox-danger',
    })
    await handleDisableTotp(user)
  } catch { /* 用户取消 */ }
}

async function handleDisableTotp(user) {
  try {
    await api.post(`/users/${user.id}/disable-totp`)
    user.totp_enabled = false
    message.success(`已关闭 ${user.username} 的两步验证`)
  } catch (e) {
    message.error(e.response?.data?.error || '操作失败')
  }
}

function showResetPwdDialog(user) {
  resetPwdForm.value = { id: user.id, username: user.username, password: '' }
  resetPwdDialogVisible.value = true
}

async function handleResetPwd() {
  // v21.96：弱密码放行，仅保留 6 位硬下限 + 成功后的强度提示
  if (!resetPwdForm.value.password || resetPwdForm.value.password.length < 6) {
    message.warning('密码至少 6 位')
    return
  }
  try {
    // v21.107 P2-2：解包 response（同上，reset-password 弱密码提示此前恒不显示）
    const { data } = await api.put(`/users/${resetPwdForm.value.id}/reset-password`, {
      password: resetPwdForm.value.password
    })
    message.success(`已重置 ${resetPwdForm.value.username} 的密码`)
    if (data?.password_strength === 'weak') {
      message.warning('密码强度偏弱', '建议使用更强的密码')
    }
    resetPwdDialogVisible.value = false
  } catch (e) {
    message.error(e.response?.data?.error || '重置失败')
  }
}

async function handleToggleStatus(user) {
  const newStatus = user.status === 'disabled' ? 'active' : 'disabled'
  try {
    await api.put(`/users/${user.id}/status`, { status: newStatus })
    user.status = newStatus
    message.success(`用户 ${user.username} 已${newStatus === 'disabled' ? '禁用' : '启用'}`)
  } catch (e) {
    message.error(e.response?.data?.error || '操作失败')
  }
}
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

.multi-admin-alert {
  margin-bottom: 16px;
}

.admin-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.admin-header h2 {
  margin: 0;
}

.mobile-card-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.mobile-card {
  background: var(--card-bg);
  border-radius: var(--radius-lg);
  padding: 14px 16px;
  border: 1px solid var(--border-color);
}

.mobile-card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

.mobile-card-title {
  font-weight: 500;
  font-size: 15px;
}

.mobile-card-info {
  display: flex;
  gap: 16px;
  font-size: 12px;
  color: var(--text-secondary);
  margin-bottom: 10px;
}

.mobile-card-actions {
  display: flex;
  gap: 8px;
}

.empty-list {
  text-align: center;
  padding: 40px 20px;
  color: var(--text-secondary);
}

@media (max-width: 768px) {
  .admin-container {
    padding: 16px;
  }

  .admin-header h2 {
    font-size: 18px;
  }
}
</style>
