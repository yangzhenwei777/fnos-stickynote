<template>
  <div>
    <div class="panel-header">
      <h3>备份目标</h3>
      <el-button
        type="primary"
        :icon="Plus"
        @click="openCreate"
      >
        新建目标
      </el-button>
    </div>

    <el-alert
      type="info"
      :closable="false"
      show-icon
      class="hint"
      title="备份会把整站（全部用户笔记+图片+配置+加密密钥）打包成单个 AES-256-GCM 加密归档上传到 WebDAV。恢复为离线方式（下载 .snbak 后用 CLI 换库），暂不提供在线一键恢复。"
    />

    <el-table
      :data="store.targets"
      stripe
      style="width: 100%"
    >
      <el-table-column
        prop="name"
        label="名称"
        min-width="120"
      />
      <el-table-column
        prop="base_url"
        label="WebDAV 地址"
        min-width="200"
        show-overflow-tooltip
      />
      <el-table-column
        label="调度"
        width="120"
      >
        <template #default="{ row }">
          <el-tag
            size="small"
            :type="row.enabled ? 'success' : 'info'"
          >
            {{ row.enabled ? row.schedule_interval : '停用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        label="保留"
        width="70"
      >
        <template #default="{ row }">
          {{ row.retention_count }}
        </template>
      </el-table-column>
      <el-table-column
        label="上次成功"
        width="160"
      >
        <template #default="{ row }">
          <span v-if="row.last_success_at">{{ new Date(row.last_success_at).toLocaleString() }}</span>
          <span
            v-else
            class="muted"
          >从未</span>
        </template>
      </el-table-column>
      <el-table-column
        label="状态"
        width="90"
      >
        <template #default="{ row }">
          <el-tooltip
            v-if="row.consecutive_failures > 0"
            :content="row.last_error"
            placement="top"
          >
            <el-tag
              type="danger"
              size="small"
            >
              连败{{ row.consecutive_failures }}
            </el-tag>
          </el-tooltip>
          <el-tag
            v-else
            type="success"
            size="small"
          >
            正常
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        label="操作"
        width="300"
      >
        <template #default="{ row }">
          <el-button
            size="small"
            :loading="testingId === row.id"
            @click="handleTest(row)"
          >
            测试
          </el-button>
          <el-button
            size="small"
            type="primary"
            :disabled="store.running"
            @click="handleRun(row)"
          >
            立即备份
          </el-button>
          <el-button
            size="small"
            @click="openEdit(row)"
          >
            编辑
          </el-button>
          <el-button
            size="small"
            type="danger"
            @click="handleDelete(row)"
          >
            删除
          </el-button>
        </template>
      </el-table-column>
    </el-table>
    <p
      v-if="!store.targets.length"
      class="muted"
    >
      还没有备份目标，点「新建目标」添加一个 WebDAV（坚果云 / fnOS 自带 WebDAV 等）。
    </p>

    <el-dialog
      v-model="dialogVisible"
      :title="editing ? '编辑目标' : '新建目标'"
      :width="isMobile ? '95%' : '560px'"
    >
      <el-form
        :model="form"
        label-width="110px"
      >
        <el-form-item label="名称">
          <el-input
            v-model="form.name"
            placeholder="例如：坚果云"
          />
        </el-form-item>
        <el-form-item label="WebDAV 地址">
          <el-input
            v-model="form.base_url"
            placeholder="https://dav.jianguoyun.com/dav/"
          />
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="form.username" />
        </el-form-item>
        <el-form-item label="密码/应用密码">
          <el-input
            v-model="form.password"
            type="password"
            show-password
            :placeholder="editing && form.has_secret ? '留空则保持不变' : '坚果云请用应用密码'"
          />
        </el-form-item>
        <el-form-item label="远端目录">
          <el-input
            v-model="form.remote_path"
            placeholder="/StickyNote-Backup/"
          />
        </el-form-item>
        <el-form-item label="调度间隔">
          <el-select v-model="form.schedule_interval">
            <el-option
              v-for="s in intervals"
              :key="s"
              :label="s"
              :value="s"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="保留份数">
          <el-input-number
            v-model="form.retention_count"
            :min="1"
            :max="60"
          />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="高级">
          <el-checkbox v-model="form.allow_loopback">
            允许内网/本机地址
          </el-checkbox>
          <el-checkbox v-model="form.allow_insecure_tls">
            跳过 TLS 校验（仅自建）
          </el-checkbox>
        </el-form-item>
        <el-form-item v-if="form.allow_insecure_tls === false">
          <template #label>
            自定义 CA
          </template>
          <el-input
            v-model="form.custom_ca_pem"
            type="textarea"
            :rows="2"
            placeholder="（可选）PEM 格式 CA 证书"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">
          取消
        </el-button>
        <el-button
          type="primary"
          :loading="saving"
          @click="handleSave"
        >
          保存
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { Plus } from '@element-plus/icons-vue'
import { useBackupStore } from '@/stores/backup'
import { message } from '@/utils/message'

const store = useBackupStore()
const isMobile = ref(window.innerWidth <= 768)
const intervals = ['1h', '6h', '12h', '24h', '7d']
const testingId = ref(0)
const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const editingId = ref(0)

const blank = () => ({
  name: '', type: 'webdav', base_url: '', username: '', password: '', remote_path: '/StickyNote-Backup/',
  enabled: true, retention_count: 7, schedule_interval: '24h',
  allow_loopback: false, allow_insecure_tls: false, custom_ca_pem: '', has_secret: false
})
const form = ref(blank())

onMounted(() => store.loadTargets())

function openCreate() {
  editing.value = false
  editingId.value = 0
  form.value = blank()
  dialogVisible.value = true
}

function openEdit(row) {
  editing.value = true
  editingId.value = row.id
  form.value = {
    ...blank(),
    name: row.name, type: row.type, base_url: row.base_url, username: row.username,
    remote_path: row.remote_path, enabled: row.enabled, retention_count: row.retention_count,
    schedule_interval: row.schedule_interval, allow_loopback: row.allow_loopback,
    allow_insecure_tls: row.allow_insecure_tls, has_secret: row.has_secret, custom_ca_pem: '', password: ''
  }
  dialogVisible.value = true
}

async function handleSave() {
  if (!form.value.name || !form.value.base_url) {
    message.warning('名称与 WebDAV 地址必填')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await store.updateTarget(editingId.value, form.value)
    } else {
      await store.createTarget(form.value)
    }
    dialogVisible.value = false
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  } finally {
    saving.value = false
  }
}

async function handleDelete(row) {
  const ok = await message.confirm(`确定删除目标「${row.name}」？远端已上传的归档不会被删除。`, { type: 'warning', danger: true })
  if (!ok) return
  try {
    await store.removeTarget(row.id)
  } catch (e) {
    message.error(e.response?.data?.error || '删除失败')
  }
}

async function handleTest(row) {
  testingId.value = row.id
  try {
    const res = await store.test(row.id)
    if (res.ok) message.success(`连接正常（${res.supports_move ? '支持' : '不支持'}服务端 MOVE）`)
    else message.errorOnce(`连接失败：${res.error}`)
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '测试失败')
  } finally {
    testingId.value = 0
  }
}

async function handleRun(row) {
  await store.runBackup(row.id)
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
.hint { margin-bottom: 14px; }
.muted { color: var(--text-secondary); }
</style>
