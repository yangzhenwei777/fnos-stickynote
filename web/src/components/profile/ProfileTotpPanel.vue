<template>
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
</template>

<script setup>
import { ref, nextTick, onMounted, h } from 'vue'
import { message } from '@/utils/message'
import { ElMessageBox, ElInput } from 'element-plus'
import { Key } from '@element-plus/icons-vue'
import api, { updateAccessToken, updateRefreshToken } from '@/api/request'
import { useAuthStore } from '@/stores/auth'
import { useTotpStatus } from '@/composables/useTotpStatus'
import QRCode from 'qrcode'

const authStore = useAuthStore()

// ---- v20.2 TOTP 两步验证 ----
// v21.151：真实启用态从服务端加载（登录缓存值仅作拉取失败回退）——修「刷新后恒显示未启用」
const { totpEnabled, loadTotpStatus } = useTotpStatus(api, !!(authStore.user?.totp_enabled))
const totpSecret = ref('')
const totpLoading = ref(false)
const totpConfirmCode = ref('')
const totpRecoveryCodes = ref([])
const qrCanvas = ref(null)

onMounted(() => {
  loadTotpStatus()
})

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
</script>
