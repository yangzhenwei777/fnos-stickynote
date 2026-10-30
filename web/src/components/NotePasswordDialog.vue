<template>
  <el-dialog
    v-model="visible"
    :title="title"
    :width="isMobile ? '95%' : '400px'"
    :close-on-click-modal="false"
  >
    <template v-if="mode === 'encrypt'">
      <!-- v21.107 P2-16：加密不可恢复警示——零知识设计此前只字未提，用户以为管理员能帮忙找回 -->
      <!-- v2.3.0：文案改为可注入（文件夹访问锁与笔记加密的语义不同） -->
      <p style="margin:0 0 12px;font-size:12px;line-height:1.6;color:var(--el-color-warning,#e6a23c)">
        {{ warningText || '重要：笔记加密后服务器只保存密文，忘记密码内容将无法恢复，管理员也无法找回，请妥善保管密码。' }}
      </p>
      <el-form :label-width="isMobile ? 'auto' : '80px'">
        <el-form-item label="密码">
          <el-input
            v-model="inputVal"
            type="password"
            show-password
            placeholder="请输入密码"
          />
        </el-form-item>
        <el-form-item label="确认密码">
          <el-input
            v-model="confirmVal"
            type="password"
            show-password
            placeholder="请再次输入密码"
          />
        </el-form-item>
      </el-form>
    </template>
    <template v-else>
      <el-input
        v-model="inputVal"
        type="password"
        show-password
        :placeholder="placeholder"
        @keyup.enter="$emit('submit')"
      />
    </template>
    <template #footer>
      <el-button @click="visible = false">
        取消
      </el-button>
      <el-button
        type="primary"
        :loading="loading"
        @click="$emit('submit')"
      >
        确认
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup>
// NotesView 弹框组件化：统一密码弹框（查看/编辑/加密/解密/下载）的呈现层。
// 纯 presentational——状态与提交逻辑仍由父组件的 useNoteEncrypt composable 持有：
// 可见性经 v-model(modelValue)，两个输入框经 v-model:input / v-model:confirm 双向回传，
// 确认/回车经 @submit 冒泡给父组件的 handlePwdSubmit（内部注入 enterEditMode 回调）。
import { computed } from 'vue'

const props = defineProps({
  modelValue: {
    type: Boolean,
    default: false
  },
  mode: {
    type: String,
    default: 'view'
  },
  title: {
    type: String,
    default: ''
  },
  warningText: {
    type: String,
    default: ''
  },
  placeholder: {
    type: String,
    default: ''
  },
  loading: {
    type: Boolean,
    default: false
  },
  isMobile: {
    type: Boolean,
    default: false
  },
  input: {
    type: String,
    default: ''
  },
  confirm: {
    type: String,
    default: ''
  }
})

const emit = defineEmits(['update:modelValue', 'update:input', 'update:confirm', 'submit'])

const visible = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v)
})
const inputVal = computed({
  get: () => props.input,
  set: (v) => emit('update:input', v)
})
const confirmVal = computed({
  get: () => props.confirm,
  set: (v) => emit('update:confirm', v)
})
</script>
