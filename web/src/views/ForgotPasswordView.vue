<template>
  <div class="auth-container">
    <div class="auth-card">
      <h2>找回密码</h2>
      <p class="subtitle">
        通过安全问题重置密码
      </p>

      <el-steps
        :active="step"
        finish-status="success"
        style="margin-bottom: 30px"
      >
        <el-step title="输入用户名" />
        <el-step title="验证安全问题" />
        <el-step title="重置密码" />
      </el-steps>

      <el-form
        v-if="step === 0"
        :model="form"
        @submit.prevent
      >
        <el-form-item>
          <el-input
            ref="usernameInput"
            v-model="form.username"
            placeholder="请输入用户名"
            prefix-icon="User"
            size="large"
            @keyup.enter="checkUsername"
          />
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            :loading="loading"
            size="large"
            style="width: 100%"
            @click="checkUsername"
            @keyup.enter="checkUsername"
          >
            下一步
          </el-button>
        </el-form-item>
      </el-form>

      <el-form
        v-else-if="step === 1"
        :model="form"
        @submit.prevent
      >
        <div class="security-question-display">
          <el-icon><QuestionFilled /></el-icon>
          <span>{{ securityQuestion }}</span>
        </div>
        <!-- v21.94 P3-8：遗忘兜底引导——统一话术不泄露问题内容，忘记答案的用户
             需要明确知道可联系管理员重置密码，而不是卡在这一步 -->
        <p class="security-question-hint">
          忘记问题或答案？请联系管理员重置密码后重新登录设置
        </p>
        <el-form-item>
          <el-input
            ref="answerInput"
            v-model="form.security_answer"
            placeholder="请输入安全答案"
            size="large"
            @keyup.enter="verifyAnswer"
          />
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            :loading="loading"
            size="large"
            style="width: 100%"
            @click="verifyAnswer"
          >
            下一步
          </el-button>
        </el-form-item>
      </el-form>

      <el-form
        v-else
        :model="form"
        @submit.prevent
      >
        <el-form-item>
          <el-input
            ref="passwordInput"
            v-model="form.new_password"
            type="password"
            placeholder="新密码（至少6位）"
            prefix-icon="Lock"
            size="large"
            show-password
            @keyup.enter="handlePasswordEnter"
          />
          <PasswordStrengthBar :password="form.new_password" />
        </el-form-item>
        <el-form-item>
          <el-input
            v-model="form.confirm_password"
            type="password"
            placeholder="请再次输入新密码"
            prefix-icon="Lock"
            size="large"
            show-password
            @keyup.enter="resetPassword"
          />
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            :loading="loading"
            size="large"
            style="width: 100%"
            @click="resetPassword"
          >
            重置密码
          </el-button>
        </el-form-item>
      </el-form>

      <div class="auth-links">
        <router-link to="/login">
          返回登录
        </router-link>
      </div>
    </div>
  </div>
</template>

<script setup>
import { withBase } from '@/utils/appBase'
import { ref, nextTick, onMounted } from 'vue'
import { getSecurityQuestion, forgotPassword } from '../api/auth'
import api from '../api/request'
import { message } from '@/utils/message'
import PasswordStrengthBar from '@/components/PasswordStrengthBar.vue'

const loading = ref(false)
const step = ref(0)
const securityQuestion = ref('')
const usernameInput = ref(null)
const answerInput = ref(null)
const passwordInput = ref(null)
const form = ref({
  username: '',
  security_answer: '',
  new_password: '',
  confirm_password: ''
})

onMounted(async () => {
  await nextTick()
  usernameInput.value?.focus()
})

async function checkUsername() {
  if (!form.value.username) {
    message.warning('请输入用户名')
    return
  }
  loading.value = true
  try {
    const { data } = await getSecurityQuestion(form.value.username)
    // v21.93 P3：后端对任何用户名都返回**统一话术**（防账号枚举），
    // 不再回传真实问题文本——此处的 security_question 恒为同一句提示。
    if (!data.security_question) {
      message.error('未获取到安全验证信息，请稍后重试')
      return
    }
    securityQuestion.value = data.security_question
    step.value = 1
    await nextTick()
    form.value.security_answer = ''
    answerInput.value?.focus()
  } catch (e) {
    // v21.107 P2-14：兜底文案改网络异常——「用户不存在」既误导（无响应≠用户不存在）
    // 又破坏后端刻意设计的防枚举统一话术
    message.error(e.response?.data?.error || '网络异常，请稍后重试')
  } finally {
    loading.value = false
  }
}

async function verifyAnswer() {
  if (!form.value.security_answer) {
    message.warning('请输入安全答案')
    return
  }
  loading.value = true
  try {
    await api.post('/auth/verify-answer', {
      username: form.value.username,
      security_answer: form.value.security_answer
    })
    step.value = 2
    await nextTick()
    form.value.new_password = ''
    form.value.confirm_password = ''
    passwordInput.value?.focus()
  } catch (e) {
    message.error(e.response?.data?.error || '安全答案错误')
  } finally {
    loading.value = false
  }
}

function handlePasswordEnter() {
  // v21.96：6 位是唯一硬下限，字符复杂度不再拦截
  if (!form.value.new_password || form.value.new_password.length < 6) {
    message.warning('密码至少 6 位')
    return
  }
  document.querySelector('input[placeholder="请再次输入新密码"]')?.focus()
}

async function resetPassword() {
  if (!form.value.new_password || form.value.new_password.length < 6) {
    message.warning('密码至少 6 位')
    return
  }
  if (form.value.new_password !== form.value.confirm_password) {
    message.warning('两次输入的密码不一致')
    return
  }
  loading.value = true
  try {
    // v21.107 P2-2：解包 axios response（拦截器返回完整 response），password_strength 提示此前恒不显示
    const { data } = await forgotPassword({
      username: form.value.username,
      security_answer: form.value.security_answer,
      new_password: form.value.new_password
    })
    message.success('密码重置成功')
    if (data?.password_strength === 'weak') {
      message.warning('密码强度偏弱', '建议更换为更强的密码')
    }
    window.location.href = withBase('/login')
  } catch (e) {
    message.error(e.response?.data?.error || '密码重置失败')
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.auth-container {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg-primary);
  padding: 20px;
}

.auth-card {
  width: 100%;
  max-width: 400px;
  background: var(--bg-primary);
  padding: 40px 30px;
  border-radius: var(--radius-xl);
  border: 1px solid var(--border-color);
}

.auth-card h2 {
  text-align: center;
  margin-bottom: 8px;
  color: var(--text-primary);
}

.auth-card .subtitle {
  text-align: center;
  color: var(--text-secondary);
  margin-bottom: 30px;
  font-size: 14px;
}

.security-question-display {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 16px;
  background: var(--bg-secondary);
  border-radius: var(--radius-md);
  margin-bottom: 20px;
  color: #303133;
}

/* v21.94 P3-8：遗忘兜底引导文字（弱化样式，不抢主视觉） */
.security-question-hint {
  margin: -12px 0 16px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.6;
}

.auth-links {
  text-align: center;
  margin-top: 24px;
}

.auth-links a {
  color: var(--primary-color);
  text-decoration: none;
  font-size: 14px;
}

.auth-links a:hover {
  text-decoration: underline;
}

@media (max-width: 768px) {
  .auth-card {
    padding: 30px 20px;
    border-radius: var(--radius-xl);
  }
}
</style>
