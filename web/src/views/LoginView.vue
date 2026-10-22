<template>
  <div class="login-container">
    <div class="login-bg" />
    <div class="login-wrapper">
      <el-alert
        v-if="insecureHttp"
        type="warning"
        closable
        show-icon
        class="http-warning"
        title="警告：当前为 HTTP 明文连接"
        description="登录密码、笔记内容等全部数据将以明文传输，任何中间节点均可窃听。强烈建议配置 HTTPS 反向代理；仅在可信内网/VPN 环境下可关闭本提示。"
        @close="dismissHttpWarning"
      />
      <div class="login-card">
        <div class="login-left">
          <div class="brand-section">
            <div class="brand-icon">
              <el-icon :size="56">
                <Document />
              </el-icon>
            </div>
            <h1 class="brand-title">
              {{ config.siteTitle }}
            </h1>
            <p class="brand-desc">
              记录每一刻的灵感与想法
            </p>
          </div>
          <div class="features">
            <div class="feature-item">
              <div class="feature-icon">
                <el-icon><EditPen /></el-icon>
              </div>
              <div class="feature-text">
                <h4>富文本编辑</h4>
                <p>支持多种格式和样式</p>
              </div>
            </div>
            <div class="feature-item">
              <div class="feature-icon">
                <el-icon><Tickets /></el-icon>
              </div>
              <div class="feature-text">
                <h4>标签管理</h4>
                <p>轻松组织和查找笔记</p>
              </div>
            </div>
            <div class="feature-item">
              <div class="feature-icon">
                <el-icon><Monitor /></el-icon>
              </div>
              <div class="feature-text">
                <h4>响应式设计</h4>
                <p>完美适配各种设备</p>
              </div>
            </div>
          </div>
        </div>
        <div class="login-right">
          <div class="form-header">
            <h2>欢迎回来</h2>
            <p>登录您的账号继续使用</p>
          </div>
          <div class="login-form">
            <div
              v-if="IS_DESKTOP"
              class="form-item"
              style="margin-bottom: 20px"
            >
              <div class="input-wrapper">
                <el-icon class="input-icon">
                  <Connection />
                </el-icon>
                <el-input
                  v-model="serverURL"
                  placeholder="服务器地址：域名或 http://IP:13389"
                  size="large"
                  class="custom-input"
                />
              </div>
            </div>
            <div
              class="form-item"
              style="margin-bottom: 20px"
            >
              <div class="input-wrapper">
                <el-icon class="input-icon">
                  <User />
                </el-icon>
                <el-input 
                  v-model="form.username" 
                  placeholder="请输入用户名" 
                  size="large"
                  class="custom-input"
                />
              </div>
            </div>
            <div
              class="form-item"
              style="margin-bottom: 24px"
            >
              <div class="input-wrapper">
                <el-icon class="input-icon">
                  <Lock />
                </el-icon>
                <el-input 
                  v-model="form.password" 
                  type="password" 
                  placeholder="请输入密码" 
                  size="large"
                  show-password
                  class="custom-input"
                  @keyup.enter="handleLogin"
                />
              </div>
            </div>
            <div class="form-options">
              <label class="remember-me">
                <el-checkbox v-model="rememberMe">记住我（30天内免登录）</el-checkbox>
              </label>
            </div>
            <div class="form-item">
              <el-button
                type="primary"
                size="large"
                class="submit-btn"
                :loading="loading"
                @click="handleLogin"
              >
                <span>{{ IS_DESKTOP ? '登录并同步' : '登录' }}</span>
                <el-icon><ArrowRight /></el-icon>
              </el-button>
              <el-button
                v-if="IS_DESKTOP"
                size="large"
                class="offline-btn"
                :disabled="loading"
                @click="handleOfflineContinue"
              >
                离线单机继续（不登录）
              </el-button>
              <p
                v-if="IS_DESKTOP"
                class="desktop-hint"
              >
                离线单机模式下数据只存本机；登录后本机的改动会自动排队上传，之后可离线使用。
              </p>
            </div>
          </div>
          <div class="form-footer">
            <router-link
              to="/forgot-password"
              class="forgot-link"
            >
              忘记密码？
            </router-link>
            <div class="register-prompt">
              <span>还没有账号？</span>
              <template v-if="allowRegister">
                <router-link
                  to="/register"
                  class="register-link"
                >
                  立即注册
                </router-link>
              </template>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessageBox } from 'element-plus'
import { message } from '@/utils/message'
import { describeLoginError } from '@/utils/loginError'
import { Document, User, Lock, ArrowRight, EditPen, Tickets, Monitor, Connection } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { getSetupStatus, verifyTotp } from '@/api/auth'
import { IS_DESKTOP } from '@/api/local/transport'
import { remoteLogin, remoteLoginTOTP, validateLoginInput, formatAuthError } from '@/api/local/desktopApi'

const router = useRouter()
const auth = useAuthStore()
const config = useConfigStore()
const loading = ref(false)
const allowRegister = ref(true)
const rememberMe = ref(false)
// 桌面壳的服务器地址（非敏感，记住上次填的即可；凭据本身由 Go 侧 DPAPI 封存）
const serverURL = ref(localStorage.getItem('sn_server_url') || '')

// 非 localhost 的 HTTP 访问时强提示明文传输风险（v18.8：info→warning 醒目横幅，可关闭，
// 关闭状态记 sessionStorage——新会话仍会再次提示，避免"关一次永久看不见"）
const insecureHttp = ref(
  window.location.protocol === 'http:' &&
  !['localhost', '127.0.0.1'].includes(window.location.hostname) &&
  sessionStorage.getItem('http_warning_dismissed') !== '1'
)

function dismissHttpWarning() {
  sessionStorage.setItem('http_warning_dismissed', '1')
  insecureHttp.value = false
}

const form = reactive({
  username: '',
  password: ''
})

onMounted(async () => {
  // 如果没有用户，跳转到管理员注册
  try {
    const { data } = await getSetupStatus()
    if (data.needs_setup) {
      router.replace('/register?setup=true')
      return
    }
  } catch {}
  await config.fetchPublicConfig()
  allowRegister.value = config.allowRegister

  const saved = localStorage.getItem('login_remember')
  if (saved) {
    try {
      const data = JSON.parse(saved)
      // 仅回填用户名；旧版本数据中的密码字段（MD5 等价物）直接丢弃并重写
      form.username = data.username || ''
      if (form.username) {
        rememberMe.value = true
        if (data.password) {
          localStorage.setItem('login_remember', JSON.stringify({ username: form.username }))
        }
      }
    } catch {
      localStorage.removeItem('login_remember')
    }
  }
})

// 桌面壳登录（批次 8-Alt 刀 6）：/auth/login 在 LocalTransport 里映射到 P0-A 桩
// LocalLogin（丢弃账密、恒成功），所以真登录必须显式走 RemoteLogin 绑定；桩通道保留为
// 用户主动选择的「离线单机继续」，不再对任意密码偷偷放行。
// promptDesktopTotp v21.151：桌面二因子第二步的验证码收集（与 Web 分支同款交互；
// 一并接受恢复码——服务端 verify-totp 自带恢复码通道）。
async function promptDesktopTotp() {
  const { value: code } = await ElMessageBox.prompt(
    '该账号已开启两步验证，请输入验证器 App 显示的 6 位动态码（或一枚恢复码）', '两步验证', {
      confirmButtonText: '验证并登录',
      cancelButtonText: '取消',
      inputType: 'text',
      inputPlaceholder: '6位动态码或恢复码',
      inputValidator: (v) => v && String(v).trim().length >= 6 ? true : '请输入完整的动态码（或恢复码）',
      distinguishCancelAndClose: true
    })
  return String(code || '').trim()
}

// afterDesktopLogin 远端会话建立后的本地收口。
async function afterDesktopLogin(rl) {
  localStorage.setItem('sn_server_url', serverURL.value.trim())
  // 远端会话建立后，才建立本地前端会话（路由守卫与 UI 用它判登录态；HTTP token 在桌面下不参与鉴权）
  const res = await auth.login(form.username, form.password, rememberMe.value)
  finishLogin(res)
  if (rl && rl.first_sync_done === false && rl.first_sync_note) {
    message.info(String(rl.first_sync_note))
  }
  // v21.147：登录后 15 秒再刷一次，拾取后台同步拉到的新数据
  // v21.194 退役：sn_sync_refresh 的 15s 二次整页重载已删（竞态温床，见 main.js 注释）；
  // 数据拾取由 installSyncRefreshPolling 轮次通知承担（193 接线）。
  // v21.151 补：必须走**整页重载**（与账号卡片同款）——main.js 只在启动时消费 sn_sync_refresh
  // 并安排 +15s 自动重载；此前这里只 router.push（SPA 跳转不触发启动钩子）→ 若首轮同步
  // 未在 8s 有界等待内完成，列表会一直停在空态（2026-09-24 真机走查实证：本地库 24 条已同步、
  // UI 45s/90s 均空；竞态型，F5 可手动恢复）。
  //
  // ⭐⭐ v21.216（211e 实机·首次登录白屏真因）：**退役登录路径的整页重载**。
  //
  // 定罪：v21.194 时代就实证过「重载窗口与 bootstrap 并发时 DOM 空壳」（main.js 注释原文）。
  // v21.140~165 靠「登录同步等换代跑完」绕开了这个竞态；v21.212 把登录改回
  // 「立即返回 + 异步换代」后，500ms 整页重载**恰好撞进 6~60s 的换代窗口** ——
  // WebView2 重载期间 Go 侧正在关旧库/切句柄/重挂 worker，前端绑定调用与
  // runtime 注入撞上中间态 ⇒ 整个 app 初始化失败 ⇒ 纯白（bootGuard 的
  // 「挂载失败显示错误摘要」防护不了 mount 成功后路由视图的渲染中断）。
  //
  // 为什么现在可以安全退役：v21.165 起的 installSyncRefreshPolling 已盯住
  // gen_epoch（v21.212 起跨进程持久化），NotesView 挂载即注册 → 两种时序全覆盖：
  //   · 换代已完 ⇒ 挂载时 loadNotes 直接读到全量数据；
  //   · 换代在途 ⇒ 完成瞬间 gen_epoch 1>0 ⇒ 回调 loadNotes + loadFolders。
  // 「SPA 跳转不触发启动钩子」的旧顾虑也不再成立——启动钩子的职责（SW 清理、
  // _spa 还原、主题）要么一次性、要么已由轮询接管。
  //
  // 整页重载唯一不可替代的场景是「库句柄全换后内存缓存作废」——这恰由
  // gen_epoch 轮询覆盖（换代即回调），不再需要拿"整页"换"干净"。
  router.push('/')
}

// handleDesktopLogin 桌面真登录：RemoteLogin 绑定（LocalLogin 桩仅保留给「离线单机继续」）。
// v21.151（生产 Bug2）：2FA 账号第一步返回 {totp_required, totp_token}——弹验证码框走
// RemoteLoginTOTP 第二步；旧流程把该响应当成功（空票）→ 下游 401 → 误报「会话已失效」。
async function handleDesktopLogin() {
  const bad = validateLoginInput(serverURL.value, form.username, form.password)
  if (bad) {
    message.warning(bad)
    return
  }
  loading.value = true
  try {
    // v21.140：Go 侧"登录即等首轮同步"（有界，上限 60s）。返回值带 first_sync_done/note，
    // 用于把"数据还没到齐"如实告知，而不是让用户以为同步丢了一部分。
    let rl = await remoteLogin(serverURL.value, form.username, form.password)
    if (rl && rl.totp_required) {
      const code = await promptDesktopTotp()
      rl = await remoteLoginTOTP(serverURL.value, form.username, rl.totp_token, code)
    }
    await afterDesktopLogin(rl)
  } catch (e) {
    if (e !== 'cancel' && e !== 'close') {
      message.error(formatAuthError(e))
    }
  } finally {
    loading.value = false
    form.password = ''
  }
}

async function handleOfflineContinue() {
  loading.value = true
  try {
    const res = await auth.login(form.username || 'local', form.password || 'offline', rememberMe.value)
    finishLogin(res)
    message.info('已进入离线单机模式，数据只存本机')
  } catch {
    message.error('无法进入离线模式')
  } finally {
    loading.value = false
  }
}

async function handleLogin() {
  if (IS_DESKTOP) {
    return handleDesktopLogin()
  }
  if (!form.username || !form.password) {
    message.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    const res = await auth.login(form.username, form.password, rememberMe.value)

    // v20.2 TOTP: 两步验证
    if (res?.totp_required) {
      await handleTotpVerification(res.totp_token)
      return
    }

    finishLogin(res)
  } catch (e) {
    // v2.1.4：错误分类展示（实现见 utils/loginError.js）。此前 `data?.error || '登录失败'`
    // 在「响应体不是 JSON」时只能显示裸的四个字——安卓中继链路上 CORS 403 空 body
    // 即是如此（实测报障），用户无从判断该找谁。按可诊断性分四类，每类给出下一步动作。
    message.error(describeLoginError(e))
  } finally {
    loading.value = false
  }
}

// ---- v20.2 TOTP 两步验证 ----
async function handleTotpVerification(totpToken) {
  loading.value = false
  try {
    const { value: code } = await ElMessageBox.prompt('请输入验证器 App 显示的 6 位验证码', '两步验证', {
      confirmButtonText: '验证',
      cancelButtonText: '取消',
      inputType: 'text',
      inputPlaceholder: '6位验证码',
      inputValidator: (v) => v && v.length >= 6 ? true : '请输入完整的6位验证码',
      distinguishCancelAndClose: true
    })
    loading.value = true
    // v21.66 P1-1：TOTP 登录的「记住我/信任设备」遵循用户勾选，不再写死 true
    // F-1 修复（v1.0.134 审计缺陷）：verifyTotp 走裸 API，axios 响应拦截器返回**完整 response**，
    // 必须解包 .data 才能取到 access_token（与 stores/auth.js:39 login 的取数约定保持一致）。
    // 修复前 res.access_token 恒为 undefined → setAuth 写入 undefined → isAuthenticated 恒 false
    // → 路由守卫弹回 /login，所有开启两步验证的账号陷入「登录死循环」。
    const { data } = await verifyTotp(totpToken, code, rememberMe.value)
    // v21.58 F-03：TOTP 验证通过后才正式建立会话（login 阶段 totp_required 未 setAuth）
    auth.setAuth({ access: data.access_token, refresh: data.refresh_token }, data.user, rememberMe.value)
    finishLogin(data)
  } catch (e) {
    if (e !== 'cancel' && e !== 'close') {
      message.error(e.response?.data?.error || '验证码错误')
    }
    // 取消/关闭验证对话框时清空残留会话状态，保持未登录
    auth.clearAuth()
  } finally {
    loading.value = false
  }
}

function finishLogin(res) {
  if (rememberMe.value) {
    localStorage.setItem('login_remember', JSON.stringify({ username: form.username }))
  } else {
    localStorage.removeItem('login_remember')
  }
  message.success('登录成功')
  if (res?.warning) {
    ElMessageBox.alert(res.warning, '服务端配置警告', { confirmButtonText: '知道了', type: 'warning' }).catch(() => {})
  }
  router.push('/')
}
</script>

<style scoped>
.offline-btn {
  width: 100%;
  margin-top: 12px;
  margin-left: 0;
}
.desktop-hint {
  font-size: 12px;
  line-height: 1.7;
  color: var(--text-muted);
  margin: 12px 0 0;
  text-align: left;
}
.login-container {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
  overflow: hidden;
  background: var(--bg-secondary);
}

.login-bg {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  overflow: hidden;
}

.login-wrapper {
  position: relative;
  z-index: 1;
  padding: 20px;
  width: 100%;
  max-width: 1100px;
}

.login-card {
  background: var(--bg-primary);
  border-radius: var(--radius-xl);
  border: 1px solid var(--border-color);
  display: flex;
  overflow: hidden;
  min-height: 600px;
}

.login-left {
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

.features {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.feature-item {
  display: flex;
  align-items: center;
  gap: 16px;
}

.feature-icon {
  width: 48px;
  height: 48px;
  background: rgba(255, 255, 255, 0.14);
  border-radius: var(--radius-md);
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  flex-shrink: 0;
}

.feature-text h4 {
  font-size: 16px;
  font-weight: 600;
  color: white;
  margin: 0 0 4px;
}

.feature-text p {
  font-size: 13px;
  color: rgba(255, 255, 255, 0.75);
  margin: 0;
}

.login-right {
  flex: 1;
  min-width: 0;
  padding: 60px 50px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  width: 100%;
}

.form-header {
  margin-bottom: 40px;
}

.form-header h2 {
  font-size: 30px;
  font-weight: 600;
  /* v21.207：改用主题文字色（亮/暗自动翻转），不再与副标题同为固定灰 */
  color: var(--text-primary);
  margin: 0 0 8px;
  letter-spacing: -0.01em;
}

.form-header p {
  font-size: 15px;
  color: var(--text-secondary);
  margin: 0;
}

.login-form {
  width: 100%;
  margin-bottom: 24px;
}

.form-item {
  width: 100%;
}

.input-wrapper {
  position: relative;
}

.input-icon {
  position: absolute;
  left: 16px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-muted);
  font-size: 20px;
  z-index: 1;
}

.custom-input :deep(.el-input__wrapper) {
  padding-left: 48px !important;
  border-radius: var(--radius-md);
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

.form-options {
  margin-bottom: 20px;
  display: flex;
  justify-content: flex-start;
}

.remember-me {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  user-select: none;
  font-size: 14px;
  color: var(--text-secondary);
}

.remember-me :deep(.el-checkbox__inner) {
  border-radius: var(--radius-sm);
}

.remember-me :deep(.el-checkbox__label) {
  font-size: 14px;
  color: var(--text-secondary);
}

.form-footer {
  display: flex;
  flex-direction: column;
  gap: 16px;
  align-items: center;
}

.forgot-link {
  color: var(--text-secondary);
  text-decoration: none;
  font-size: 14px;
  transition: color 0.2s;
}

.forgot-link:hover {
  color: var(--primary-color);
  text-decoration: underline;
}

.register-prompt {
  display: flex;
  gap: 6px;
  font-size: 14px;
  color: var(--text-secondary);
}

.register-link {
  color: var(--primary-color);
  text-decoration: none;
  font-weight: 500;
  transition: color 0.2s;
}

.register-link:hover {
  color: var(--primary-hover);
  text-decoration: underline;
}

@media (max-width: 968px) {
  .login-left {
    display: none;
  }

  .login-card {
    max-width: 480px;
    margin: 0 auto;
  }

  .login-right {
    padding: 48px 32px;
  }
}

@media (max-width: 480px) {
  .login-wrapper {
    padding: 16px;
  }

  .login-card {
    border-radius: var(--radius-xl);
    min-height: auto;
  }

  .login-left {
    display: none;
  }

  .login-right {
    padding: 40px 24px 32px;
  }

  .form-header {
    margin-bottom: 24px;
  }

  .form-header h2 {
    font-size: 24px;
  }

  .login-form {
    margin-bottom: 0;
  }

  .form-options {
    margin-bottom: 16px;
  }

  .form-footer {
    margin-top: 12px;
  }
}

.http-warning {
  width: 100%;
  max-width: 1100px;
  margin-bottom: 16px;
}
.http-warning :deep(.el-alert__title) {
  color: var(--el-color-danger);
}
</style>
