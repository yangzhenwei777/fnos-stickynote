<!--
  SyncInitBanner · 换代/全量同步窗口的用户可见提示（v21.166 · 计划项 2 / X5 延伸）

  背景：首登 Bootstrap 换代（全量同步）实测 10~60s 波动，期间调度闸门关闭。
  此前只有「账号与设备」卡片有横幅，用户若停在列表页只会看到空列表，
  极易误判为"又坏了"并重复触发强制同步 —— **恰好放大了 205 刚修好的
  「首装列表恒空」缺陷的心理阴影**。

  设计（抽成组件而非各页复制粘贴）：
  ① **同源文案**：与 DesktopAccountCard 既有横幅同源同措辞，避免两处说法不一。
  ② **零新增绑定**：复用既有 `SyncPaused`（闸门）+ `gen_epoch`（换代代数，
     205 已透出），不新增任何 Wails 绑定方法。
  ③ **纯展示**：只读状态、不改同步语义；桥不可用（非桌面端）自动静默降级。
  ④ **超时可行动**：超过 thresholdMs 仍未完成 → 升级为可行动提示
    （给出「稍后手动刷新 / 查日志」出路），避免无限等待的困惑。

  判据优先级：闸门 paused 为准（换代窗口权威信号）；gen_epoch 仅用于超时计时，
  因为换代"已完成"的判定权在 Go 侧，前端不自行推断。
-->
<template>
  <el-alert
    v-if="visible"
    class="sync-init-banner"
    :type="slow ? 'warning' : 'info'"
    :closable="false"
    show-icon
    :title="title"
    :description="description"
  />
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { syncPaused, desktopBridgeAvailable, bootstrapStatus } from '@/api/local/desktopApi'

// thresholdMs：换代通常 10~60s，超过则升级为可行动提示（180s 与走查工装同口径）
const props = defineProps({
  thresholdMs: { type: Number, default: 180000 },
  pollMs: { type: Number, default: 2000 }
})

// 阶段中文映射（2.0.3 · P1-3）。键 = Go bootstrap.Phase* 常量值（只能追加不能改名）。
const PHASE_LABELS = {
  request: '正在向服务器请求快照',
  poll: '等待服务器生成快照',
  download: '正在下载快照',
  verify: '正在校验并解包数据',
  activate: '正在切换到新数据代',
  postprocess: '正在合并附件与身份信息',
  done: '同步完成'
}

const paused = ref(false)
const startedAt = ref(0)
// 阶段可见状态：{phase, since_ms}；null = Go 侧无记录（旧版本桥/未开跑）→ 退回纯窗口提示
const phase = ref(null)
// tick：Date.now() 不是响应式源，纯 computed 不会因时间流逝重算。
// 用秒级心跳驱动重渲染，否则"超时升级"永远不触发（首版即栽此坑，被 A3 用例当场抓住）。
const tick = ref(0)
let timer = null
let ticker = null

const visible = computed(() => paused.value)

const waitedSec = computed(() => {
  void tick.value // 心跳推动重算
  return Math.max(0, Math.floor((Date.now() - startedAt.value) / 1000))
})

const slow = computed(() => {
  void tick.value // 建立依赖：心跳推动重算
  return paused.value && props.thresholdMs > 0 &&
    Date.now() - startedAt.value > props.thresholdMs
})

const title = computed(() => {
  const base = slow.value ? '全量同步初始化较慢' : '正在初始化全量同步'
  const pl = PHASE_LABELS[phase.value?.phase]
  return pl ? `${base} · ${pl}` : base
})

const description = computed(() => {
  const wait = waitedSec.value > 0 ? `（已等待 ${waitedSec.value} 秒）` : ''
  if (slow.value) {
    return `首次同步的数据量较大时可能需要一两分钟${wait}。期间无需操作；若长时间无变化，可下拉刷新或在「账号与设备 → 排障协助」导出诊断包（含日志）。`
  }
  const pl = PHASE_LABELS[phase.value?.phase]
  if (pl) return `${pl}${wait}。换代期间同步请求会排队，完成后自动恢复，无需操作。`
  return `换代期间同步请求会排队（通常 10~60 秒），完成后自动恢复，无需操作。`
})

async function poll () {
  try {
    const p = Boolean(await syncPaused())
    if (p && !paused.value) {
      // 进入窗口：起表（用于超时判定）
      startedAt.value = Date.now()
    } else if (!p && paused.value) {
      startedAt.value = 0
      phase.value = null
    }
    paused.value = p
    // 窗口内顺带读阶段（旧版本桥无此绑定 → 静默降级为纯窗口提示）
    if (p) {
      try {
        const st = await bootstrapStatus()
        if (st && st.phase) phase.value = st
      } catch { /* 旧桥无绑定，降级 */ }
    }
  } catch {
    paused.value = false // 桥不可用/未登录 → 视为无窗口，不打扰
  }
}

onMounted(() => {
  if (!desktopBridgeAvailable()) return // Web 端不显示
  poll()
  timer = setInterval(poll, props.pollMs)
  ticker = setInterval(() => { tick.value++ }, 1000)
})

onUnmounted(() => {
  if (timer) { clearInterval(timer); timer = null }
  if (ticker) { clearInterval(ticker); ticker = null }
})
</script>

<style scoped>
.sync-init-banner {
  margin: 0 0 12px 0;
}
</style>