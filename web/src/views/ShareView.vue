<template>
  <div class="share-page">
    <header class="share-header">
      <span class="share-brand">Sticky note</span>
    </header>
    <main class="share-main">
      <div
        v-if="loading"
        class="share-state"
      >
        加载中…
      </div>
      <div
        v-else-if="error"
        class="share-state"
      >
        {{ error }}
      </div>
      <template v-else>
        <h1 class="share-title">
          {{ note.title || '无标题' }}
        </h1>
        <div class="share-meta">
          最后更新 {{ formatDate(note.updated_at) }}
        </div>
        <article
          class="share-content"
          v-html="sanitizedContent"
        />
      </template>
    </main>
    <footer class="share-footer">
      由 Sticky note 分享 · 只读
    </footer>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { getSharedNote } from '@/api/share'
import { sanitizeNoteHtml } from '@/utils/sanitize'
import { formatDate } from '@/utils/note'

const route = useRoute()
const loading = ref(true)
const error = ref('')
const note = ref(null)

const sanitizedContent = computed(() => sanitizeNoteHtml(note.value?.content || ''))

onMounted(async () => {
  const token = String(route.query.t || '')
  if (!token) {
    error.value = '分享链接无效'
    loading.value = false
    return
  }
  try {
    const { data } = await getSharedNote(token)
    note.value = data
    document.title = `${data.title || '分享笔记'} · Sticky note`
  } catch (e) {
    error.value = e?.response?.status === 404
      ? '分享链接不存在、已过期或已撤销'
      : '加载分享内容失败，请稍后再试'
  } finally {
    loading.value = false
  }
})
</script>

<style scoped>
.share-page {
  min-height: 100vh;
  background: var(--bg-primary);
  color: var(--text-primary);
  display: flex;
  flex-direction: column;
}

.share-header {
  padding: 20px 24px;
  border-bottom: 1px solid var(--border-color);
}

.share-brand {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-secondary);
}

.share-main {
  flex: 1;
  width: 100%;
  max-width: 760px;
  margin: 0 auto;
  padding: 40px 24px 64px;
  box-sizing: border-box;
}

.share-title {
  margin: 0 0 8px;
  font-size: 30px;
  line-height: 1.35;
  font-weight: 600;
  word-break: break-word;
}

.share-meta {
  margin-bottom: 28px;
  font-size: 13px;
  color: var(--text-muted);
}

.share-content {
  line-height: 1.8;
  font-size: 16px;
  overflow-wrap: anywhere;
}

.share-content :deep(h1),
.share-content :deep(h2),
.share-content :deep(h3) {
  margin: 1.2em 0 0.6em;
}

.share-content :deep(img) {
  max-width: 100%;
  height: auto;
  border-radius: 8px;
}

.share-content :deep(pre) {
  background: var(--bg-secondary);
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 16px;
  overflow-x: auto;
  font-family: var(--font-code, monospace);
}

.share-content :deep(table) {
  border-collapse: collapse;
  width: 100%;
  margin: 12px 0;
}

.share-content :deep(th),
.share-content :deep(td) {
  border: 1px solid var(--border-color);
  padding: 8px 12px;
}

.share-content :deep(blockquote) {
  border-left: 2px solid var(--primary-color);
  margin: 1em 0;
  padding: 2px 0 2px 14px;
  color: var(--text-secondary);
}

.share-state {
  padding: 80px 0;
  text-align: center;
  color: var(--text-muted);
}

.share-footer {
  padding: 16px 24px;
  border-top: 1px solid var(--border-color);
  text-align: center;
  font-size: 12px;
  color: var(--text-muted);
}

@media (max-width: 640px) {
  .share-main {
    padding: 24px 16px 48px;
  }

  .share-title {
    font-size: 24px;
  }
}
</style>
