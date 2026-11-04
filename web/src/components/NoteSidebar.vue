<template>
  <!-- v21.56 P2-4：从 NotesView 拆出——侧栏搜索框（标签筛选区已按需求于前台隐藏） -->
  <div class="sidebar-search">
    <div
      class="search-box"
      :title="searchSyntaxHelp"
    >
      <el-icon class="search-icon">
        <Search />
      </el-icon>
      <el-input
        :model-value="searchQuery"
        placeholder="搜索笔记… 支持 tag: folder:"
        clearable
        class="search-input"
        @update:model-value="emit('update:search', $event)"
        @input="emit('search')"
      />
      <!-- v18.8: 短关键词提示（<3 字走 LIKE 模糊匹配） -->
      <div
        v-if="showShortQueryHint"
        class="short-query-hint"
      >
        少于 3 个字：使用模糊匹配，结果可能较多
      </div>
    </div>
  </div>
</template>

<script setup>
import { Search } from '@element-plus/icons-vue'

defineProps({
  searchQuery: { type: String, default: '' },
  showShortQueryHint: { type: Boolean, default: false },
})
const emit = defineEmits(['update:search', 'search'])

const searchSyntaxHelp = '搜索语法：tag:标签、folder:文件夹、sort:updated|created|title、since:2026-01-01、until:2026-12-31，多个条件用空格分隔'
</script>

<style src="@/styles/notes-view.css" scoped></style>
