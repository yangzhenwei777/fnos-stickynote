<template>
  <div class="portable-card">
    <h3>便携备份</h3>
    <p class="portable-desc">
      将本机全部笔记、附件与未同步改动打包为加密的 <code>.snbak</code> 文件（口令保护，上限
      256MB）。可在另一台 Windows 设备导入继续离线使用；导入后未同步的修改会自动保留并待联网同步。
    </p>

    <div class="portable-actions">
      <el-button
        type="primary"
        plain
        :loading="exporting"
        @click="onExport"
      >
        导出便携备份
      </el-button>
      <el-button
        plain
        :loading="importing"
        @click="onImport"
      >
        从备份导入
      </el-button>
    </div>

    <el-alert
      v-if="notice"
      :title="notice.title"
      :type="notice.type"
      :description="notice.detail"
      show-icon
      :closable="true"
      class="portable-notice"
      @close="notice = null"
    />
  </div>
</template>

<script setup>
// PortableBackupCard —— 批次 6（P6）便携备份入口（v20 §106~§109/§148 桌面走查面）。
// 口令经 ElMessageBox.prompt 内存过路不落日志；导入成功后整页 reload：Go 侧已换库接管，
// 前端全部内存列表缓存立即作废，reload 是最小正确收口（P4 数据中心再做无刷局部刷新）。
import { ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import { message } from '@/utils/message'
import { portableExport, portableImport, formatPortableError, hardReloadHome } from '@/api/local/desktopApi'

const exporting = ref(false)
const importing = ref(false)
const notice = ref(null)

const PASSWORD_RULE = {
  inputType: 'password',
  inputPlaceholder: '备份口令（至少 6 位，导入时需要同一口令）',
  inputValidator: (v) => (String(v || '').length >= 6 ? true : '口令至少 6 位'),
  confirmButtonText: '确定',
  cancelButtonText: '取消'
}

async function askPassword (title) {
  try {
    const { value } = await ElMessageBox.prompt(title, '便携备份', PASSWORD_RULE)
    return String(value || '')
  } catch {
    return null // 用户取消
  }
}

function fmtMB (bytes) {
  return (Number(bytes || 0) / 1024 / 1024).toFixed(1) + ' MB'
}

async function onExport () {
  const pw = await askPassword('设置本次导出的备份口令')
  if (pw === null) return
  exporting.value = true
  try {
    const res = await portableExport(pw)
    if (res && res.cancelled) return
    message.success('便携备份已导出')
    notice.value = {
      type: 'success',
      title: `已导出 ${fmtMB(res.out_bytes)}：${res.path || ''}`,
      detail: `笔记 ${res.note_count} 篇 · 附件 ${res.attachments} 个 · 未同步改动 ${res.outbox_pending} 条 · 备份编号 ${res.backup_id}`
    }
  } catch (e) {
    message.error(formatPortableError(e))
  } finally {
    exporting.value = false
  }
}

async function onImport () {
  const pw = await askPassword('输入该备份文件的导出口令')
  if (pw === null) return
  importing.value = true
  try {
    const res = await portableImport(pw)
    if (res && res.cancelled) return
    if (res.rebind_required) {
      notice.value = {
        type: 'warning',
        title: '已导入（离线可看），但该备份来自另一台服务器',
        detail: '为避免覆盖服务器数据，未自动进入同步状态。请重新登录当前服务器后执行"重建同步"。'
      }
    }
    if (res.relogin_required) {
      notice.value = {
        type: 'warning',
        title: '导入完成，需要重新登录',
        detail: '本机没有可用的设备凭据（新设备）。登录一次后即可离线打开并继续同步。'
      }
    }
    message.success('导入完成，正在刷新')
    setTimeout(hardReloadHome, 600)
  } catch (e) {
    message.error(formatPortableError(e))
  } finally {
    importing.value = false
  }
}
</script>

<style scoped>
.portable-card {
  max-width: 560px;
}
.portable-desc {
  font-size: 13px;
  color: var(--text-muted);
  line-height: 1.7;
  margin: 8px 0 16px;
}
.portable-actions {
  display: flex;
  gap: 12px;
}
.portable-notice {
  margin-top: 16px;
}
</style>
