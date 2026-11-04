<template>
  <Teleport to="body">
    <div
      class="slash-menu"
      :style="{ top: top + 'px', left: left + 'px' }"
      role="listbox"
      @mousedown.prevent
    >
      <div class="slash-menu-title">
        插入内容
      </div>
      <button
        v-for="(item, i) in items"
        :key="item.id"
        type="button"
        class="slash-menu-item"
        :class="{ active: i === activeIndex }"
        role="option"
        :aria-selected="i === activeIndex"
        @mousedown.prevent="$emit('select', item)"
        @mouseenter="$emit('hover', i)"
      >
        <span class="slash-menu-hint">{{ item.hint }}</span>
        <span class="slash-menu-label">{{ item.label }}</span>
      </button>
    </div>
  </Teleport>
</template>

<script setup>
defineProps({
  items: { type: Array, default: () => [] },
  activeIndex: { type: Number, default: 0 },
  top: { type: Number, default: 0 },
  left: { type: Number, default: 0 }
})
defineEmits(['select', 'hover'])
</script>

<style scoped>
.slash-menu {
  position: fixed;
  z-index: 3000;
  width: 208px;
  max-height: 320px;
  overflow-y: auto;
  padding: 6px;
  background: var(--bg-primary, #fff);
  border: 1px solid var(--border-color, #e4e7ed);
  border-radius: 10px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.14);
}

.slash-menu-title {
  padding: 4px 8px 6px;
  font-size: 12px;
  color: var(--text-muted, #909399);
}

.slash-menu-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 7px 8px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--text-primary, #303133);
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}

.slash-menu-item.active {
  background: var(--bg-secondary, #f5f7fa);
  color: var(--primary-color, #409eff);
}

.slash-menu-hint {
  flex: 0 0 28px;
  text-align: center;
  font-family: var(--font-code, monospace);
  font-size: 12px;
  color: var(--text-muted, #909399);
}

.slash-menu-item.active .slash-menu-hint {
  color: var(--primary-color, #409eff);
}

.slash-menu-label {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
