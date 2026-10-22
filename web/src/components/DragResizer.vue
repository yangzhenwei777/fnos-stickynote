<template>
  <!-- v21.86 竖向分割线：鼠标按住拖动自定义相邻侧栏宽度（min/max 钳制）。
       移动端由父组件控制不渲染（触屏拖动易误触）。 -->
  <div
    class="drag-resizer"
    title="拖动调整宽度"
    @mousedown="onMouseDown"
  />
</template>

<script setup>
const props = defineProps({
  min: { type: Number, default: 200 },
  max: { type: Number, default: 600 },
  modelValue: { type: Number, required: true }
})
const emit = defineEmits(['update:modelValue', 'resize-end'])

function onMouseDown(e) {
  e.preventDefault()
  const startX = e.clientX
  const startWidth = props.modelValue
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
  const onMove = (ev) => {
    const w = Math.min(props.max, Math.max(props.min, startWidth + ev.clientX - startX))
    emit('update:modelValue', Math.round(w))
  }
  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    emit('resize-end')
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
}
</script>

<style scoped>
/* 默认透明（热区），hover/拖动时高亮主题色——
   避免与相邻侧栏 border-right 形成双线 */
.drag-resizer {
  width: 5px;
  flex-shrink: 0;
  cursor: col-resize;
  background: transparent;
  transition: background 0.2s;
  z-index: 10;
}

.drag-resizer:hover,
.drag-resizer:active {
  background: var(--primary-color);
}
</style>
