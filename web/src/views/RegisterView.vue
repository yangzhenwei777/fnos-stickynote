<template>
  <div class="register-container">
    <div class="register-bg" />
    <div class="register-wrapper">
      <div class="register-card">
        <div class="register-left">
          <div class="brand-section">
            <div class="brand-icon">
              <el-icon :size="56">
                <EditPen />
              </el-icon>
            </div>
            <h1 class="brand-title">
              {{ isSetup ? '设置管理员' : '创建账号' }}
            </h1>
            <p class="brand-desc">
              {{ isSetup ? '首次使用，请设置管理员账号' : '开始记录您的每一个灵感' }}
            </p>
          </div>
          <div class="welcome-content">
            <div class="welcome-icon">
              <el-icon :size="80">
                <Star />
              </el-icon>
            </div>
            <h3>{{ isSetup ? '欢迎使用' + config.siteTitle : '加入我们' }}</h3>
            <p>{{ isSetup ? '设置管理员后即可开始使用' : '创建一个账号，享受完整的笔记体验' }}</p>
            <div class="benefits">
              <div class="benefit-item">
                <el-icon><CircleCheck /></el-icon>
                <span>数据云端同步</span>
              </div>
              <div class="benefit-item">
                <el-icon><CircleCheck /></el-icon>
                <span>多设备支持</span>
              </div>
              <div class="benefit-item">
                <el-icon><CircleCheck /></el-icon>
                <span>安全加密存储</span>
              </div>
            </div>
          </div>
        </div>
        <div class="register-right">
          <div class="form-header">
            <h2>{{ isSetup ? '设置管理员' : '开始注册' }}</h2>
            <p>{{ isSetup ? '请设置管理员账号和密码' : '只需几步即可完成注册' }}</p>
          </div>
          <el-form
            :model="form"
            class="register-form"
            label-width="0"
            @submit.prevent
          >
            <el-form-item>
              <div class="input-wrapper">
                <el-icon class="input-icon">
                  <User />
                </el-icon>
                <el-input 
                  v-model="form.username" 
                  :placeholder="isSetup ? '请输入管理员用户名' : '请输入用户名'"
                  size="large"
                  class="custom-input"
                  @keyup.enter="focusPassword"
                />
              </div>
            </el-form-item>
            <el-form-item>
              <div class="input-wrapper">
                <el-icon class="input-icon">
                  <Lock />
                </el-icon>
                <el-input
                  v-model="form.password"
                  type="password"
                  :placeholder="isSetup ? '请输入管理员密码' : '请输入密码'"
                  size="large"
                  show-password
                  class="custom-input"
                  @keyup.enter="focusConfirmPassword"
                />
              </div>
              <PasswordStrengthBar :password="form.password" />
            </el-form-item>
            <el-form-item>
              <div class="input-wrapper">
                <el-icon class="input-icon">
                  <Lock />
                </el-icon>
                <el-input 
                  v-model="form.password_confirm" 
                  type="password" 
                  placeholder="请再次输入密码" 
                  size="large"
                  show-password
                  class="custom-input"
                  @keyup.enter="focusSecurityQuestion"
                />
              </div>
            </el-form-item>
            <el-form-item>
              <div class="input-wrapper">
                <el-icon class="input-icon">
                  <QuestionFilled />
                </el-icon>
                <el-input 
                  v-model="form.security_question" 
                  placeholder="安全问题（用于找回密码）" 
                  size="large"
                  class="custom-input"
                  @keyup.enter="focusSecurityAnswer"
                />
              </div>
            </el-form-item>
            <el-form-item>
              <div class="input-wrapper">
                <el-icon class="input-icon">
                  <Key />
                </el-icon>
                <el-input 
                  v-model="form.security_answer" 
                  type="password" 
                  placeholder="安全答案" 
                  size="large"
                  show-password
                  class="custom-input"
                  @keyup.enter="handleRegister"
                />
              </div>
            </el-form-item>
            <el-form-item>
              <el-button 
                type="primary" 
                size="large" 
                class="submit-btn" 
                :loading="loading" 
                @click="handleRegister"
              >
                <span>{{ isSetup ? '设置管理员' : '创建账号' }}</span>
                <el-icon><ArrowRight /></el-icon>
              </el-button>
            </el-form-item>
          </el-form>
          <div
            v-if="!isSetup"
            class="form-footer"
          >
            <div class="login-prompt">
              <span>已有账号？</span>
              <router-link
                to="/login"
                class="login-link"
              >
                立即登录
              </router-link>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, nextTick, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from '@/utils/message'
import { EditPen, User, Lock, QuestionFilled, Key, ArrowRight, Star, CircleCheck } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'
import PasswordStrengthBar from '@/components/PasswordStrengthBar.vue'
import { useConfigStore } from '@/stores/config'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const config = useConfigStore() // F-6：注册页欢迎语读取全局 siteTitle，随高级设置即时更新
const loading = ref(false)

onMounted(() => {
  // 注册页为登录前页面，挂载时拉取公共配置以确保应用标题最新（无认证依赖，安全）
  config.fetchPublicConfig()
})

const isSetup = computed(() => route.query.setup === 'true')

const form = reactive({
  username: '',
  password: '',
  password_confirm: '',
  security_question: '',
  security_answer: ''
})

async function focusPassword() {
  await nextTick()
  const inputs = document.querySelectorAll('.custom-input')
  if (inputs[1]) {
    const input = inputs[1].querySelector('input')
    input?.focus()
  }
}

async function focusConfirmPassword() {
  await nextTick()
  const inputs = document.querySelectorAll('.custom-input')
  if (inputs[2]) {
    const input = inputs[2].querySelector('input')
    input?.focus()
  }
}

async function focusSecurityQuestion() {
  await nextTick()
  const inputs = document.querySelectorAll('.custom-input')
  if (inputs[3]) {
    const input = inputs[3].querySelector('input')
    input?.focus()
  }
}

async function focusSecurityAnswer() {
  await nextTick()
  const inputs = document.querySelectorAll('.custom-input')
  if (inputs[4]) {
    const input = inputs[4].querySelector('input')
    input?.focus()
  }
}

async function handleRegister() {
  if (!form.username || !form.password || !form.password_confirm) {
    message.warning('请填写必要信息')
    return
  }
  // v21.96：取消强密码强制执行——6 位是唯一硬下限，字符复杂度不再拦截。
  // 弱密码照常放行，仅在提交成功后提示一次，由用户自行决定是否更换。
  if (form.password.length < 6) {
    message.warning('密码至少 6 位')
    return
  }
  if (form.password !== form.password_confirm) {
    message.warning('两次输入的密码不一致')
    return
  }
  loading.value = true
  try {
    const data = await auth.register({
      username: form.username,
      password: form.password,
      security_question: form.security_question,
      security_answer: form.security_answer
    })
    message.success(isSetup.value ? '管理员设置成功' : '注册成功')
    // 弱密码提示（不阻断流程）：强度条已在输入过程中实时提示，此处为提交后收尾提醒
    if (data?.password_strength === 'weak') {
      message.warning('密码强度偏弱', '建议后续在个人中心修改为更强的密码')
    }
    router.push('/')
  } catch (e) {
    message.error(e.response?.data?.error || '注册失败')
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.register-container {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
  overflow: hidden;
  background: var(--bg-secondary);
}

.register-bg {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  overflow: hidden;
}

.register-wrapper {
  position: relative;
  z-index: 1;
  padding: 20px;
  width: 100%;
  max-width: 1100px;
}

.register-card {
  background: var(--bg-primary);
  border-radius: var(--radius-xl);
  border: 1px solid var(--border-color);
  display: flex;
  overflow: hidden;
  min-height: 650px;
}

.register-left {
  flex: 1.2;
  background: var(--primary-color);
  padding: 60px 50px;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  position: relative;
  overflow: hidden;
}

.brand-section {
  position: relative;
  z-index: 1;
}

.brand-icon {
  width: 80px;
  height: 80px;
  background: rgba(255, 255, 255, 0.16);
  border-radius: var(--radius-lg);
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  margin-bottom: 24px;
}

.brand-title {
  font-size: 36px;
  font-weight: 600;
  color: white;
  margin: 0 0 12px;
  letter-spacing: -0.01em;
}

.brand-desc {
  font-size: 16px;
  color: rgba(255, 255, 255, 0.85);
  margin: 0;
  line-height: 1.6;
}

.welcome-content {
  position: relative;
  z-index: 1;
  text-align: center;
}

.welcome-icon {
  color: white;
  margin-bottom: 20px;
  opacity: 0.9;
}

.welcome-content h3 {
  font-size: 24px;
  font-weight: 600;
  color: white;
  margin: 0 0 8px;
}

.welcome-content p {
  font-size: 14px;
  color: rgba(255, 255, 255, 0.8);
  margin: 0 0 32px;
}

.benefits {
  display: flex;
  flex-direction: column;
  gap: 12px;
  align-items: flex-start;
}

.benefit-item {
  display: flex;
  align-items: center;
  gap: 10px;
  color: white;
  font-size: 14px;
}

.register-right {
  flex: 1;
  padding: 60px 50px;
  display: flex;
  flex-direction: column;
  justify-content: center;
}

.form-header {
  margin-bottom: 32px;
}

.form-header h2 {
  font-size: 30px;
  font-weight: 600;
  /* v21.100：暗色适配（原 #1f2937 暗色下与背景同色） */
  color: var(--text-primary);
  margin: 0 0 8px;
  letter-spacing: -0.01em;
}

.form-header p {
  font-size: 15px;
  /* v21.100：暗色适配（原 #6b7280 暗色下对比度不足） */
  color: var(--text-secondary);
  margin: 0;
}

.register-form {
  margin-bottom: 24px;
}

.input-wrapper {
  position: relative;
  width: 100%;
}

.custom-input {
  width: 100%;
}

.input-icon {
  position: absolute;
  left: 16px;
  top: 50%;
  transform: translateY(-50%);
  /* v21.100：暗色适配（原 #9ca3af 暗色下可见性一般） */
  color: var(--text-muted);
  font-size: 20px;
  z-index: 1;
}

.custom-input :deep(.el-input__wrapper) {
  padding-left: 48px !important;
  border-radius: var(--radius-md);
  /* v21.100：暗色适配（原 #e5e7eb 暗色下边框偏亮） */
  box-shadow: 0 0 0 1px var(--border-color);
  transition: box-shadow 0.15s;
}

.custom-input :deep(.el-input__wrapper:hover) {
  box-shadow: 0 0 0 1px var(--primary-color);
}

.custom-input :deep(.el-input__wrapper.is-focus) {
  box-shadow: 0 0 0 2px var(--primary-color);
}

.submit-btn {
  width: 100%;
  height: 44px;
  font-size: 16px;
  font-weight: 500;
  border-radius: var(--radius-md);
  background: var(--primary-color);
  border: none;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  transition: background-color 0.15s;
}

.submit-btn:hover {
  background: var(--primary-hover);
}

.form-footer {
  display: flex;
  flex-direction: column;
  gap: 16px;
  align-items: center;
}

.login-prompt {
  display: flex;
  gap: 6px;
  font-size: 14px;
  /* v21.100：暗色适配（原 #6b7280 暗色下对比度不足） */
  color: var(--text-secondary);
}

.login-link {
  color: #2563eb;
  text-decoration: none;
  font-weight: 600;
  transition: color 0.2s;
}

/* v21.168 审计④：旧配色遗留青绿 #0e7e75（非主题色），改用主题 token */
.login-link:hover {
  color: var(--primary-hover);
  text-decoration: underline;
}

@media (max-width: 968px) {
  .register-left {
    display: none;
  }

  .register-card {
    max-width: 480px;
    margin: 0 auto;
    min-height: auto;
  }

  .register-right {
    padding: 48px 32px;
  }
}
</style>
