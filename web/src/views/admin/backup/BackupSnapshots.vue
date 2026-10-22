<template>
  <div>
    <div class="panel-header">
      <h3>备份快照</h3>
      <div class="header-actions">
        <el-button
          :icon="Search"
          :loading="discovering"
          @click="handleDiscover"
        >
          从远端发现历史快照
        </el-button>
        <el-button
          :icon="Refresh"
          @click="store.loadSnapshots()"
        >
          刷新
        </el-button>
      </div>
    </div>
    <el-alert
      v-if="!store.snapshots.length"
      type="info"
      :closable="false"
      show-icon
      class="hint"
      title="列表为空？若你刚重装/清空过数据，历史归档仍在 WebDAV 上——先「导入主密钥」，再点上方「从远端发现历史快照」把它们登记进来。"
    />
    <el-table
      :data="store.snapshots"
      stripe
      style="width: 100%"
    >
      <el-table-column
        label="备份 ID"
        min-width="280"
        show-overflow-tooltip
      >
        <template #default="{ row }">
          <span class="mono">{{ row.backup_id }}</span>
        </template>
      </el-table-column>
      <el-table-column
        label="时间"
        min-width="170"
      >
        <template #default="{ row }">
          {{ new Date(row.created_at).toLocaleString() }}
        </template>
      </el-table-column>
      <el-table-column
        label="触发"
        width="90"
        align="center"
      >
        <template #default="{ row }">
          {{ triggerText(row.trigger) }}
        </template>
      </el-table-column>
      <el-table-column
        label="规模"
        min-width="240"
      >
        <template #default="{ row }">
          用户{{ row.user_count }} · 笔记{{ row.note_count }} · 附件{{ row.attachment_count }} · {{ fmtSize(row.size_bytes) }}
        </template>
      </el-table-column>
      <el-table-column
        label="状态"
        width="130"
      >
        <template #default="{ row }">
          <el-tag
            :type="statusType(row.status)"
            size="small"
          >
            {{ statusText(row.status) }}
          </el-tag>
          <el-tag
            v-if="row.incomplete"
            type="warning"
            size="small"
          >
            不完整
          </el-tag>
          <el-tag
            v-if="!row.has_remote"
            type="info"
            size="small"
          >
            无可用副本
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column
        label="操作"
        width="360"
      >
        <template #default="{ row }">
          <el-button
            size="small"
            :loading="verifying === row.backup_id"
            :disabled="!row.has_remote"
            @click="handleVerify(row)"
          >
            校验完整性
          </el-button>
          <el-button
            size="small"
            :loading="downloading === row.backup_id"
            :disabled="row.incomplete || !row.has_remote"
            @click="handleDownload(row)"
          >
            下载
          </el-button>
          <el-button
            size="small"
            type="danger"
            plain
            :disabled="row.incomplete || !row.has_remote"
            @click="openRestore(row)"
          >
            恢复整站
          </el-button>
          <el-button
            size="small"
            type="danger"
            :loading="deleting === row.backup_id"
            @click="handleDelete(row)"
          >
            删除备份
          </el-button>
        </template>
      </el-table-column>
    </el-table>
    <p
      v-if="!store.snapshots.length"
      class="muted"
    >
      还没有备份记录。到「备份目标」点「立即备份」生成第一份。
    </p>

    <!-- 在线全量恢复向导 -->
    <el-dialog
      v-model="restoreVisible"
      title="在线恢复整站（危险）"
      width="560px"
    >
      <el-alert
        type="error"
        :closable="false"
        show-icon
        class="hint"
        title="此操作会用所选备份整体覆盖当前全部用户的笔记、图片、配置与登录密钥；恢复过程中站点会短暂不可用，完成后所有人需重新登录。此过程不可撤销。"
      />
      <p
        v-if="restoreTarget"
        class="muted"
      >
        恢复目标：{{ restoreTarget.backup_id.slice(0, 8) }}…（{{ new Date(restoreTarget.created_at).toLocaleString() }}，笔记 {{ restoreTarget.note_count }} 条）
      </p>

      <el-form
        :model="restoreForm"
        label-width="120px"
      >
        <el-form-item label="当前账号密码">
          <el-input
            v-model="restoreForm.password"
            type="password"
            show-password
            autocomplete="off"
            placeholder="二次校验管理员口令"
          />
        </el-form-item>
        <el-form-item label="主密钥来源">
          <el-input
            v-model="restoreForm.key_file_text"
            type="textarea"
            :rows="3"
            placeholder="（通常留空：用本机现有主密钥即可）灾难恢复/换实例时粘贴 backup_master_key.txt"
          />
        </el-form-item>
        <el-form-item label="跨实例恢复">
          <el-checkbox v-model="restoreForm.allow_identity_change">
            允许更换实例身份（当备份来自另一套部署时勾选）
          </el-checkbox>
        </el-form-item>
        <el-form-item label="">
          <el-checkbox v-model="restoreForm.confirm">
            我已确认：整体覆盖当前数据，且已知晓需全员重新登录
          </el-checkbox>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button
          :disabled="restoring"
          @click="restoreVisible = false"
        >
          取消
        </el-button>
        <el-button
          type="danger"
          :disabled="!restoreForm.confirm || restoring"
          :loading="restoring"
          @click="handleRestore"
        >
          {{ restoring ? '恢复进行中…' : '确认恢复' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { Refresh, Search } from '@element-plus/icons-vue'
import { useBackupStore } from '@/stores/backup'
import { message } from '@/utils/message'
import { extractApiError } from '@/utils/apiError'

const store = useBackupStore()
const verifying = ref('')
const downloading = ref('')
const deleting = ref('')
const discovering = ref(false)
const restoring = ref(false)
const restoreVisible = ref(false)
const restoreTarget = ref(null)
const restoreForm = ref({ password: '', key_file_text: '', allow_identity_change: false, confirm: false })

onMounted(() => store.loadSnapshots())

async function handleDiscover() {
  discovering.value = true
  try {
    const res = await store.discover()
    if (res.ok) {
      if (res.added > 0) message.success(`发现并登记 ${res.added} 个历史快照`)
      else message.info('远端没有发现新的历史快照')
    } else {
      message.errorOnce(res.error || '发现失败')
    }
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '发现失败')
  } finally {
    discovering.value = false
  }
}

function fmtSize(n) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`
}
function statusType(s) {
  return { LOCAL_VERIFIED: 'success', VERIFIED: 'success', DISCOVERED: 'primary', INCOMPLETE: 'warning', FAILED: 'danger' }[s] || 'info'
}
function statusText(s) {
  return { LOCAL_CREATED: '已生成', LOCAL_VERIFIED: '已校验', DISCOVERED: '远端发现', INCOMPLETE: '不完整', FAILED: '失败' }[s] || s
}
// 触发方式英文枚举 → 中文（需求4：其他页面英文菜单一并中文化）。
function triggerText(t) {
  return { manual: '手动', scheduled: '定时', discovered: '远端发现' }[t] || (t || '—')
}

async function handleVerify(row) {
  verifying.value = row.backup_id
  try {
    const res = await store.verify(row.backup_id)
    if (res.ok) message.success('存档完整性校验通过')
    else message.errorOnce(`校验未通过：${res.error}`)
  } catch (e) {
    message.errorOnce(e.response?.data?.error || '校验失败')
  } finally {
    verifying.value = ''
  }
}

async function handleDownload(row) {
  downloading.value = row.backup_id
  try {
    await store.download(row.backup_id)
  } catch (e) {
    // 下载走 responseType:blob，出错时响应体是 Blob——用共享 util 抽出后端具体原因
    message.errorOnce(await extractApiError(e, '下载失败'))
  } finally {
    downloading.value = ''
  }
}

async function handleDelete(row) {
  const ok = await message.confirm(
    `确定删除备份「${row.backup_id.slice(0, 8)}…」？将同时删除所有目标上的远端归档(.snbak)与索引(.index.json)文件，且本地记录一并清除，此操作不可恢复。`,
    { type: 'warning', danger: true }
  )
  if (!ok) return
  deleting.value = row.backup_id
  try {
    const res = await store.deleteSnapshot(row.backup_id)
    if (res.warnings && res.warnings.length) {
      message.warning(`本地记录已删除，但部分远端文件删除失败：${res.warnings.join('；')}`)
    } else {
      message.success(`已删除备份（远端归档 ${res.deleted_remote} 份）`)
    }
  } catch (e) {
    const code = e.response?.data?.code
    if (e.response?.status === 409 || code === 'BACKUP_ALREADY_RUNNING') {
      message.warning('已有备份/恢复操作在运行，请稍候')
    } else {
      message.errorOnce(e.response?.data?.error || '删除失败')
    }
  } finally {
    deleting.value = ''
  }
}

function openRestore(row) {
  restoreTarget.value = row
  restoreForm.value = { password: '', key_file_text: '', allow_identity_change: false, confirm: false }
  restoreVisible.value = true
}

async function handleRestore() {
  if (!restoreForm.value.password) { message.warning('请输入当前账号密码'); return }
  restoring.value = true
  try {
    await store.restore({
      backup_id: restoreTarget.value.backup_id,
      password: restoreForm.value.password,
      key_file_text: restoreForm.value.key_file_text,
      allow_identity_change: restoreForm.value.allow_identity_change,
      confirm: true,
    })
    // 触发成功后进入恢复窗口；等 /health 重新 healthy 即完成。
    const done = await store.waitForRestoreDone()
    if (done) {
      restoreVisible.value = false
      message.success('恢复完成，请使用备份中的账号重新登录')
    } else {
      message.errorOnce('恢复超时或仍在进行，请刷新页面查看状态')
    }
  } catch (e) {
    const code = e.response?.data?.code
    if (e.response?.status === 409 || code === 'BACKUP_ALREADY_RUNNING') {
      message.warning('已有备份/恢复操作在运行，请稍候')
    } else {
      message.errorOnce(e.response?.data?.error || '恢复请求失败')
    }
  } finally {
    restoring.value = false
  }
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
.header-actions { display: flex; gap: 8px; }
.mono { font-family: ui-monospace, monospace; }
.muted { color: var(--text-secondary); }
.hint { margin-bottom: 12px; }
</style>
