<script setup>
// DesktopAccountCard —— 批次 8-Alt 刀 6：桌面远端登录入口（账号与设备）。
// 2026-11-03 巨型 Vue 拆分 刀2：全部业务逻辑抽至 composables/useDesktopAccount.js
// （零行为变化，本组件只做呈现；逻辑注释与三条硬规矩见该文件头）。
import { useDesktopAccount } from '@/composables/useDesktopAccount'

const {
  busy, diagPath, st, paused, loading, loadError,
  form, formError, totp,
  loggedIn, accounting, needAttention, heldReason,
  autoBootTripped, autoBootCooldownSec, lastNetFailure, serverUrlRecovered,
  urlCheck, urlCheckBlocked,
  online, recoverable, reloginRequired, retryInSec, retryInMin,
  stateLine, stateLevel,
  refresh, onLogin, onVerifyTotp, cancelTotp,
  onForceResync, onRetryNow, onLogout, onBootstrap, onExportDiagnostics
} = useDesktopAccount()
</script>

<template>
  <div class="acct">
    <p class="acct-desc">
      桌面端可以先离线记账，登录后再把改动同步到服务器。<strong>退出登录会清除本机保存的登录凭据</strong>，
      已记录的改动保留。
    </p>

    <el-alert
      v-if="loadError"
      class="acct-alert"
      type="error"
      :closable="false"
      show-icon
      title="账号状态读取失败"
      :description="loadError"
    />

    <el-alert
      v-if="paused"
      class="acct-alert"
      type="info"
      :closable="false"
      show-icon
      title="正在初始化全量同步"
      description="换代期间同步请求会排队（通常 10~30 秒），完成后自动恢复，无需操作。"
    />

    <!-- v21.202 P0-2 附带：档案在但本机 DPAPI 打不开（换机/复制目录）时，
         后端已从明文列回填 server_url。这里告诉用户"地址没变、只是本机解不开封存"。 -->
    <el-alert
      v-if="serverUrlRecovered && !loggedIn"
      class="acct-alert"
      type="warning"
      :closable="false"
      show-icon
      title="本机登录凭据无法解封"
      description="档案里的服务器地址已自动填回（地址本身没变）。但本机无法解密保存的登录凭据（常见于换机或复制了程序目录），请重新输入账号密码登录一次。"
    />

    <!-- v21.202 P0-3：最近一次网络/代理失败。后端把 TLS 握手超时 / DNS / 拒连
         翻成可执行中文；此前这类信息只在日志里，界面零提示（2026-10-10 实机
         表现为"登录后白屏 + 不知原因"）。 -->
    <el-alert
      v-if="lastNetFailure"
      class="acct-alert"
      type="error"
      :closable="false"
      show-icon
      title="无法连接服务器"
      :description="lastNetFailure + '（若使用代理软件，请把该服务器域名设为直连后重试）'"
    />

    <!-- v21.201 A1：自动重建熔断可见化。此前"反复重建"对用户完全不可见，
         表现为「要登录很多次 + 期间必须多次关窗口」，用户无从判断该做什么。 -->
    <el-alert
      v-if="autoBootTripped"
      class="acct-alert"
      type="warning"
      :closable="false"
      show-icon
      title="已停止自动重建同步"
      description="检测到同步反复重建（本地与服务端数据不一致且自动修复未生效），已停止自动重建以保护你的数据。请点下方【强制全量同步】手动恢复，或重启程序。你的笔记没有被删除。"
    />
    <el-alert
      v-else-if="autoBootCooldownSec > 0"
      class="acct-alert"
      type="info"
      :closable="false"
      show-icon
      title="自动同步处于冷却期"
      :description="`刚完成过一次全量同步，约 ${autoBootCooldownSec} 秒内不再重复重建。期间你的修改会正常排队，不会丢失。`"
    />

    <div class="acct-state">
      <span
        class="acct-dot"
        :class="stateLevel"
      />
      <div class="acct-state-text">
        <strong>{{ stateLine }}</strong>
        <small v-if="st.last_login_at">上次登录 {{ st.last_login_at }}</small>
        <small v-else-if="st.server_url">服务器 {{ st.server_url }}</small>
        <small v-if="st.revalidate_due === true">距上次联网重验已超期，建议尽快登录确认</small>
        <!-- ⭐ v21.218：恢复态明确告知「无需重新登录」，避免用户反复重登 -->
        <small v-if="recoverable && !online">本机凭据完好，联网后会自动继续同步，无需重新登录</small>
        <small v-if="st.relogin_required === true">本机凭据已失效，需要重新登录</small>
      </div>
      <!-- ⭐ v21.222：按钮组独占一行（此前与状态文字同一行，长 URL 会把它们挤出容器） -->
      <div class="acct-actions">
        <el-button
          size="small"
          :loading="loading"
          @click="refresh"
        >
          刷新
        </el-button>
        <!-- ⭐ v21.221：立即重试 —— 打破 5/15/30min 长退避，给用户一个显式出口。
           214 实机：调度器终态后退避，用户面对「恢复中」只能干等，无法使用。 -->
        <el-button
          v-if="reloginRequired || recoverable || needAttention"
          size="small"
          type="primary"
          plain
          :loading="busy === 'retry'"
          @click="onRetryNow"
        >
          立即重试
        </el-button>
        <el-button
          size="small"
          type="danger"
          plain
          :loading="busy === 'resync'"
          @click="onForceResync"
        >
          强制全量同步
        </el-button>
      </div>
    </div>

    <!-- ⭐ v21.221：第四态的明确引导 —— 凭据作废时明确告知「必须重新登录」，
         并说明稍后会自动重试（不是彻底停手），避免用户反复重登或无限干等。 -->
    <el-alert
      v-if="reloginRequired"
      class="acct-alert"
      type="error"
      :closable="false"
      show-icon
      title="本机登录凭据已被服务器作废"
      :description="`请用下方登录框重新登录一次${retryInSec > 0 ? `（若网络恢复，系统也会在约 ${retryInMin} 分钟后自动重试）` : ''}。本机数据不会丢失。`"
    />
    <el-alert
      v-else-if="needAttention && !online"
      class="acct-alert"
      type="warning"
      :closable="false"
      show-icon
      title="自动同步遇到问题，已进入退避重试"
      :description="`系统会每隔一段时间自动重试${retryInSec > 0 ? `（下次约 ${retryInMin} 分钟后）` : ''}，期间你的修改会正常保存在本机，不会丢失。也可点击上方【立即重试】马上再试一次。`"
    />

    <el-alert
      v-if="needAttention"
      class="acct-alert"
      type="warning"
      :closable="false"
      show-icon
      title="自动同步已暂停，需要你处理"
      :description="heldReason ? `原因：${heldReason}` : '服务器数据已换代或本机游标落后，重建同步可恢复'"
    >
      <el-button
        size="small"
        type="primary"
        style="margin-top: 8px"
        :loading="busy === 'bootstrap'"
        @click="onBootstrap"
      >
        重建同步
      </el-button>
      <el-button
        size="small"
        type="danger"
        plain
        style="margin-top: 8px"
        :loading="busy === 'resync'"
        @click="onForceResync"
      >
        强制全量同步
      </el-button>
    </el-alert>

    <section class="acct-block">
      <h4>排障协助</h4>
      <p class="acct-hint">
        导出诊断包（应用日志、内置运行组件来源、WebView2 实际版本、本机档案代数与实体计数）。
        <strong>不含笔记正文</strong>，可安全发给维护者。
      </p>
      <el-button
        size="small"
        plain
        style="margin-top: 8px"
        :loading="busy === 'diag'"
        @click="onExportDiagnostics"
      >
        导出诊断包
      </el-button>
      <p
        v-if="diagPath"
        class="acct-hint"
        style="margin-top: 8px; word-break: break-all;"
      >
        已导出：{{ diagPath }}
      </p>
    </section>

    <section class="acct-block">
      <!-- ⭐ v21.218：恢复态（凭据在、暂未联网）下标题改为「切换账号」，
           避免"恢复中"却提示"登录服务器"造成"我明明登过"的困惑。 -->
      <h4>{{ loggedIn ? '切换到其他服务器 / 账号' : (recoverable ? '切换到其他服务器 / 账号' : '登录服务器') }}</h4>
      <label class="acct-field">
        <span>服务器地址</span>
        <el-input
          v-model="form.baseURL"
          :disabled="totp.active"
          placeholder="https://note.example.com（推荐域名；内网 IP 需同网络且未被代理接管）"
        />
        <!-- v21.202 P1：地址误配即时提示（error 同步禁用登录按钮） -->
        <p
          v-if="urlCheck.hint"
          class="acct-url-hint"
          :class="urlCheck.level"
        >{{ urlCheck.hint }}</p>
      </label>
      <label class="acct-field">
        <span>用户名</span>
        <el-input
          v-model="form.username"
          :disabled="totp.active"
          autocomplete="off"
          placeholder="账号"
        />
      </label>
      <label
        v-if="!totp.active"
        class="acct-field"
      >
        <span>密码</span>
        <el-input
          v-model="form.password"
          type="password"
          show-password
          autocomplete="new-password"
          placeholder="登录密码"
          @keyup.enter="onLogin"
        />
      </label>
      <label
        v-if="totp.active"
        class="acct-field"
      >
        <span>两步验证码</span>
        <el-input
          v-model="totp.code"
          placeholder="6 位动态码，或一枚恢复码"
          autocomplete="one-time-code"
          @keyup.enter="onVerifyTotp"
        />
      </label>
      <p
        v-if="formError"
        class="acct-err"
      >
        {{ formError }}
      </p>
      <div class="acct-actions">
        <el-button
          v-if="!totp.active"
          type="primary"
          :loading="busy === 'login'"
          :disabled="urlCheckBlocked"
          @click="onLogin"
        >
          登录
        </el-button>
        <template v-else>
          <el-button
            type="primary"
            :loading="busy === 'totp'"
            @click="onVerifyTotp"
          >
            验证并登录
          </el-button>
          <el-button @click="cancelTotp">
            返回重新登录
          </el-button>
        </template>
        <el-button
          v-if="loggedIn || accounting"
          :loading="busy === 'logout'"
          @click="onLogout"
        >
          退出登录
        </el-button>
      </div>
      <p class="acct-tip">
        密码只在提交瞬间存在于内存，不写入本机存储；登录凭据以系统级加密（DPAPI）封存，仅本机本用户可解。
      </p>
    </section>
  </div>
</template>

<style scoped>
/* ⭐ v21.222：卡片容器加宽 + 顶部状态区改「文字/按钮纵向可换行」布局。
 *
 * 215 实机缺陷：`.acct-state` 是横向 flex，长文案（服务器 URL + 账号 + 多行 small）
 * 把三个按钮（刷新/立即重试/强制全量同步）**挤出容器**，
 * 表现为「部分按钮超出内容、状态文字被挤成一条窄柱」。
 *
 * 修法要点：
 *   ① `.acct` 去掉 620px 硬上限（设置页本就该给足宽度）；
 *   ② `.acct-state` 加 `flex-wrap: wrap` 且文字块 `min-width: 0`
 *      —— `min-width:0` 是 flex 子项能正确收缩/换行的**必要条件**，
 *      缺了它长 URL 会把整行撑爆（这正是 215 的现象）；
 *   ③ 按钮组独占一行（`width:100%` + `flex-wrap`），不再与文字抢同一行。
 */
.acct {
  width: 100%;
  max-width: 860px;
}
.acct-desc {
  font-size: 13px;
  color: var(--text-muted);
  line-height: 1.7;
  margin: 8px 0 16px;
}
.acct-url-hint {
  margin: 4px 0 0;
  font-size: 12px;
  line-height: 1.5;
}
.acct-url-hint.error { color: var(--el-color-danger); }
.acct-url-hint.warn { color: var(--el-color-warning); }

.acct-alert {
  margin-bottom: 12px;
}
.acct-state {
  display: flex;
  flex-wrap: wrap; /* ⭐ v21.222：窄屏时按钮自动换到下一行，不再被挤出容器 */
  align-items: flex-start;
  gap: 10px 12px;
  padding: 12px;
  border-radius: var(--radius-md);
  background: var(--bg-tertiary, rgba(128, 128, 128, .08));
  margin-bottom: 16px;
}
.acct-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  margin-top: 5px;
  flex: 0 0 auto;
  background: var(--el-color-info, #909399);
}
.acct-dot.on {
  background: var(--el-color-success, #67c23a);
}
.acct-dot.half {
  background: var(--el-color-warning, #e6a23c);
}
/* ⭐ v21.221：需重新登录（凭据作废）——用 danger 色，与「恢复中」的 warning 区分 */
.acct-dot.alert {
  background: var(--el-color-danger, #f56c6c);
}
.acct-state-text {
  flex: 1 1 260px; /* ⭐ v21.222：给一个合理的基准宽度，配合 min-width:0 才能正确换行 */
  min-width: 0; /* flex 子项收缩的前提：缺它长 URL 会撑爆整行（215 实机现象） */
  display: flex;
  flex-direction: column;
  gap: 2px;
  /* 长 URL / 长状态文案强制断行，不允许横向溢出 */
  overflow-wrap: anywhere;
  word-break: break-word;
}
.acct-state-text strong {
  font-size: 14px;
  overflow-wrap: anywhere;
  word-break: break-word;
}
/* ⭐ v21.222：按钮组独占一行（宽度 100% + 自动换行），不再与状态文字抢同一行 */
.acct-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  width: 100%;
  padding-top: 2px;
}
.acct-state-text small {
  font-size: 12px;
  color: var(--text-muted);
}
.acct-block h4 {
  margin: 0 0 12px;
  font-size: 15px;
}
.acct-field {
  display: block;
  margin-bottom: 12px;
}
.acct-field span {
  display: block;
  font-size: 13px;
  color: var(--text-muted);
  margin-bottom: 4px;
}
.acct-err {
  font-size: 13px;
  color: var(--el-color-danger, #f56c6c);
  margin: 0 0 10px;
}
.acct-actions {
  display: flex;
  gap: 12px;
}
.acct-tip {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.7;
  margin-top: 12px;
}
</style>
