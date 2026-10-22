<script setup>
// NotePager —— 笔记列表分页（1.0.218 视觉改版，按用户参考图三段式）。
//
// 仅数字窗 + 「全部」两件套：移除每页条数/跳页/总数（总数在页面头部「共 N 条笔记」已可见）。
// 「全部」模式：数据侧由父组件分批拉全（本组件只切按钮文案「全部 ⇄ 分页」）；
// 数字窗规则见 @/utils/pager.js（纯函数 + 单测）。
import { computed } from 'vue'
import { buildPagerItems } from '@/utils/pager'

const props = defineProps({
  total: { type: Number, required: true },
  pageSize: { type: Number, required: true },
  current: { type: Number, required: true },
  allMode: { type: Boolean, default: false }
})
const emit = defineEmits(['page-change', 'toggle-all'])

const totalPages = computed(() =>
  Math.max(1, Math.ceil((Number(props.total) || 0) / Math.max(1, Number(props.pageSize) || 1))))
const items = computed(() => buildPagerItems(totalPages.value, props.current))
</script>

<template>
  <nav
    class="note-pager"
    aria-label="分页"
  >
    <template v-if="!allMode">
      <template
        v-for="(it, i) in items"
        :key="it.type === 'page' ? 'p' + it.page : 'e' + it.id + i"
      >
        <span
          v-if="it.type === 'ellipsis'"
          class="np-ellipsis"
        >…</span>
        <button
          v-else
          class="np-page"
          :class="{ active: it.page === current }"
          :aria-current="it.page === current ? 'page' : undefined"
          @click="emit('page-change', it.page)"
        >
          {{ it.page }}
        </button>
      </template>
    </template>
    <button
      class="np-all"
      :class="{ active: allMode }"
      @click="emit('toggle-all', !allMode)"
    >
      {{ allMode ? '分页' : '全部' }}
    </button>
  </nav>
</template>

<style scoped>
.note-pager {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-wrap: wrap;
  gap: 6px 10px;
}

.np-page,
.np-all {
  background: none;
  border: 0;
  padding: 4px 8px;
  font-size: 14px;
  color: var(--text-secondary);
  cursor: pointer;
  border-radius: var(--radius-sm, 6px);
  transition: color 0.15s, background-color 0.15s;
}

.np-page:hover,
.np-all:hover {
  color: var(--primary-color);
  background: var(--primary-light);
}

.np-page.active {
  color: var(--primary-color);
  font-weight: 700;
}

.np-ellipsis {
  color: var(--text-muted);
  font-size: 14px;
  padding: 0 2px;
  user-select: none;
}

.np-all {
  margin-left: 6px;
  font-weight: 600;
}

.np-all.active {
  color: var(--primary-color);
}

@media (max-width: 768px) {
  .note-pager {
    gap: 4px 6px;
  }

  .np-page,
  .np-all {
    font-size: 13px;
    padding: 4px 6px;
  }
}
</style>
