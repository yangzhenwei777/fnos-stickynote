<script setup>
// FolderEditDialog —— 新建/重命名文件夹弹窗（2026-11-03 巨型 Vue 拆分 刀3，
// 自 AppSidebar.vue 原样迁出，零行为变化；创建成功后 emit('created') 供父组件展开文件夹列表）。
import { ref } from 'vue'
import { message } from '@/utils/message'
import { useFolderStore } from '@/stores/folder'

const emit = defineEmits(['created'])
const folderStore = useFolderStore()

const dialogVisible = ref(false)
const dialogMode = ref('create') // create | rename
const dialogInput = ref('')
const dialogLoading = ref(false)
let renameTarget = null

function openCreate() {
  dialogMode.value = 'create'
  dialogInput.value = ''
  dialogVisible.value = true
}

function openRename(f) {
  dialogMode.value = 'rename'
  renameTarget = f
  dialogInput.value = f.name
  dialogVisible.value = true
}

async function submitDialog() {
  const name = dialogInput.value.trim()
  if (!name) {
    message.error('文件夹名称不能为空')
    return
  }
  dialogLoading.value = true
  try {
    if (dialogMode.value === 'create') {
      const f = await folderStore.createNewFolder(name)
      if (f) {
        dialogVisible.value = false
        // v21.107 P1-11：创建后自动切入新文件夹——与移动端行为统一（此前 PC 停留原地易被误以为没生效）
        folderStore.selectFolder(f.id)
        emit('created', f)
        message.success('创建成功')
      }
    } else if (renameTarget) {
      const ok = await folderStore.renameFolderById(renameTarget.id, name)
      if (ok) {
        dialogVisible.value = false
        message.success('重命名成功')
      }
    }
  } finally {
    dialogLoading.value = false
  }
}

defineExpose({ openCreate, openRename })
</script>

<template>
  <el-dialog
    v-model="dialogVisible"
    :title="dialogMode === 'create' ? '新建文件夹' : '重命名文件夹'"
    width="400px"
    :close-on-click-modal="false"
    append-to-body
  >
    <el-input
      v-model="dialogInput"
      :placeholder="dialogMode === 'create' ? '请输入文件夹名称' : '请输入新名称'"
      maxlength="32"
      show-word-limit
      clearable
      @keyup.enter="submitDialog"
    />
    <template #footer>
      <el-button @click="dialogVisible = false">
        取消
      </el-button>
      <el-button
        type="primary"
        :loading="dialogLoading"
        @click="submitDialog"
      >
        确定
      </el-button>
    </template>
  </el-dialog>
</template>
