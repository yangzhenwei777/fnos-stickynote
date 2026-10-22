<template>
  <div class="color-picker">
    <!-- SV 面板 + 垂直色相条 -->
    <div class="picker-main">
      <div
        class="sv-panel"
        :style="{ background: `hsl(${hue},100%,50%)` }"
        @mousedown="handleSVDown"
        @touchstart.prevent="handleSVDown"
      >
        <div class="sv-white" />
        <div class="sv-black" />
        <div
          class="sv-thumb"
          :style="{ left: sat + '%', top: (100 - val) + '%' }"
        />
      </div>
      <div
        class="hue-bar-vert"
        @mousedown="handleHueDown"
        @touchstart.prevent="handleHueDown"
      >
        <div
          class="hue-thumb-vert"
          :style="{ top: (hue / 360) * 100 + '%', background: curHex }"
        />
      </div>
    </div>

    <!-- 预设色块 -->
    <div class="preset-row">
      <button
        v-for="c in presets"
        :key="c"
        class="preset-swatch"
        :style="{ background: c }"
        :title="c"
        @click="applyColor(c)"
      />
    </div>

    <!-- HEX 输入 -->
    <div class="hex-row">
      <span class="label">HEX</span>
      <input
        v-model="hexInput"
        class="hex-field"
        maxlength="7"
        @input="handleHexInput"
        @blur="handleHexBlur"
      >
    </div>

    <!-- 透明度滑杆 -->
    <div class="alpha-row">
      <span class="label">透明度</span>
      <div class="alpha-slider-wrap">
        <div class="alpha-bg" />
        <div
          class="alpha-gradient"
          :style="{ background: `linear-gradient(to right, transparent, ${curHex})` }"
        />
        <input
          v-model.number="alpha"
          type="range"
          min="0"
          max="100"
          class="alpha-slider"
          @input="updateAlpha"
        >
      </div>
      <span class="alpha-val">{{ alpha }}%</span>
    </div>

    <!-- 最近使用 -->
    <div
      v-if="recentColors.length"
      class="recent-row"
    >
      <span class="label">最近使用</span>
      <div class="recent-list">
        <button
          v-for="(c, i) in recentColors"
          :key="i"
          class="recent-swatch"
          :style="{ background: c }"
          :title="c"
          @click="applyColor(c)"
        />
      </div>
    </div>

    <!-- 按钮 -->
    <div class="picker-footer">
      <el-button
        size="small"
        @click="reset"
      >
        重置
      </el-button>
      <el-button
        size="small"
        type="primary"
        @click="confirm"
      >
        应用
      </el-button>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'

const props = defineProps({
  modelValue: { type: String, default: '#000000' },
})

const emit = defineEmits(['update:modelValue', 'apply'])

const hue = ref(0)
const sat = ref(100)
const val = ref(100)
const alpha = ref(100)
const hexInput = ref('#000000')

const presets = ['#3B82F6', '#22C55E', '#EF4444', '#F97316', '#A855F7', '#EC4899']
const RECENT_KEY = 'recent-colors'
const recentColors = ref([])

onMounted(() => {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    if (raw) recentColors.value = JSON.parse(raw)
  } catch { /* ignore */ }
  parseColor(props.modelValue)
})

watch(() => props.modelValue, (v) => parseColor(v))

const curHex = computed(() => hsvToHex(hue.value, sat.value / 100, val.value / 100))
const curColor = computed(() => {
  if (alpha.value >= 100) return curHex.value
  const { r, g, b } = hexToRgb(curHex.value)
  return `rgba(${r},${g},${b},${(alpha.value / 100).toFixed(2)})`
})

function parseColor(c) {
  if (!c) return
  if (c.startsWith('rgba')) {
    const m = c.match(/[\d.]+/g)
    if (m && m.length >= 3) {
      const [r, g, b] = m.map(Number)
      const hsv = rgbToHsv(r, g, b)
      hue.value = hsv.h; sat.value = hsv.s; val.value = hsv.v
      hexInput.value = rgbToHex(r, g, b)
      alpha.value = m.length >= 4 ? Math.round(parseFloat(m[3]) * 100) : 100
    }
  } else if (c.startsWith('#')) {
    hexInput.value = c
    const { r, g, b } = hexToRgb(c)
    const hsv = rgbToHsv(r, g, b)
    hue.value = hsv.h; sat.value = hsv.s; val.value = hsv.v
    alpha.value = 100
  }
}

// ---- SV 面板交互（v21.28：修复拖动坐标 + 实时预览）----
// 拖动时事件挂在 document 上，currentTarget 会变，故 mousedown 时缓存面板 rect
let svRect = null

function pickSV(clientX, clientY) {
  if (!svRect) return
  const x = Math.max(0, Math.min(1, (clientX - svRect.left) / svRect.width))
  const y = Math.max(0, Math.min(1, (clientY - svRect.top) / svRect.height))
  sat.value = Math.round(x * 100)
  val.value = Math.round((1 - y) * 100)
  hexInput.value = curHex.value
  emitPreview()
}

function handleSVDown(e) {
  svRect = e.currentTarget.getBoundingClientRect()
  const t = e.touches?.[0]
  pickSV(t ? t.clientX : e.clientX, t ? t.clientY : e.clientY)
  const onMove = (ev) => {
    ev.preventDefault()
    const tt = ev.touches?.[0]
    pickSV(tt ? tt.clientX : ev.clientX, tt ? tt.clientY : ev.clientY)
  }
  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
    document.removeEventListener('touchmove', onMove)
    document.removeEventListener('touchend', onUp)
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
  document.addEventListener('touchmove', onMove, { passive: false })
  document.addEventListener('touchend', onUp)
}

// ---- 色相条（垂直，v21.28：支持按住拖动 + 实时预览）----
let hueRect = null

function pickHue(clientY) {
  if (!hueRect) return
  const ratio = Math.max(0, Math.min(1, (clientY - hueRect.top) / hueRect.height))
  hue.value = Math.round(ratio * 360)
  if (hue.value >= 360) hue.value = 0
  hexInput.value = curHex.value
  emitPreview()
}

function handleHueDown(e) {
  hueRect = e.currentTarget.getBoundingClientRect()
  const t = e.touches?.[0]
  pickHue(t ? t.clientY : e.clientY)
  const onMove = (ev) => {
    ev.preventDefault()
    const tt = ev.touches?.[0]
    pickHue(tt ? tt.clientY : ev.clientY)
  }
  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
    document.removeEventListener('touchmove', onMove)
    document.removeEventListener('touchend', onUp)
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
  document.addEventListener('touchmove', onMove, { passive: false })
  document.addEventListener('touchend', onUp)
}

// ---- 实时预览（v21.28：拖动中实时回传颜色，不记最近使用）----
function emitPreview() {
  emit('update:modelValue', curColor.value)
}

// ---- HEX ----
function handleHexInput() {
  const v = hexInput.value
  if (!/^#[0-9A-Fa-f]{0,6}$/.test(v)) { hexInput.value = curHex.value; return }
  if (v.length === 7) {
    const { r, g, b } = hexToRgb(v)
    const hsv = rgbToHsv(r, g, b)
    hue.value = hsv.h; sat.value = hsv.s; val.value = hsv.v
  }
}

function handleHexBlur() {
  if (hexInput.value.length !== 7 || !/^#[0-9A-Fa-f]{6}$/.test(hexInput.value)) {
    hexInput.value = curHex.value
  }
}

function updateAlpha() {}

function applyColor(c) { parseColor(c); confirm() }

function reset() { hue.value = 0; sat.value = 100; val.value = 100; alpha.value = 100; hexInput.value = '#000000' }

function confirm() {
  const color = curColor.value
  addRecent(color)
  emit('update:modelValue', color)
  emit('apply', color)
}

function addRecent(c) {
  recentColors.value = [c, ...recentColors.value.filter(x => x !== c)].slice(0, 8)
  localStorage.setItem(RECENT_KEY, JSON.stringify(recentColors.value))
}

// ---- 颜色工具函数 ----
function hsvToHex(h, s, v) {
  const c = v * s; const x = c * (1 - Math.abs((h / 60) % 2 - 1)); const m = v - c
  let r, g, b
  if (h < 60) { r = c; g = x; b = 0 }
  else if (h < 120) { r = x; g = c; b = 0 }
  else if (h < 180) { r = 0; g = c; b = x }
  else if (h < 240) { r = 0; g = x; b = c }
  else if (h < 300) { r = x; g = 0; b = c }
  else { r = c; g = 0; b = x }
  r = Math.round((r + m) * 255); g = Math.round((g + m) * 255); b = Math.round((b + m) * 255)
  return '#' + [r, g, b].map(x => x.toString(16).padStart(2, '0')).join('')
}

function hexToRgb(hex) {
  const v = parseInt(hex.slice(1), 16)
  return { r: (v >> 16) & 255, g: (v >> 8) & 255, b: v & 255 }
}

function rgbToHex(r, g, b) {
  return '#' + [r, g, b].map(x => x.toString(16).padStart(2, '0')).join('')
}

function rgbToHsv(r, g, b) {
  const rn = r / 255; const gn = g / 255; const bn = b / 255
  const max = Math.max(rn, gn, bn); const min = Math.min(rn, gn, bn); const d = max - min
  let h = 0
  if (d !== 0) {
    if (max === rn) h = ((gn - bn) / d + 6) % 6
    else if (max === gn) h = (bn - rn) / d + 2
    else h = (rn - gn) / d + 4
    h = Math.round(h * 60)
  }
  const s = max === 0 ? 0 : Math.round((d / max) * 100)
  const v = Math.round(max * 100)
  return { h, s, v }
}
</script>

<style scoped>
.color-picker { padding: 12px; user-select: none; }

/* 主面板：SV + 色相条并排 */
.picker-main { display: flex; gap: 8px; }

/* SV 面板 */
.sv-panel {
  flex: 1; height: 140px; border-radius: var(--radius-md); position: relative; cursor: crosshair;
}
.sv-white {
  position: absolute; inset: 0; border-radius: var(--radius-md);
  background: linear-gradient(to right, #fff, transparent);
}
.sv-black {
  position: absolute; inset: 0; border-radius: var(--radius-md);
  background: linear-gradient(to top, #000, transparent);
}
.sv-thumb {
  position: absolute; width: 14px; height: 14px; border-radius: 50%;
  border: 2px solid #fff; box-shadow: 0 1px 4px rgba(0,0,0,.4);
  transform: translate(-50%, -50%); pointer-events: none; z-index: 1;
}

/* 垂直色相条 */
.hue-bar-vert {
  width: 14px; border-radius: 7px; cursor: pointer; position: relative; flex-shrink: 0;
  background: linear-gradient(to bottom, #f00, #ff0, #0f0, #0ff, #00f, #f0f, #f00);
}
.hue-thumb-vert {
  position: absolute; left: -3px; width: 20px; height: 20px; border-radius: 50%;
  border: 2px solid #fff; box-shadow: 0 1px 4px rgba(0,0,0,.3); transform: translateY(-50%);
  pointer-events: none;
}
.preset-row { display: flex; gap: 8px; margin-top: 12px; }
.preset-swatch {
  width: 28px; height: 28px; border-radius: var(--radius-sm); border: 1px solid var(--border-light, #e0e0e0);
  cursor: pointer; transition: transform .15s;
}
.preset-swatch:hover { transform: scale(1.15); }
.hex-row { display: flex; align-items: center; gap: 8px; margin-top: 12px; }
.label { font-size: 12px; color: var(--text-muted, #909399); min-width: 36px; }
.hex-field {
  flex: 1; height: 28px; border: 1px solid var(--border-light, #e0e0e0); border-radius: var(--radius-sm);
  padding: 0 8px; font-size: 13px; font-family: monospace; outline: none;
}
.hex-field:focus { border-color: var(--el-color-primary, #3B82F6); }
.alpha-row { display: flex; align-items: center; gap: 8px; margin-top: 10px; }
.alpha-slider-wrap { flex: 1; position: relative; height: 20px; border-radius: var(--radius-sm); overflow: hidden; }
.alpha-bg {
  position: absolute; inset: 0;
  background-image:
    linear-gradient(45deg, #ccc 25%, transparent 25%),
    linear-gradient(-45deg, #ccc 25%, transparent 25%),
    linear-gradient(45deg, transparent 75%, #ccc 75%),
    linear-gradient(-45deg, transparent 75%, #ccc 75%);
  background-size: 8px 8px; background-position: 0 0, 0 4px, 4px -4px, -4px 0;
}
.alpha-gradient { position: absolute; inset: 0; border-radius: var(--radius-sm); pointer-events: none; }
.alpha-slider { position: absolute; top: 0; left: 0; width: 100%; height: 100%; opacity: 0; cursor: pointer; }
.alpha-val { font-size: 12px; color: var(--text-muted); min-width: 32px; text-align: right; }
.recent-row { margin-top: 12px; }
.recent-list { display: flex; gap: 6px; margin-top: 4px; flex-wrap: wrap; }
.recent-swatch {
  width: 24px; height: 24px; border-radius: var(--radius-sm); border: 1px solid var(--border-light);
  cursor: pointer; transition: transform .15s;
}
.recent-swatch:hover { transform: scale(1.2); }
.picker-footer { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; padding-top: 10px; border-top: 1px solid var(--border-light, #e0e0e0); }
</style>
