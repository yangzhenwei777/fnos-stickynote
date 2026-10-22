<template>
  <div>
    <h3>备份主密钥</h3>
    <el-alert
      type="warning"
      :closable="false"
      show-icon
      class="hint"
      title="主密钥是解开所有备份归档的唯一钥匙，一旦丢失将无法恢复任何备份；服务器磁盘损坏也可能一并丢失。请立刻导出并离线妥善保管（如密码管理器）。它不存放在数据库里，故不会随备份一起自我循环。"
    />

    <div class="kid-line">
      当前密钥标识 (kid)：<span class="mono">{{ kid || '（首次备份时自动生成）' }}</span>
    </div>

    <el-form
      label-width="110px"
      class="key-form"
    >
      <el-form-item label="当前账号密码">
        <el-input
          v-model="password"
          type="password"
          show-password
          placeholder="导出 / 导入 / 再生成都需二次校验口令"
          autocomplete="off"
        />
      </el-form-item>
    </el-form>

    <div class="actions">
      <el-button
        type="primary"
        :loading="exporting"
        @click="handleExport"
      >
        导出密钥文件
      </el-button>

      <el-button @click="importVisible = true">
        导入密钥文件
      </el-button>

      <el-button
        type="danger"
        plain
        @click="regenVisible = true"
      >
        再生成主密钥
      </el-button>
    </div>

    <!-- 导入 -->
    <el-dialog
      v-model="importVisible"
      title="导入备份主密钥"
      width="560px"
    >
      <p class="muted">
        用于换盘/同机重建后接管历史归档。导入会覆盖当前主密钥，
        之后既有归档才可校验/下载；新备份将用导入的密钥加密。
      </p>
      <el-input
        v-model="importContent"
        type="textarea"
        :rows="6"
        placeholder="粘贴 backup_master_key.txt 的内容"
      />
      <template #footer>
        <el-button @click="importVisible = false">
          取消
        </el-button>
        <el-button
          type="primary"
          :loading="importing"
          @click="handleImport"
        >
          导入
        </el-button>
      </template>
    </el-dialog>

    <!-- 再生成 -->
    <el-dialog
      v-model="regenVisible"
      title="再生成主密钥（危险）"
      width="480px"
    >
      <el-alert
        type="error"
        :closable="false"
        show-icon
        title="生成全新主密钥后，所有历史备份归档将永久无法解密！仅在确定要作废历史备份时使用。目标凭据会自动用新密钥重新加密，无需重填密码。"
      />
      <el-checkbox
        v-model="regenConfirm"
        class="regen-confirm"
      >
        我已知悉历史归档会作废
      </el-checkbox>
      <template #footer>
        <el-button @click="regenVisible = false">
          取消
        </el-button>
        <el-button
          type="danger"
          :disabled="!regenConfirm"
          :loading="regening"
          @click="handleRegen"
        >
          确认再生成
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useBackupStore } from '@/stores/backup'
import * as backupApi from '@/api/backup'
import { message } from '@/utils/message'
import { extractApiError } from '@/utils/apiError'

const store = useBackupStore()
const password = ref('')
const kid = ref('')
const exporting = ref(false)
const importing = ref(false)
const regening = ref(false)
const importVisible = ref(false)
const regenVisible = ref(false)
const importContent = ref('')
const regenConfirm = ref(false)

async function handleExport() {
  if (!password.value) { message.warning('请输入当前账号密码'); return }
  exporting.value = true
  try {
    await store.exportKey(password.value)
    message.success('密钥文件已开始下载，请离线妥善保管')
  } catch (e) {
    message.errorOnce(await extractApiError(e))
  } finally {
    exporting.value = false
  }
}

async function handleImport() {
  if (!password.value || !importContent.value) { message.warning('需粘贴密钥文件并输入账号密码'); return }
  importing.value = true
  try {
    const { data } = await backupApi.importKey(importContent.value, password.value)
    kid.value = data.kid
    message.success(data.message || '已导入')
    importVisible.value = false
    importContent.value = ''
  } catch (e) {
    message.errorOnce(await extractApiError(e))
  } finally {
    importing.value = false
  }
}

async function handleRegen() {
  if (!password.value) { message.warning('请输入当前账号密码'); return }
  regening.value = true
  try {
    const { data } = await backupApi.regenerateKey(password.value, true)
    kid.value = data.kid
    message.success(data.message || '已生成新主密钥')
    regenVisible.value = false
    regenConfirm.value = false
    store.refresh()
  } catch (e) {
    message.errorOnce(await extractApiError(e))
  } finally {
    regening.value = false
  }
}
</script>

<style scoped>
h3 { margin: 0 0 12px; }
.hint { margin-bottom: 14px; }
.kid-line { margin-bottom: 14px; }
.mono { font-family: ui-monospace, monospace; }
.key-form { max-width: 420px; }
.actions {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.regen-confirm { margin-top: 12px; }
.muted {
  color: var(--text-secondary);
  font-size: 13px;
}
</style>
