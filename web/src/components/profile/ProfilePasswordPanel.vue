<template>
  <div class="panel-header">
    <el-icon
      :size="40"
      class="panel-icon"
    >
      <Key />
    </el-icon>
    <div>
      <h3>密码修改</h3>
      <p>修改您的登录密码</p>
    </div>
  </div>
  <el-form
    :model="passwordForm"
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
          v-model="passwordForm.current_password"
          type="password"
          placeholder="请输入当前密码"
          show-password
          @keyup.enter="focusNewPassword"
        />
      </div>
    </div>
    <div class="form-group">
      <label class="group-label">新密码</label>
      <div class="input-box">
        <el-icon class="input-box-icon">
          <Lock />
        </el-icon>
        <el-input
          v-model="passwordForm.new_password"
          type="password"
          placeholder="请输入新密码（至少 6 位）"
          show-password
          @keyup.enter="focusConfirmPassword"
        />
      </div>
      <PasswordStrengthBar :password="passwordForm.new_password" />
    </div>
    <div class="form-group">
      <label class="group-label">确认密码</label>
      <div class="input-box">
        <el-icon class="input-box-icon">
          <Lock />
        </el-icon>
        <el-input
          v-model="passwordForm.confirm_password"
          type="password"
          placeholder="请再次输入新密码"
          show-password
          @keyup.enter="handlePasswordChange"
        />
      </div>
    </div>
    <el-button
      type="primary"
      class="submit-btn"
      :loading="passwordLoading"
      @click="handlePasswordChange"
    >
      确认修改
    </el-button>
  </el-form>
</template>

<script setup>
import { ref, nextTick } from 'vue'
import { changePassword } from '../../api/auth'
import { message } from '@/utils/message'
import { Lock, Key } from '@element-plus/icons-vue'
import PasswordStrengthBar from '@/components/PasswordStrengthBar.vue'
import { updateAccessToken, updateRefreshToken } from '@/api/request'

const passwordLoading = ref(false)
const passwordForm = ref({
  current_password: '',
  new_password: '',
  confirm_password: ''
})

async function focusNewPassword() {
  await nextTick()
  const inputs = document.querySelectorAll('.input-box')
  if (inputs[1]) {
    const input = inputs[1].querySelector('input')
    input?.focus()
  }
}

async function focusConfirmPassword() {
  await nextTick()
  const inputs = document.querySelectorAll('.input-box')
  if (inputs[2]) {
    const input = inputs[2].querySelector('input')
    input?.focus()
  }
}

async function handlePasswordChange() {
  if (!passwordForm.value.current_password || !passwordForm.value.new_password || !passwordForm.value.confirm_password) {
    message.warning('请填写完整密码信息')
    return
  }
  if (passwordForm.value.new_password !== passwordForm.value.confirm_password) {
    message.warning('新密码与确认密码不一致')
    return
  }
  // v21.96：取消强密码强制执行——6 位是唯一硬下限，字符复杂度不再拦截
  if (passwordForm.value.new_password.length < 6) {
    message.warning('新密码至少 6 位')
    return
  }
  passwordLoading.value = true
  try {
    // v21.105 P0-2：改密成功后端已递增 token_version 并重签 token 对，
    // 必须同步本地 storage——否则下一个请求仍带旧 token 被 401 踢回登录页
    // （顺带修复：此前未解包 response，password_strength 恒 undefined）
    const { data } = await changePassword({
      current_password: passwordForm.value.current_password,
      new_password: passwordForm.value.new_password
    })
    if (data?.access_token) updateAccessToken(data.access_token)
    if (data?.refresh_token) updateRefreshToken(data.refresh_token)
    message.success('密码修改成功')
    if (data?.password_strength === 'weak') {
      message.warning('密码强度偏弱', '建议更换为更强的密码')
    }
    passwordForm.value = { current_password: '', new_password: '', confirm_password: '' }
  } catch (e) {
    message.error(e.response?.data?.error || '密码修改失败')
  } finally {
    passwordLoading.value = false
  }
}
</script>
