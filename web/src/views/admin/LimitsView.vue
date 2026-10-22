<template>
  <div class="admin-container">
    <div class="admin-header">
      <h2>配额与限额</h2>
    </div>

    <div class="limits-grid">
      <!-- 存储配额 -->
      <div class="limit-card">
        <div class="limit-header">
          <span class="limit-title">存储配额</span>
        </div>
        <p class="limit-desc">
          每用户上传文件总大小上限。设为 0 表示不限制。
        </p>
        <div class="limit-value">
          <span class="current">{{ storage.value }} MB</span>
          <el-button
            size="small"
            type="primary"
            @click="editLimit('max_storage_mb_per_user', storage)"
          >
            编辑
          </el-button>
        </div>
      </div>

      <!-- 内容上限 -->
      <div class="limit-card">
        <div class="limit-header">
          <span class="limit-title">单篇笔记上限</span>
        </div>
        <p class="limit-desc">
          用户每条笔记的大小上限，超出将无法保存。设为 0 表示不限制。
        </p>
        <div class="limit-value">
          <span class="current">{{ content.value }} MB</span>
          <el-button
            size="small"
            type="primary"
            @click="editLimit('max_note_content_kb', content)"
          >
            编辑
          </el-button>
        </div>
      </div>

      <!-- 笔记数上限 -->
      <div class="limit-card">
        <div class="limit-header">
          <span class="limit-title">笔记数上限</span>
        </div>
        <p class="limit-desc">
          每用户最多可创建的笔记数。
        </p>
        <div class="limit-value">
          <span class="current">{{ notes.value }} 条</span>
          <el-button
            size="small"
            type="primary"
            @click="editLimit('max_notes_per_user', notes)"
          >
            编辑
          </el-button>
        </div>
      </div>

      <!-- 注册开关 -->
      <div class="limit-card">
        <div class="limit-header">
          <span class="limit-title">注册开关</span>
        </div>
        <p class="limit-desc">
          控制新用户是否可以通过注册页面自行注册。
        </p>
        <div class="limit-value">
          <el-switch
            v-model="registerEnabled"
            active-text="开启"
            inactive-text="关闭"
            inline-prompt
            @change="toggleRegister"
          />
        </div>
      </div>

      <!-- 回收站清空天数（P2-4/需求 2026-08-06） -->
      <div class="limit-card">
        <div class="limit-header">
          <span class="limit-title">回收站清空时间</span>
        </div>
        <p class="limit-desc">
          笔记删除后在回收站保留的天数，到期自动物理清除。设为 0 表示永久保留（默认）。
        </p>
        <div class="limit-value">
          <span class="current">{{ retention.value === 0 ? '永久保留' : retention.value + ' 天' }}</span>
          <el-button
            size="small"
            type="primary"
            @click="editLimit('trash_retention_days', retention)"
          >
            设置
          </el-button>
        </div>
        <p
          v-if="retention.value > 0"
          class="limit-warning"
          style="color:#E6A23C;font-size:12px;margin:6px 0 0;"
        >
          ⚠️ 开启后超期笔记将自动物理删除，<b>不可恢复</b>（每 6 小时检查一次）
        </p>
      </div>
    </div>

    <!-- v21.92：完整配置项入口。
         本页上方 5 张卡片只是常用限额的快捷面板；系统里还有「应用标题」「跨域白名单」
         等配置项，只在 /admin-configs 提供编辑，而该页此前没有任何菜单入口。
         这里给出显式跳转，避免管理员（尤其非技术用户）根本不知道还有这些设置。 -->
    <div class="all-configs-entry">
      <el-button
        link
        type="primary"
        class="all-configs-link"
        @click="gotoAllConfigs"
      >
        查看全部配置项 →
      </el-button>
      <span class="all-configs-hint">（应用标题、跨域白名单等在此修改）</span>
    </div>

    <!-- 编辑弹窗 -->
    <el-dialog
      v-model="dialogVisible"
      title="修改配置"
      :width="isMobile ? '95%' : '400px'"
    >
      <p style="margin:0 0 12px;color:var(--text-secondary);">
        {{ editingLabel }}
      </p>
      <el-input-number
        v-model="editVal"
        :min="0"
        :step="editingStep"
        style="width:100%"
      />
      <p style="margin:8px 0 0;font-size:12px;color:var(--text-muted);">
        {{ editingHint }}
      </p>
      <template #footer>
        <el-button @click="dialogVisible = false">
          取消
        </el-button>
        <el-button
          type="primary"
          @click="saveEdit"
        >
          保存
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { getConfigs, updateConfig } from '../../api/config'
import { message } from '@/utils/message'

// v21.92：本页只暴露 5 个常用限额，另有「应用标题」「跨域白名单」等配置项
// 仅在 /admin-configs 可见，而该页此前没有任何界面入口（只能手敲网址）。
const router = useRouter()
function gotoAllConfigs() { router.push('/admin-configs') }

const storage = ref({ value: 1024, key: 'max_storage_mb_per_user' })
const content = ref({ value: 1024, key: 'max_note_content_kb' })
const notes = ref({ value: 1000, key: 'max_notes_per_user' })
const retention = ref({ value: 0, key: 'trash_retention_days' })
const registerEnabled = ref(true)

const dialogVisible = ref(false)
const editingKey = ref('')
const editingLabel = ref('')
const editingStep = ref(1)
const editingHint = ref('')
const editVal = ref(0)
const isMobile = ref(false)

onMounted(() => { checkMobile(); window.addEventListener('resize', checkMobile); loadLimits() })
onUnmounted(() => window.removeEventListener('resize', checkMobile))
function checkMobile() { isMobile.value = window.innerWidth <= 768 }

async function loadLimits() {
  try {
    const { data } = await getConfigs()
    for (const c of data.configs || []) {
      if (c.key === 'max_storage_mb_per_user') storage.value.value = parseInt(c.value) || 0
      if (c.key === 'max_note_content_kb') content.value.value = Math.round(parseInt(c.value) / 102.4) / 10 || 0
      if (c.key === 'max_notes_per_user') notes.value.value = parseInt(c.value) || 0
      if (c.key === 'trash_retention_days') retention.value.value = parseInt(c.value) || 0
      if (c.key === 'allow_register') registerEnabled.value = c.value === 'true'
    }
  } catch { /* silent */ }
}

function editLimit(key, refData) {
  editingKey.value = key
  editVal.value = refData.value
  const labels = {
    max_storage_mb_per_user: '存储配额（MB）',
    max_note_content_kb: '单篇笔记上限（MB）',
    max_notes_per_user: '笔记数上限（条）',
    trash_retention_days: '回收站清空天数（天）'
  }
  const steps = { max_storage_mb_per_user: 10, max_note_content_kb: 0.5, max_notes_per_user: 10, trash_retention_days: 7 }
  const hints = { max_storage_mb_per_user: '设为 0 表示不限制', max_note_content_kb: '设为 0 表示不限制（系统硬上限 100MB）', max_notes_per_user: '无上限限制', trash_retention_days: '0=永久保留；开启后超期笔记自动物理删除，不可恢复' }
  editingLabel.value = labels[key] || key
  editingStep.value = steps[key] || 1
  editingHint.value = hints[key] || ''
  dialogVisible.value = true
}

async function saveEdit() {
  try {
    const val = editingKey.value === 'max_note_content_kb' ? Math.round(editVal.value * 1024) : editVal.value
    await updateConfig(editingKey.value, String(val))
    message.success('配置更新成功')
    dialogVisible.value = false
    loadLimits()
  } catch (e) {
    message.error(e.response?.data?.error || '更新失败')
  }
}

async function toggleRegister(val) {
  try {
    await updateConfig('allow_register', val ? 'true' : 'false')
    message.success('注册已' + (val ? '开启' : '关闭'))
  } catch {
    registerEnabled.value = !val // 回退
    message.error('更新失败')
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
.admin-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
.admin-header h2 { margin: 0; }

.limits-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 16px; }

.limit-card {
  background: var(--card-bg);
  border-radius: var(--radius-lg);
  padding: 20px;
  border: 1px solid var(--border-color);
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.limit-header { display: flex; align-items: center; gap: 8px; }
.limit-icon { font-size: 20px; }
.limit-title { font-weight: 600; font-size: 15px; color: var(--text-primary); }
.limit-desc { margin: 0; font-size: 13px; color: var(--text-secondary); }
.limit-value { display: flex; justify-content: space-between; align-items: center; }
.current { font-size: 20px; font-weight: 600; color: var(--primary-color); }

/* v21.92 完整配置项入口：一行小字链接，不抢主内容视觉 */
.all-configs-entry {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px solid var(--border-light, #e5e7eb);
}
.all-configs-link { font-size: 13px; padding: 0; height: auto; }
.all-configs-hint { font-size: 12px; color: var(--text-muted); }

@media (max-width: 768px) {
  .admin-container { padding: 16px; }
  .limits-grid { grid-template-columns: 1fr; }
  .admin-header h2 { font-size: 18px; }
  .current { font-size: 16px; }
}
</style>
