import { ref, computed } from 'vue'

/**
 * useNoteSearch — 搜索/标签筛选/防抖（从 NotesView.vue 拆分，P2-1）
 */
export function useNoteSearch(onSearch) {
  const searchQuery = ref('')
  const activeTag = ref('')
  const allTags = ref([])

  let searchTimer = null

  // v18.8: 短关键词提示（1-2 字走后端 LIKE 模糊匹配而非 FTS5）
  const showShortQueryHint = computed(() => {
    const q = searchQuery.value.trim()
    return q.length > 0 && q.length < 3
  })

  function debouncedSearch() {
    clearTimeout(searchTimer)
    searchTimer = setTimeout(() => onSearch(searchQuery.value, activeTag.value), 300)
  }

  function toggleTag(tag) {
    activeTag.value = activeTag.value === tag ? '' : tag
    onSearch(searchQuery.value, activeTag.value)
  }

  function clearTag() {
    activeTag.value = ''
    onSearch(searchQuery.value, activeTag.value)
  }

  return {
    searchQuery, activeTag, allTags, showShortQueryHint,
    debouncedSearch, toggleTag, clearTag
  }
}
