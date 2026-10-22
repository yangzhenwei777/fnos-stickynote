<template>
  <!-- v21.87 笔记【移动到/复制到】文件夹选择弹框。
       列表 = 我的笔记（默认，folder_id 为 NULL 的虚拟项）+ 自定义文件夹；
       打开时默认勾选笔记当前所在文件夹（用户需求 Q2：笔记在"我的笔记"时默认勾在其后）。 -->
  <el-dialog
    :model-value="modelValue"
    :title="title"
    width="400px"
    class="folder-picker-dialog"
    :close-on-click-modal="false"
    append-to-body
    @update:model-value="$emit('update:modelValue', $event)"
  >
    <div class="fp-list">
      <div
        class="fp-item"
        :class="{ active: selected === 'default' }"
        @click="selected = 'default'"
      >
        <el-icon class="fp-item-icon">
          <Document />
        </el-icon>
        <span class="fp-name">我的笔记</span>
        <el-icon
          v-if="selected === 'default'"
          class="fp-check"
        >
          <Check />
        </el-icon>
      </div>
      <div
        v-for="f in folders"
        :key="f.id"
        class="fp-item"
        :class="{ active: selected === String(f.id) }"
        @click="selected = String(f.id)"
      >
        <el-icon class="fp-item-icon">
          <Folder />
        </el-icon>
        <span class="fp-name">{{ f.name }}</span>
        <el-icon
          v-if="selected === String(f.id)"
          class="fp-check"
        >
          <Check />
        </el-icon>
      </div>
      <div
        v-if="folders.length === 0"
        class="fp-empty"
      >
        暂无自定义文件夹
      </div>
    </div>
    <template #footer>
      <el-button @click="$emit('update:modelValue', false)">
        取消
      </el-button>
      <el-button
        type="primary"
        :loading="loading"
        @click="submit"
      >
        确定
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, watch } from 'vue'
import { Document, Folder, Check } from '@element-plus/icons-vue'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '选择文件夹' },
  folders: { type: Array, default: () => [] },
  // 笔记当前所在文件夹：null=默认"我的笔记"，数字=自定义文件夹 id
  currentFolderId: { type: Number, default: null },
  loading: { type: Boolean, default: false }
})

const emit = defineEmits(['update:modelValue', 'confirm'])

const selected = ref('default')

// 打开弹框（或挂载即打开）时，重置选中项为笔记当前文件夹（用户需求 Q2）。
// immediate: true — mount 时 modelValue 已为 true 的场景（测试/直接挂载）也能正确初始化。
watch(
  () => [props.modelValue, props.currentFolderId],
  ([v]) => {
    if (v) {
      selected.value = props.currentFolderId == null ? 'default' : String(props.currentFolderId)
    }
  },
  { immediate: true }
)

function submit() {
  // 'default' → null（后端 folder_id IS NULL 语义）；数字字符串 → Number
  const folderId = selected.value === 'default' ? null : Number(selected.value)
  emit('confirm', folderId)
}
</script>

<style scoped>
/* el-dialog append-to-body → Teleport 到 body，scoped 不生效，须用 :global 兜底 */
:global(.folder-picker-dialog .fp-list) {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 320px;
  overflow-y: auto;
}

:global(.folder-picker-dialog .fp-item) {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--text-primary);
  border: 1px solid var(--border-light);
  transition: all 0.2s;
}

:global(.folder-picker-dialog .fp-item:hover) {
  background: var(--bg-tertiary);
}

:global(.folder-picker-dialog .fp-item.active) {
  background: var(--primary-light);
  border-color: var(--primary-color);
  color: var(--primary-color);
  font-weight: 600;
}

:global(.folder-picker-dialog .fp-item-icon) {
  font-size: 16px;
  flex-shrink: 0;
}

:global(.folder-picker-dialog .fp-name) {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
}

:global(.folder-picker-dialog .fp-check) {
  color: var(--primary-color);
  flex-shrink: 0;
}

:global(.folder-picker-dialog .fp-empty) {
  padding: 16px;
  text-align: center;
  color: var(--text-muted);
  font-size: 13px;
}
</style>
