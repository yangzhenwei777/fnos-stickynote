<script setup>
// ConflictCenterCard —— 批次 8-Alt 刀 2：冲突中心简化版（§137~§139 的用户可见面）。
//
// 设计约束（与刀 1 数据面同源，勿在此重定义语义）：
//   - 处置按钮只渲染 Go 侧 `allowed_resolutions` 给的方式（后端仍独立校验，不信任 UI）；
//   - 破坏性处置（接受远端 / 放弃这笔）必须二次确认，明确写出"会丢什么"；
//   - 另存副本复用既有 CreateNote 绑定（新 sync_id 新建 → 红线 27 天然满足）；
//   - 处置后做**局部刷新**：本机库未换代，不像便携导入那样需要整页 reload；
//   - 桥不可用（Web 构建/未注入）时整卡不渲染，由父级 v-if 控制。
import { ref, computed, onMounted } from 'vue'
import { ElMessageBox } from 'element-plus'
import { message } from '@/utils/message'
import {
  syncStatus, syncNow, conflictList, outboxList, conflictResolve,
  RESOLUTION_LABELS, KIND_LABELS, formatConflictError,
  summarizeConflictPayload, conflictCopyCandidate, conflictSaveCopy
} from '@/api/local/desktopApi'

const PAGE = 50
const loading = ref(false)
const busyId = ref(0)
const onlyOpen = ref(true)
// 加载失败必须**留在页面上**：真机走查坐实——只弹 toast 时 3 秒后消失，概况条显示
// "待推送 — 笔"，用户读到的是"健康且为空"，而实际是三个只读调用根本没成功。
const loadError = ref('')
const status = ref({})
const conflicts = ref([])
const total = ref(0)
const expanded = ref(0)
const outbox = ref([])
const outboxCounts = ref({})
const outboxTotal = ref(0)
const outboxFilter = ref('open')

const OUTBOX_FILTERS = {
  open: 'PENDING,SENDING,FAILED,CONFLICT',
  acked: 'ACKED',
  all: ''
}
const OUTBOX_STATUS_LABELS = {
  PENDING: '待推送', SENDING: '推送中', FAILED: '失败待重试', CONFLICT: '冲突停手', ACKED: '已同步'
}

const hasMore = computed(() => conflicts.value.length < total.value)
const hasMoreOutbox = computed(() => outbox.value.length < outboxTotal.value)
const openCount = computed(() => Number(status.value.conflict_count || 0))
const summaryLine = computed(() => {
  const s = status.value || {}
  const bits = []
  bits.push(`待推送 ${s.pending_ops ?? '—'} 笔`)
  bits.push(`待处置冲突 ${s.conflict_count ?? '—'} 条`)
  if ((s.conflict_total ?? 0) > (s.conflict_count ?? 0)) bits.push(`历史已处置 ${s.conflict_total} 条`)
  if (s.cursor !== undefined && s.cursor !== '') bits.push(`同步游标 ${s.cursor}`)
  if (s.last_sync_at) bits.push(`最近同步 ${s.last_sync_at}`)
  return bits.join(' · ')
})

function kindLabel (k) {
  return KIND_LABELS[k] || `未知冲突类型（${k}）`
}
function resolutionLabel (r) {
  return RESOLUTION_LABELS[r] || r
}
function entityLabel (t) {
  return { NOTE: '笔记', FOLDER: '文件夹', FOLDER_ORDER: '文件夹排序' }[t] || '实体'
}
function copyOf (c) {
  return conflictCopyCandidate(c.local_payload)
}

async function loadAll () {
  loading.value = true
  try {
    const [st, cf, ob] = await Promise.all([
      syncStatus(),
      conflictList(onlyOpen.value, PAGE, 0),
      outboxList(OUTBOX_FILTERS[outboxFilter.value] || '', PAGE, 0)
    ])
    status.value = st || {}
    conflicts.value = (cf && cf.items) || []
    total.value = (cf && cf.total) || 0
    outbox.value = (ob && ob.items) || []
    outboxCounts.value = (ob && ob.counts) || {}
    outboxTotal.value = (ob && ob.total) || 0
    expanded.value = 0
    loadError.value = ''
  } catch (e) {
    const msg = formatConflictError(e)
    loadError.value = msg
    message.error(msg)
    // 失败时清空列表：留着上一次的数字会让人误读为"当前状态"
    conflicts.value = []
    outbox.value = []
    total.value = 0
    outboxTotal.value = 0
  } finally {
    loading.value = false
  }
}

const syncing = ref(false)

// 立即同步：跑完一轮就重拉三个只读面，让"待推送 N 笔"当场归零，而不是等下一个空闲周期。
async function onSyncNow () {
  syncing.value = true
  try {
    const r = await syncNow()
    if (r && r.error) {
      message.error(formatConflictError(new Error(r.error)))
    } else {
      message.success(`已同步：推送 ${r?.pushed ?? 0} 笔 · 应用 ${r?.applied ?? 0} 条 · 冲突 ${r?.conflicts ?? 0} 条`)
    }
    await loadAll()
  } catch (e) {
    message.error(formatConflictError(e))
  } finally {
    syncing.value = false
  }
}

async function loadMore () {
  try {
    const cf = await conflictList(onlyOpen.value, PAGE, conflicts.value.length)
    conflicts.value = conflicts.value.concat((cf && cf.items) || [])
  } catch (e) {
    message.error(formatConflictError(e))
  }
}

async function loadMoreOutbox () {
  try {
    const ob = await outboxList(OUTBOX_FILTERS[outboxFilter.value] || '', PAGE, outbox.value.length)
    outbox.value = outbox.value.concat((ob && ob.items) || [])
    outboxTotal.value = (ob && ob.total) || outboxTotal.value
  } catch (e) {
    message.error(formatConflictError(e))
  }
}

async function onFilterChange (key) {
  outboxFilter.value = key
  await loadAll()
}

// confirmResolve 破坏性后果由这里逐条写清（禁"确定吗"式无信息确认）。
function confirmText (c, resolution) {
  if (resolution === 'keep-remote') {
    return {
      title: '接受远端版本',
      body: `将用远端内容覆盖本机这一条${entityLabel(c.entity_type)}的未同步修改，本机改动的内容会丢失（可先"另存副本"保留）。远端：${summarizeConflictPayload(c.remote_payload)}`,
      confirm: '确认覆盖本机'
    }
  }
  if (resolution === 'drop-local') {
    return {
      title: '放弃本机这笔',
      body: `本机未推送的改动将不再尝试同步（内容留在本机，但不会再上传）。远端版本 ${c.remote_version} 会作为基点，下次编辑从它继续。`,
      confirm: '确认放弃'
    }
  }
  if (resolution === 'recreate') {
    return {
      title: '以本机内容重建到服务器',
      body: `将在服务器上以本机这条${entityLabel(c.entity_type)}的当前内容新建（沿用原同步编号，内容与本机一模一样）；远端本来就没有这条，不会覆盖任何内容。重建后会立即自动同步。`,
      confirm: '确认重建'
    }
  }
  return {
    title: '以本机为准重投',
    body: '本机内容保持不变并重新排队推送，以远端当前版本为基点（若远端在此期间又变化，可能再次冲突）。',
    confirm: '确认重投'
  }
}

async function onResolve (c, resolution) {
  const t = confirmText(c, resolution)
  let ok = true
  try {
    await ElMessageBox.confirm(t.body, t.title, {
      confirmButtonText: t.confirm,
      cancelButtonText: '取消',
      type: resolution === 'keep-local' || resolution === 'recreate' ? 'info' : 'warning'
    })
  } catch {
    ok = false
  }
  if (!ok) return
  busyId.value = c.id
  try {
    const res = await conflictResolve(c.id, resolution, resolution === 'keep-remote')
    if (resolution === 'recreate') {
      message.success('已重建到服务器：将以本机当前内容新建同步（稍候自动完成）')
    } else {
      const bits = []
      if (res.requeued_ops) bits.push(`重投 ${res.requeued_ops} 笔`)
      if (res.applied_remote) bits.push('已写入远端内容')
      if (res.terminated_ops) bits.push(`终止 ${res.terminated_ops} 笔`)
      message.success(`已处置：${bits.join(' · ') || '完成'}`)
    }
    await loadAll()
  } catch (e) {
    message.error(formatConflictError(e))
  } finally {
    busyId.value = 0
  }
}

async function onCopy (c) {
  busyId.value = c.id
  try {
    const res = await conflictSaveCopy(c.id)
    const where = res && res.folder_fallback
      ? '（原文件夹已不存在，已存入「我的笔记」）'
      : (res && res.folder_name ? `（已存回「${res.folder_name}」）` : '')
    message.success(`已另存为本机新笔记${where}（新身份，不会再以旧编号进入同步）`)
  } catch (e) {
    message.error(formatConflictError(e))
  } finally {
    busyId.value = 0
  }
}

function toggle (id) {
  expanded.value = expanded.value === id ? 0 : id
}

onMounted(loadAll)
defineExpose({ loadAll })
</script>

<template>
  <div class="conflict-center">
    <p class="cc-desc">
      离线期间本机改动与服务器上的新版本相撞时，会在这里列出并交给你决定，<strong>不会自动覆盖任何一方的内容</strong>。
    </p>
    <div class="cc-head">
      <span class="cc-summary">{{ summaryLine }}</span>
      <div class="cc-head-actions">
        <el-button
          size="small"
          type="primary"
          :loading="syncing"
          @click="onSyncNow"
        >
          立即同步
        </el-button>
        <el-button
          size="small"
          :loading="loading"
          @click="loadAll"
        >
          刷新
        </el-button>
      </div>
    </div>

    <el-alert
      v-if="loadError"
      class="cc-error"
      type="error"
      :closable="false"
      show-icon
      title="同步状态读取失败"
      :description="loadError"
    >
      <el-button
        size="small"
        style="margin-top: 8px"
        :loading="loading"
        @click="loadAll"
      >
        重试
      </el-button>
    </el-alert>

    <section class="cc-section">
      <div class="cc-section-head">
        <h4>同步冲突</h4>
        <label class="cc-switch">
          <input
            v-model="onlyOpen"
            type="checkbox"
            @change="loadAll"
          >
          <span>只看未处置</span>
        </label>
      </div>
      <p
        v-if="!conflicts.length"
        class="cc-empty"
      >
        {{ onlyOpen ? '当前没有需要你处理的冲突。' : '还没有产生过冲突记录。' }}
      </p>
      <ul class="cc-list">
        <li
          v-for="c in conflicts"
          :key="c.id"
          class="cc-item"
          :class="{ resolved: c.status !== 'open' }"
        >
          <div class="cc-item-main">
            <div class="cc-item-title">
              <span class="cc-kind">{{ kindLabel(c.kind) }}</span>
              <el-tag
                v-if="c.status !== 'open'"
                size="small"
                type="info"
              >
                已处置 · {{ resolutionLabel(c.resolution) }}
              </el-tag>
              <el-tag
                v-else-if="openCount"
                size="small"
                type="warning"
              >
                待处置
              </el-tag>
            </div>
            <div class="cc-item-meta">
              {{ entityLabel(c.entity_type) }} · {{ c.created_at }} · 未推送改动 {{ c.open_ops }} 笔
            </div>
          </div>
          <div class="cc-item-actions">
            <el-button
              size="small"
              text
              @click="toggle(c.id)"
            >
              {{ expanded === c.id ? '收起' : '详情' }}
            </el-button>
            <template v-if="c.status === 'open'">
              <el-button
                v-if="copyOf(c).ok"
                size="small"
                :disabled="busyId === c.id"
                @click="onCopy(c)"
              >
                另存副本
              </el-button>
              <el-button
                v-for="r in (c.allowed_resolutions || [])"
                :key="r"
                size="small"
                :type="(r === 'keep-local' || r === 'recreate') ? 'primary' : 'default'"
                :loading="busyId === c.id"
                :disabled="busyId === c.id"
                @click="onResolve(c, r)"
              >
                {{ resolutionLabel(r) }}
              </el-button>
            </template>
          </div>
          <div
            v-if="expanded === c.id"
            class="cc-detail"
          >
            <p
              v-if="!copyOf(c).ok && copyOf(c).reason"
              class="cc-hint"
            >
              {{ copyOf(c).reason }}
            </p>
            <div class="cc-detail-grid">
              <div>
                <h5>本机内容</h5>
                <p>{{ summarizeConflictPayload(c.local_payload) }}</p>
                <p class="cc-detail-ver">
                  基点版本 {{ c.base_version }}
                </p>
              </div>
              <div>
                <h5>远端内容</h5>
                <p>{{ summarizeConflictPayload(c.remote_payload) }}</p>
                <p class="cc-detail-ver">
                  远端版本 {{ c.remote_version }}
                </p>
              </div>
            </div>
          </div>
        </li>
      </ul>
      <el-button
        v-if="hasMore"
        size="small"
        text
        @click="loadMore"
      >
        加载更多（还有 {{ total - conflicts.length }} 条）
      </el-button>
    </section>

    <section class="cc-section">
      <div class="cc-section-head">
        <h4>本机挂账（尚未同步到服务器的改动）</h4>
        <div class="cc-filters">
          <button
            :class="{ active: outboxFilter === 'open' }"
            @click="onFilterChange('open')"
          >
            未同步 {{ (outboxCounts.PENDING || 0) + (outboxCounts.SENDING || 0) + (outboxCounts.FAILED || 0) + (outboxCounts.CONFLICT || 0) }}
          </button>
          <button
            :class="{ active: outboxFilter === 'acked' }"
            @click="onFilterChange('acked')"
          >
            已同步 {{ outboxCounts.ACKED || 0 }}
          </button>
          <button
            :class="{ active: outboxFilter === 'all' }"
            @click="onFilterChange('all')"
          >
            全部 {{ outboxTotal }}
          </button>
        </div>
      </div>
      <p
        v-if="!outbox.length"
        class="cc-empty"
      >
        没有记录。
      </p>
      <ul class="cc-list">
        <li
          v-for="o in outbox"
          :key="o.seq"
          class="cc-op"
        >
          <span class="cc-op-status">{{ OUTBOX_STATUS_LABELS[o.status] || o.status }}</span>
          <span>{{ entityLabel(o.entity_type) }} · {{ o.operation }}</span>
          <span class="cc-op-meta">
            基点 {{ o.base_version == null ? '—' : o.base_version }} · {{ o.payload_bytes }} B
            <template v-if="o.retry_count"> · 已重试 {{ o.retry_count }} 次</template>
            <template v-if="o.last_error"> · {{ o.last_error }}</template>
          </span>
        </li>
      </ul>
      <el-button
        v-if="hasMoreOutbox"
        size="small"
        text
        @click="loadMoreOutbox"
      >
        加载更多
      </el-button>
    </section>
  </div>
</template>

<style scoped>
.conflict-center {
  max-width: 860px;
}
.cc-desc {
  font-size: 13px;
  color: var(--text-muted);
  line-height: 1.7;
  margin: 8px 0 16px;
}
.cc-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}
.cc-head-actions {
  display: flex;
  gap: 8px;
}
.cc-summary {
  font-size: 13px;
  color: var(--text-muted);
}
.cc-error {
  margin-bottom: 4px;
}
.cc-section {
  margin-top: 20px;
}
.cc-section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  border-bottom: 1px solid var(--border-color, rgba(128, 128, 128, .2));
  padding-bottom: 8px;
  margin-bottom: 8px;
}
.cc-section-head h4 {
  margin: 0;
  font-size: 15px;
}
.cc-switch {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
  cursor: pointer;
}
.cc-filters {
  display: flex;
  gap: 8px;
}
.cc-filters button {
  background: none;
  border: 0;
  font-size: 13px;
  color: var(--text-muted);
  cursor: pointer;
  padding: 2px 6px;
  border-radius: var(--radius-sm);
}
.cc-filters button.active {
  color: var(--el-color-primary);
  background: var(--primary-light);
}
.cc-empty {
  font-size: 13px;
  color: var(--text-muted);
  margin: 12px 0;
}
.cc-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.cc-item {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  gap: 8px 12px;
  padding: 12px 0;
  border-bottom: 1px solid var(--border-color, rgba(128, 128, 128, .15));
}
.cc-item.resolved {
  opacity: .65;
}
.cc-item-main {
  flex: 1 1 320px;
}
.cc-item-title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.cc-kind {
  font-size: 14px;
}
.cc-item-meta {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 4px;
}
.cc-item-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.cc-detail {
  flex: 1 1 100%;
  background: var(--bg-tertiary, rgba(128, 128, 128, .08));
  border-radius: var(--radius-md);
  padding: 12px;
  margin-top: 4px;
}
.cc-detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 12px;
}
.cc-detail-grid h5 {
  margin: 0 0 4px;
  font-size: 12px;
  color: var(--text-muted);
}
.cc-detail-grid p {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  word-break: break-word;
}
.cc-detail-ver {
  font-size: 12px !important;
  color: var(--text-muted);
  margin-top: 4px !important;
}
.cc-hint {
  font-size: 12px;
  color: var(--el-color-warning, #e6a23c);
  margin: 0 0 8px;
}
.cc-op {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  align-items: baseline;
  font-size: 13px;
  padding: 8px 0;
  border-bottom: 1px solid var(--border-color, rgba(128, 128, 128, .12));
}
.cc-op-status {
  font-weight: 600;
}
.cc-op-meta {
  font-size: 12px;
  color: var(--text-muted);
}
</style>
