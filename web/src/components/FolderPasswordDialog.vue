<template>
  <NotePasswordDialog
    v-model="visible"
    v-model:input="sec.input"
    v-model:confirm="sec.confirm"
    :mode="sec.mode"
    :title="sec.title"
    :placeholder="sec.placeholder"
    :warning-text="sec.warningText"
    :loading="sec.loading"
    :is-mobile="isMobile"
    @submit="sec.submit"
  />
</template>

<script setup>
import { computed, ref } from 'vue'
import NotePasswordDialog from './NotePasswordDialog.vue'
import { useFolderSecurityStore } from '@/stores/folderSecurity'

// v2.3.0 文件夹访问锁：全局单例弹框（App.vue 挂载），加密/解密/解锁共用。
const sec = useFolderSecurityStore()
const isMobile = ref(window.innerWidth <= 768)

const visible = computed({
  get: () => sec.visible,
  set: (v) => { if (!v) sec.close() }
})
</script>
