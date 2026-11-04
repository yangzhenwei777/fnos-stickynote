<template>
  <div class="panel-header">
    <el-icon
      :size="40"
      class="panel-icon"
    >
      <QuestionFilled />
    </el-icon>
    <div>
      <h3>安全问题</h3>
      <p>设置用于找回密码的安全问题和答案</p>
    </div>
  </div>
  <el-form
    :model="securityForm"
    class="form-wrapper"
    @submit.prevent
  >
    <div class="form-group">
      <label class="group-label">当前密码</label>
      <div class="input-box">
        <el-icon class="input-box-icon">
          <Lock />
        </el-icon>
        <el-input
          v-model="securityForm.current_password"
          type="password"
          placeholder="请输入当前密码"
          show-password
          @keyup.enter="focusAnswer"
        />
      </div>
    </div>
    <div class="form-group">
      <label class="group-label">安全问题</label>
      <div class="input-box">
        <el-icon class="input-box-icon">
          <QuestionFilled />
        </el-icon>
        <el-input
          v-model="securityForm.question"
          placeholder="例如：您第一所学校的名称？"
          @keyup.enter="focusAnswer"
        />
      </div>
    </div>
    <div class="form-group">
      <label class="group-label">安全答案</label>
      <div class="input-box">
        <el-icon class="input-box-icon">
          <Key />
        </el-icon>
        <el-input
          v-model="securityForm.answer"
          type="password"
          placeholder="请输入安全答案"
          show-password
          @keyup.enter="focusConfirmAnswer"
        />
      </div>
    </div>
    <div class="form-group">
      <label class="group-label">确认答案</label>
      <div class="input-box">
        <el-icon class="input-box-icon">
          <Key />
        </el-icon>
        <el-input
          v-model="securityForm.confirm_answer"
          type="password"
          placeholder="请再次输入安全答案"
          show-password
          @keyup.enter="handleSecurityChange"
        />
      </div>
    </div>
    <el-button
      type="primary"
      class="submit-btn"
      :loading="securityLoading"
      @click="handleSecurityChange"
    >
      确认修改
    </el-button>
  </el-form>
</template>

<script setup>
import { ref, nextTick } from 'vue'
import { updateSecurityQuestion } from '../../api/auth'
import { message } from '@/utils/message'
import { Lock, Key, QuestionFilled } from '@element-plus/icons-vue'

const securityLoading = ref(false)
const securityForm = ref({
  // v21.90 H-02：修改安全问题需验证当前密码（敏感操作二次认证）
  current_password: '',
  question: '',
  answer: '',
  confirm_answer: ''
})

async function focusAnswer() {
  await nextTick()
  const inputs = document.querySelectorAll('.input-box')
  if (inputs[1]) {
    const input = inputs[1].querySelector('input')
    input?.focus()
  }
}

async function focusConfirmAnswer() {
  await nextTick()
  const inputs = document.querySelectorAll('.input-box')
  if (inputs[2]) {
    const input = inputs[2].querySelector('input')
    input?.focus()
  }
}

async function handleSecurityChange() {
  if (!securityForm.value.current_password) {
    message.warning('请输入当前密码')
    return
  }
  if (!securityForm.value.question || !securityForm.value.answer || !securityForm.value.confirm_answer) {
    message.warning('请填写完整安全问题信息')
    return
  }
  if (securityForm.value.answer !== securityForm.value.confirm_answer) {
    message.warning('两次输入的安全答案不一致')
    return
  }
  securityLoading.value = true
  try {
    await updateSecurityQuestion({
      current_password: securityForm.value.current_password,
      security_question: securityForm.value.question,
      security_answer: securityForm.value.answer
    })
    message.success('安全问题修改成功')
    securityForm.value = { current_password: '', question: '', answer: '', confirm_answer: '' }
  } catch (e) {
    message.error(e.response?.data?.error || '安全问题修改失败')
  } finally {
    securityLoading.value = false
  }
}
</script>
