<template>
  <div class="profile-container">
    <div class="profile-layout">
      <div class="profile-sidebar">
        <div class="sidebar-header">
          <div class="avatar">
            <el-icon :size="32">
              <UserFilled />
            </el-icon>
          </div>
          <h2>个人设置</h2>
        </div>
        <div class="menu-list">
          <div 
            class="menu-item" 
            :class="{ active: activeTab === 'password' }"
            @click="activeTab = 'password'"
          >
            <el-icon><Key /></el-icon>
            <span>密码修改</span>
          </div>
          <div 
            class="menu-item" 
            :class="{ active: activeTab === 'security' }"
            @click="activeTab = 'security'"
          >
            <el-icon><QuestionFilled /></el-icon>
            <span>安全问题</span>
          </div>
          <div 
            class="menu-item" 
            :class="{ active: activeTab === 'quota' }"
            @click="activeTab = 'quota'"
          >
            <el-icon><TrendCharts /></el-icon>
            <span>用量</span>
          </div>
          <div 
            class="menu-item" 
            :class="{ active: activeTab === 'totp' }"
            @click="activeTab = 'totp'"
          >
            <el-icon><Key /></el-icon>
            <span>两步验证</span>
          </div>
          <div
            v-if="IS_DESKTOP"
            class="menu-item"
            :class="{ active: activeTab === 'account' }"
            @click="activeTab = 'account'"
          >
            <el-icon><Connection /></el-icon>
            <span>账号与设备</span>
          </div>
          <div
            v-if="IS_DESKTOP"
            class="menu-item"
            :class="{ active: activeTab === 'sync' }"
            @click="activeTab = 'sync'"
          >
            <el-icon><Refresh /></el-icon>
            <span>同步与冲突</span>
          </div>
          <div
            v-if="IS_DESKTOP"
            class="menu-item"
            :class="{ active: activeTab === 'portable' }"
            @click="activeTab = 'portable'"
          >
            <el-icon><FolderOpened /></el-icon>
            <span>便携备份</span>
          </div>
        </div>
      </div>

      <div class="profile-content">
        <div
          v-if="activeTab === 'password'"
          class="tab-panel"
        >
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
        </div>

        <div
          v-else-if="activeTab === 'security'"
          class="tab-panel"
        >
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
        </div>

        <div
          v-else-if="activeTab === 'quota'"
          class="tab-panel"
        >
          <div class="panel-header">
            <el-icon
              :size="40"
              class="panel-icon"
            >
              <TrendCharts />
            </el-icon>
            <div>
              <h3>用量概览</h3>
              <p>您的笔记与存储使用情况</p>
            </div>
          </div>
          <div class="quota-section">
            <div class="quota-item">
              <div class="quota-label">
                <span>笔记数</span>
                <span class="quota-val">{{ quota.notes_count }} / {{ quota.notes_limit > 0 ? quota.notes_limit : '不限' }}</span>
              </div>
              <el-progress
                :percentage="notesPercent"
                :color="notesPercent > 80 ? '#e6a23c' : '#2563eb'"
                :stroke-width="12"
              />
            </div>
            <div class="quota-item">
              <div class="quota-label">
                <span>存储空间</span>
                <span class="quota-val">{{ formatBytes(quota.storage_bytes) }} / {{ quota.storage_limit_mb > 0 ? quota.storage_limit_mb + ' MB' : '不限' }}</span>
              </div>
              <el-progress
                :percentage="storagePercent"
                :color="storagePercent > 80 ? '#e6a23c' : '#2563eb'"
                :stroke-width="12"
              />
            </div>
            <p class="quota-note">
              提示：回收站中的笔记不计入配额。
            </p>
          </div>
        </div>
        <div
          v-else-if="activeTab === 'totp'"
          class="tab-panel"
        >
          <div class="panel-header">
            <el-icon
              :size="40"
              class="panel-icon"
            >
              <Key />
            </el-icon>
            <div>
              <h3>两步验证</h3>
              <p v-if="!totpEnabled">
                绑定验证器App后，登录需输入动态验证码
              </p>
              <p v-else>
                两步验证已开启，登录需输入动态验证码
              </p>
            </div>
          </div>
          <div
            v-if="!totpEnabled && !totpSecret"
            class="form-wrapper"
          >
            <p style="color:var(--text-secondary);margin-bottom:16px;">
              开启后，除密码外还需手机验证器生成的6位动态码，大幅提升账号安全。
            </p>
            <el-button
              type="primary"
              :loading="totpLoading"
              @click="enableTotp"
            >
              开启两步验证
            </el-button>
          </div>
          <div
            v-else-if="!totpEnabled && totpSecret"
            class="form-wrapper"
          >
            <p style="font-weight:600;">
              步骤1：用验证器 App 扫描二维码
            </p>
            <div style="margin:16px 0;padding:16px;background:#fff;display:inline-block;border-radius:var(--radius-lg);">
              <canvas
                ref="qrCanvas"
                style="width:200px;height:200px;"
              />
            </div>
            <p style="font-size:12px;color:var(--text-muted);margin-bottom:12px;">
              或手动输入密钥：<code>{{ totpSecret }}</code>
            </p>
            <p style="font-weight:600;">
              步骤2：输入验证器显示的6位验证码确认
            </p>
            <div style="display:flex;gap:8px;align-items:center;margin-top:8px;">
              <el-input
                v-model="totpConfirmCode"
                placeholder="6位验证码"
                maxlength="6"
                style="width:160px;"
                @keyup.enter="confirmTotp"
              />
              <el-button
                type="primary"
                :loading="totpLoading"
                @click="confirmTotp"
              >
                确认开启
              </el-button>
            </div>
          </div>
          <div
            v-else
            class="form-wrapper"
          >
            <div style="display:flex;align-items:center;gap:8px;margin-bottom:16px;">
              <span style="color:#67c23a;font-size:24px;">●</span>
              <span style="font-weight:600;">两步验证已开启</span>
            </div>
            <div
              v-if="totpRecoveryCodes.length"
              style="background:var(--bg-secondary);border-radius:var(--radius-md);padding:12px;margin-bottom:16px;"
            >
              <p style="font-weight:600;margin-bottom:8px;">
                一次性恢复码（请立即保存，不再显示）：
              </p>
              <div style="display:grid;grid-template-columns:1fr 1fr;gap:4px;">
                <code
                  v-for="c in totpRecoveryCodes"
                  :key="c"
                  style="font-family:monospace;font-size:13px;"
                >{{ c }}</code>
              </div>
            </div>
            <el-button
              type="danger"
              plain
              :loading="totpLoading"
              @click="disableTotp"
            >
              关闭两步验证
            </el-button>
          </div>
        </div>

        <div
          v-else-if="activeTab === 'account'"
          class="tab-panel"
        >
          <DesktopAccountCard />
        </div>

        <div
          v-else-if="activeTab === 'sync'"
          class="tab-panel"
        >
          <ConflictCenterCard />
        </div>

        <div
          v-else-if="activeTab === 'portable'"
          class="tab-panel"
        >
          <PortableBackupCard />
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, nextTick, onMounted, h } from 'vue'
import { changePassword, updateSecurityQuestion } from '../api/auth'
import { getQuota } from '../api/note'
import { message } from '@/utils/message'
import { ElMessageBox, ElInput } from 'element-plus'
import { IS_DESKTOP } from '@/api/local/transport'
import PortableBackupCard from '@/components/PortableBackupCard.vue'
import ConflictCenterCard from '@/components/ConflictCenterCard.vue'
import DesktopAccountCard from '@/components/DesktopAccountCard.vue'
import { Lock, Key, QuestionFilled, UserFilled, TrendCharts, FolderOpened, Refresh, Connection } from '@element-plus/icons-vue'
import PasswordStrengthBar from '@/components/PasswordStrengthBar.vue'
import QRCode from 'qrcode'
import api, { updateAccessToken, updateRefreshToken } from '@/api/request'
import { useAuthStore } from '@/stores/auth'
import { useTotpStatus } from '@/composables/useTotpStatus'

const authStore = useAuthStore()

const activeTab = ref('password')

const passwordLoading = ref(false)
const passwordForm = ref({
  current_password: '',
  new_password: '',
  confirm_password: ''
})

const securityLoading = ref(false)
const securityForm = ref({
  // v21.90 H-02：修改安全问题需验证当前密码（敏感操作二次认证）
  current_password: '',
  question: '',
  answer: '',
  confirm_answer: ''
})

// 配额可视化（v19.1）
const quota = reactive({ notes_count: 0, notes_limit: 1000, storage_bytes: 0, storage_limit_mb: 1024 })
const notesPercent = computed(() => quota.notes_limit > 0 ? Math.min(100, Math.round(quota.notes_count / quota.notes_limit * 100)) : 0)
const storagePercent = computed(() => quota.storage_limit_mb > 0 ? Math.min(100, Math.round(quota.storage_bytes / 1024 / 1024 / quota.storage_limit_mb * 100)) : 0)

function formatBytes(n) {
  if (!n) return '0 B'
  if (n < 1024) return n + ' B'
  if (n < 1048576) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1073741824) return (n / 1048576).toFixed(1) + ' MB'
  return (n / 1073741824).toFixed(2) + ' GB'
}

async function loadQuota() {
  try {
    const { data } = await getQuota()
    Object.assign(quota, data)
  } catch { /* 静默失败，不影响页面其他功能 */ }
}

onMounted(() => {
  loadQuota()
  loadTotpStatus()
})

// ---- v20.2 TOTP 两步验证 ----
// v21.151：真实启用态从服务端加载（登录缓存值仅作拉取失败回退）——修「刷新后恒显示未启用」
const { totpEnabled, loadTotpStatus } = useTotpStatus(api, !!(authStore.user?.totp_enabled))
const totpSecret = ref('')
const totpLoading = ref(false)
const totpConfirmCode = ref('')
const totpRecoveryCodes = ref([])
const qrCanvas = ref(null)

async function enableTotp() {
  totpLoading.value = true
  try {
    const { data } = await api.post('/auth/enable-totp')
    totpSecret.value = data.secret
    await nextTick()
    if (qrCanvas.value) {
      await QRCode.toCanvas(qrCanvas.value, data.otpauth_url, { width: 200 })
    }
  } catch (e) {
    message.error(e.response?.data?.error || '操作失败')
    // v21.151：服务端报「已启用」（本地显示态过期）→ 立即回读真实态纠正显示
    if (String(e?.response?.data?.error || '').includes('已启用')) loadTotpStatus()
  } finally { totpLoading.value = false }
}

async function confirmTotp() {
  if (!totpConfirmCode.value) return message.error('请输入验证码')
  totpLoading.value = true
  try {
    const { data } = await api.post('/auth/confirm-totp', { code: totpConfirmCode.value })
    totpEnabled.value = true
    totpRecoveryCodes.value = data.recovery_codes || []
    totpSecret.value = ''
    message.success(data.message)
  } catch (e) {
    message.error(e.response?.data?.error || '验证码错误')
  } finally { totpLoading.value = false }
}

async function disableTotp() {
  // v21.93 P0（R-02/H-01 审计核对）：关闭 TOTP 必须「当前密码 + 第二因子」。
  // v21.90 只收集了当前密码，后端又允许 code 留空 → 已启用 MFA 的账号
  // 实际可仅凭密码关闭 2FA，第二因子形同虚设。此处同步收集验证码/恢复码。
  const password = ref('')
  const code = ref('')
  try {
    await ElMessageBox({
      title: '敏感操作确认',
      message: () => h('div', null, [
        h('p', { style: 'margin:0 0 10px;color:var(--el-text-color-regular);line-height:1.6' },
          '关闭两步验证需验证「当前密码 + 验证码或恢复码」'),
        h(ElInput, {
          modelValue: password.value,
          type: 'password',
          placeholder: '当前密码',
          showPassword: true,
          clearable: true,
          'onUpdate:modelValue': v => (password.value = v)
        }),
        h(ElInput, {
          modelValue: code.value,
          placeholder: '验证码或恢复码',
          clearable: true,
          style: 'margin-top:10px',
          'onUpdate:modelValue': v => (code.value = v)
        })
      ]),
      confirmButtonText: '确认关闭',
      // v21.211：关闭两步验证属高风险操作，确认键用红色
      confirmButtonClass: 'el-button--danger',
      cancelButtonText: '取消',
      beforeClose: (action, instance, done) => {
        if (action !== 'confirm') return done()
        if (!password.value.trim()) return message.error('请输入当前密码')
        if (!code.value.trim()) return message.error('请输入验证码或恢复码')
        done()
      }
    })
  } catch { return }
  if (!password.value.trim() || !code.value.trim()) return
  totpLoading.value = true
  try {
    // v21.105 P0-2：关闭 TOTP 后端已递增 token_version 并重签 token 对，同步本地 storage
    const { data } = await api.post('/auth/disable-totp', {
      current_password: password.value.trim(),
      code: code.value.trim()
    })
    if (data?.access_token) updateAccessToken(data.access_token)
    if (data?.refresh_token) updateRefreshToken(data.refresh_token)
    totpEnabled.value = false
    totpRecoveryCodes.value = []
    message.success('已关闭两步验证')
  } catch (e) {
    message.error(e.response?.data?.error || '操作失败')
  } finally { totpLoading.value = false }
}

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

<style scoped>
.profile-container {
  /* v21.86：原 calc(100vh - 64px) 假设顶部菜单栏，现改为左侧菜单布局满高；
     padding 原由 MainLayout main-content 提供，全宽改造后自带 */
  min-height: 100vh;
  padding: 24px;
  box-sizing: border-box;
  background: var(--bg-secondary);
}

.profile-layout {
  display: flex;
  width: 100%;
  max-width: 1200px;
  margin: 0 auto;
  min-height: calc(100vh - 48px);
  background: var(--bg-primary);
  border-radius: var(--radius-xl);
  border: 1px solid var(--border-color);
}

.profile-sidebar {
  width: 200px;
  flex-shrink: 0;
  /* v21.100：暗色适配——原硬编码 #fafbfc/#f0f0f0 不随主题，改 CSS 变量 */
  background: var(--bg-secondary);
  border-right: 1px solid var(--border-light);
  padding: 24px 0;
}

.sidebar-header {
  text-align: center;
  padding: 0 20px 24px;
  border-bottom: 1px solid var(--border-light);
}

.avatar {
  width: 56px;
  height: 56px;
  border-radius: 50%;
  background: var(--primary-color);
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  margin: 0 auto 12px;
}

.sidebar-header h2 {
  font-size: 16px;
  font-weight: 600;
  /* v21.100：暗色适配（原 #1f2937 暗色下与背景同色） */
  color: var(--text-primary);
  margin: 0;
}

.menu-list {
  padding: 16px 12px;
}

.menu-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 16px;
  border-radius: var(--radius-md);
  font-size: 14px;
  font-weight: 500;
  /* v21.100：暗色适配（原 #6b7280 暗色侧栏上对比度不足） */
  color: var(--text-secondary);
  cursor: pointer;
  transition: all 0.2s;
  user-select: none;
}

.menu-item:hover {
  background: var(--bg-tertiary);
  color: var(--text-primary);
}

.menu-item.active {
  /* v21.207：与主界面导航选中态一致——浅蓝底 + 深蓝字，无渐变/阴影 */
  background: var(--primary-tint);
  color: var(--primary-ink);
}

.menu-item .el-icon {
  font-size: 18px;
}

.profile-content {
  flex: 1;
  padding: 32px 36px;
  display: flex;
  flex-direction: column;
  justify-content: center;
}

.tab-panel {
  /* v21.177 方案A（用户拍板）：全宽自适应——原 440px 在宽窗下两侧空白过大 */
  max-width: none;
  width: 100%;
  margin: 0 auto;
}

.panel-header {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 32px;
}

.panel-icon {
  color: #2563eb;
  flex-shrink: 0;
}

.panel-header h3 {
  font-size: 20px;
  font-weight: 600;
  /* v21.100：暗色适配（原 #1f2937 暗色下与背景同色） */
  color: var(--text-primary);
  margin: 0 0 4px;
}

.panel-header p {
  font-size: 13px;
  color: var(--text-muted);
  margin: 0;
}

.form-wrapper {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.form-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.group-label {
  font-size: 13px;
  font-weight: 500;
  /* v21.168 审计④：旧灰 #9ca3af 仅 2.54:1，改用 --text-muted token（自动随暗色主题） */
  color: var(--text-muted);
}

.input-box {
  position: relative;
  display: flex;
  align-items: center;
}

.input-box-icon {
  position: absolute;
  left: 14px;
  font-size: 18px;
  color: var(--text-muted);
  z-index: 1;
}

.input-box :deep(.el-input__wrapper) {
  padding-left: 42px !important;
  border-radius: var(--radius-md);
  box-shadow: 0 0 0 1px var(--border-color);
}

.input-box :deep(.el-input__wrapper.is-focus) {
  box-shadow: 0 0 0 2px var(--primary-color);
}

.submit-btn {
  height: 44px;
  font-size: 15px;
  font-weight: 500;
  border-radius: var(--radius-md);
  background: var(--primary-color);
  border: none;
  margin-top: 8px;
}

.quota-section {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.quota-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.quota-label {
  display: flex;
  justify-content: space-between;
  font-size: 14px;
  font-weight: 500;
  /* v21.168 审计④：旧灰 #9ca3af 仅 2.54:1，改用 --text-muted token（自动随暗色主题） */
  color: var(--text-muted);
}

.quota-val {
  color: #6b7280;
  font-weight: 400;
}

.quota-note {
  font-size: 12px;
  color: var(--text-muted);
  margin: 0;
}

@media (max-width: 768px) {
  .profile-container {
    padding: 0;
  }

  .profile-layout {
    flex-direction: column;
    border-radius: 0;
    min-height: calc(100vh - 56px);
    max-width: 100%;
  }

  .profile-sidebar {
    width: 100%;
    flex-shrink: 0;
    border-right: none;
    /* v21.100：暗色适配（与 PC 端一致） */
    border-bottom: 1px solid var(--border-light);
    padding: 12px 0 10px;
  }

  .sidebar-header {
    padding: 0 16px 10px;
  }

  .avatar {
    width: 40px;
    height: 40px;
    margin-bottom: 8px;
  }

  .sidebar-header h2 {
    font-size: 15px;
  }

  .menu-list {
    display: flex;
    padding: 0 10px;
    gap: 6px;
  }

  .menu-item {
    flex: 1;
    justify-content: center;
    padding: 8px 6px;
    font-size: 13px;
    gap: 4px;
  }

  .profile-content {
    padding: 16px 16px 24px;
    display: block;
  }

  .tab-panel {
    max-width: none;
  }

  .panel-header {
    margin-bottom: 16px;
  }

  .panel-icon {
    display: none;
  }

  .panel-header h3 {
    font-size: 18px;
  }

  .form-wrapper {
    gap: 14px;
  }
}
</style>
