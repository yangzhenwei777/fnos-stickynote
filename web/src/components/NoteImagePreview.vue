<template>
  <!-- v21.179 图片查看器（原 NoteImagePreview 全量重写）：对标 sspai 图片交互——
       点击弹原图 / 滚轮锚点缩放 / 拖拽平移 / 左右旋转 / 下载 / 缩略图条开关 /
       ←→ 切换 / Esc·点空白·按钮关闭。查看态与编辑内预览共用。
       （1.0.219：编辑内预览此前无放大能力，用户 218 内测实机反馈后补齐。） -->
  <Teleport to="body">
    <div
      v-if="visible && current"
      ref="root"
      class="image-viewer"
      tabindex="-1"
      @keydown.esc="close"
      @keydown.left.prevent="prev"
      @keydown.right.prevent="next"
    >
      <!-- 主图舞台：滚轮缩放 + 拖拽平移 + 点击空白关闭 -->
      <div
        ref="stage"
        class="iv-stage"
        :class="{ dragging }"
        @wheel.prevent="onWheel"
        @pointerdown="onPointerDown"
        @pointermove="onPointerMove"
        @pointerup="onPointerUp"
        @pointercancel="onPointerUp"
        @click="onStageClick"
      >
        <img
          ref="imgEl"
          :src="current"
          class="iv-img"
          :style="imgStyle"
          draggable="false"
          alt="图片预览"
        >
      </div>

      <!-- 右上角工具栏：左旋 / 右旋 / 下载 / 缩略图开关 / 关闭 -->
      <div class="iv-toolbar">
        <button
          class="iv-btn"
          aria-label="向左旋转"
          title="向左旋转"
          @click="rotateLeft"
        >
          <el-icon :size="18">
            <RefreshLeft />
          </el-icon>
        </button>
        <button
          class="iv-btn"
          aria-label="向右旋转"
          title="向右旋转"
          @click="rotateRight"
        >
          <el-icon :size="18">
            <RefreshRight />
          </el-icon>
        </button>
        <button
          class="iv-btn"
          aria-label="下载图片"
          title="下载图片"
          @click="download"
        >
          <el-icon :size="18">
            <Download />
          </el-icon>
        </button>
        <button
          v-if="list.length > 1"
          class="iv-btn"
          :class="{ 'is-active': showThumbs }"
          aria-label="切换缩略图"
          title="切换缩略图"
          @click="showThumbs = !showThumbs"
        >
          <el-icon :size="18">
            <Grid />
          </el-icon>
        </button>
        <button
          class="iv-btn"
          aria-label="关闭预览"
          title="关闭预览"
          @click="close"
        >
          <el-icon :size="18">
            <Close />
          </el-icon>
        </button>
      </div>

      <!-- 左右切换（多图时） -->
      <template v-if="list.length > 1">
        <button
          class="iv-nav iv-nav-prev"
          aria-label="上一张"
          @click="prev"
        >
          <el-icon :size="26">
            <ArrowLeftBold />
          </el-icon>
        </button>
        <button
          class="iv-nav iv-nav-next"
          aria-label="下一张"
          @click="next"
        >
          <el-icon :size="26">
            <ArrowRightBold />
          </el-icon>
        </button>
      </template>

      <!-- 底部缩略图条：文档全部图片横向列表，点击切换、当前高亮（可开关） -->
      <div
        v-if="list.length > 1 && showThumbs"
        class="iv-thumbs"
      >
        <img
          v-for="(u, i) in list"
          :key="i"
          ref="thumbEls"
          :src="u"
          class="iv-thumb"
          :class="{ 'is-active': i === index }"
          draggable="false"
          alt="缩略图"
          @click="setIndex(i)"
        >
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { ref, computed, watch, nextTick, onBeforeUnmount } from 'vue'
import {
  RefreshLeft, RefreshRight, Download, Grid, Close, ArrowLeftBold, ArrowRightBold
} from '@element-plus/icons-vue'

const props = defineProps({
  visible: { type: Boolean, default: false },
  list: { type: Array, default: () => [] },
  index: { type: Number, default: 0 },
  title: { type: String, default: '' },
})
const emit = defineEmits(['update:visible', 'update:index'])

const root = ref(null)
const stage = ref(null)
const imgEl = ref(null)
const thumbEls = ref([])

const current = computed(() => props.list[props.index] || '')

// 变换状态：scale=1 即「适屏（contain）」基准；rotate 仅 90° 倍数；tx/ty 为平移像素
const scale = ref(1)
const rotate = ref(0)
const tx = ref(0)
const ty = ref(0)
const showThumbs = ref(true)
const dragging = ref(false)

const MIN_SCALE = 0.2
const MAX_SCALE = 5

const imgStyle = computed(() => ({
  transform: `translate(${tx.value}px, ${ty.value}px) rotate(${rotate.value}deg) scale(${scale.value})`,
}))

function resetTransform () {
  scale.value = 1
  rotate.value = 0
  tx.value = 0
  ty.value = 0
}

function close () {
  emit('update:visible', false)
}

function setIndex (i) {
  if (i === props.index || i < 0 || i >= props.list.length) return
  emit('update:index', i)
}

function prev () {
  if (props.list.length < 2) return
  setIndex((props.index - 1 + props.list.length) % props.list.length)
}

function next () {
  if (props.list.length < 2) return
  setIndex((props.index + 1) % props.list.length)
}

function scrollThumbIntoView () {
  const el = thumbEls.value?.[props.index]
  el?.scrollIntoView?.({ inline: 'center', block: 'nearest', behavior: 'smooth' })
}

// 打开：重置状态 + 锁定 body 滚动（同步段，不依赖 DOM）；焦点与缩略图定位等 nextTick
let bodyOverflowBak = ''
let bodyLocked = false
function lockBody () {
  if (bodyLocked) return
  bodyOverflowBak = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  bodyLocked = true
}
function releaseBody () {
  if (bodyLocked) {
    document.body.style.overflow = bodyOverflowBak || ''
    bodyLocked = false
  }
}

watch(() => props.visible, async (v) => {
  if (v) {
    resetTransform()
    showThumbs.value = true
    lockBody()
    await nextTick()
    root.value?.focus()
    scrollThumbIntoView()
  } else {
    releaseBody()
  }
})

onBeforeUnmount(releaseBody)

// 切图：重置变换 + 缩略图居中 + 预加载下一张
watch(() => props.index, () => {
  resetTransform()
  nextTick(scrollThumbIntoView)
  const nl = props.list[(props.index + 1) % props.list.length]
  if (nl && nl !== current.value && typeof Image !== 'undefined') {
    const im = new Image()
    im.src = nl
  }
})

// ---- 滚轮缩放：以鼠标位置为锚 ----
// t' = t + (1-k)·(M - C - t)，k = s'/s —— 旋转矩阵与各向同性缩放可交换，任意旋转角下成立
function onWheel (e) {
  if (!current.value) return
  const factor = Math.exp(-e.deltaY * 0.0015)
  const ns = Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale.value * factor))
  if (ns === scale.value) return
  const st = stage.value
  const k = ns / scale.value
  if (st) {
    const rect = st.getBoundingClientRect()
    const cx = rect.left + rect.width / 2
    const cy = rect.top + rect.height / 2
    const ux = e.clientX - cx - tx.value
    const uy = e.clientY - cy - ty.value
    tx.value += (1 - k) * ux
    ty.value += (1 - k) * uy
  }
  scale.value = ns
}

// ---- 拖拽平移（pointer capture；位移阈值判定「拖拽 vs 点击」） ----
let dragStart = null
let moved = false
let lastDragMoved = false

function onPointerDown (e) {
  if (e.button !== 0 || !current.value) return
  dragStart = { x: e.clientX, y: e.clientY, tx: tx.value, ty: ty.value }
  moved = false
  dragging.value = true
  try { stage.value?.setPointerCapture(e.pointerId) } catch { /* WebView2/旧内核兜底：无 capture 也可拖 */ }
}

function onPointerMove (e) {
  if (!dragStart) return
  const dx = e.clientX - dragStart.x
  const dy = e.clientY - dragStart.y
  if (!moved && (Math.abs(dx) > 4 || Math.abs(dy) > 4)) moved = true
  if (moved) {
    tx.value = dragStart.tx + dx
    ty.value = dragStart.ty + dy
  }
}

function onPointerUp (e) {
  if (!dragStart) return
  try { stage.value?.releasePointerCapture(e.pointerId) } catch { /* 同上 */ }
  lastDragMoved = moved
  dragStart = null
  dragging.value = false
}

// 点击：拖拽过不关闭；点图片不关闭；点空白关闭
function onStageClick (e) {
  if (lastDragMoved) { lastDragMoved = false; return }
  if (e.target?.closest?.('.iv-img')) return
  close()
}

// ---- 旋转：90° 步进；旋转后重置平移，超出舞台则自动缩到适屏 ----
function rotateLeft () { applyRotate((rotate.value + 270) % 360) }
function rotateRight () { applyRotate((rotate.value + 90) % 360) }

function applyRotate (r) {
  rotate.value = r
  tx.value = 0
  ty.value = 0
  const st = stage.value
  const img = imgEl.value
  if (!st || !img) return
  const w = img.offsetWidth
  const h = img.offsetHeight
  if (!w || !h) return
  // 90°/270° 时视觉包围盒宽高互换
  const rw = r % 180 === 0 ? w : h
  const rh = r % 180 === 0 ? h : w
  const fit = Math.min(st.clientWidth / rw, st.clientHeight / rh)
  if (fit < scale.value) scale.value = Math.max(MIN_SCALE, fit)
}

// ---- 下载（同源直下；文件名 = 笔记标题-序号.扩展名） ----
function download () {
  const url = current.value
  if (!url) return
  const ext = /\.([a-zA-Z0-9]+)(?:[?#]|$)/.exec(url)?.[1] || 'png'
  const safeTitle = (props.title || '图片').replace(/[\\/:*?"<>|\s]+/g, '_').slice(0, 40)
  const a = document.createElement('a')
  a.href = url
  a.download = `${safeTitle}-${props.index + 1}.${ext}`
  document.body.appendChild(a)
  a.click()
  a.remove()
}

// 供组件测试断言（生产代码勿依赖）
defineExpose({ scale, rotate, tx, ty, showThumbs })
</script>

<style scoped>
.image-viewer {
  position: fixed;
  inset: 0;
  z-index: 3000;
  background: rgba(0, 0, 0, 0.92);
  outline: none;
  user-select: none;
  overflow: hidden;
}

/* 主图舞台 */
.iv-stage {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 64px 88px;
  cursor: zoom-out;
  touch-action: none;
}
.iv-stage.dragging { cursor: grabbing; }

.iv-img {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
  cursor: grab;
  border-radius: 4px;
  transform-origin: center center;
  will-change: transform;
  -webkit-user-drag: none;
  user-select: none;
}
.iv-stage.dragging .iv-img { cursor: grabbing; }

/* 右上角工具栏 */
.iv-toolbar {
  position: absolute;
  top: 16px;
  right: 20px;
  display: flex;
  gap: 10px;
  z-index: 2;
}
.iv-btn {
  width: 40px;
  height: 40px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.14);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition: background 0.15s ease;
}
.iv-btn:hover { background: rgba(255, 255, 255, 0.3); }
.iv-btn.is-active { background: rgba(255, 255, 255, 0.34); }

/* 左右切换 */
.iv-nav {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  width: 48px;
  height: 48px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.12);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  z-index: 2;
  transition: background 0.15s ease;
}
.iv-nav:hover { background: rgba(255, 255, 255, 0.3); }
.iv-nav-prev { left: 20px; }
.iv-nav-next { right: 20px; }

/* 底部缩略图条 */
.iv-thumbs {
  position: absolute;
  left: 50%;
  bottom: 16px;
  transform: translateX(-50%);
  max-width: min(92vw, 900px);
  display: flex;
  gap: 8px;
  padding: 8px 10px;
  background: rgba(0, 0, 0, 0.55);
  border-radius: 10px;
  overflow-x: auto;
  z-index: 2;
}
.iv-thumb {
  width: 64px;
  height: 64px;
  object-fit: cover;
  border-radius: 6px;
  border: 2px solid transparent;
  opacity: 0.55;
  cursor: pointer;
  flex: 0 0 auto;
  transition: opacity 0.15s ease, border-color 0.15s ease;
}
.iv-thumb:hover { opacity: 0.85; }
.iv-thumb.is-active { opacity: 1; border-color: #fff; }

/* 窄屏适配 */
@media (max-width: 640px) {
  .iv-stage { padding: 56px 12px; }
  .iv-toolbar { top: 10px; right: 10px; gap: 6px; }
  .iv-btn { width: 36px; height: 36px; }
  .iv-nav { width: 40px; height: 40px; }
  .iv-nav-prev { left: 8px; }
  .iv-nav-next { right: 8px; }
  .iv-thumbs { max-width: 94vw; bottom: 10px; gap: 6px; }
  .iv-thumb { width: 48px; height: 48px; }
}
</style>
