<template>
  <FolderPasswordDialog />
  <router-view />
</template>

<script setup>
// v21.95 F-4（审计 P2）：暗色模式单一真相源。
// 原主题由本组件（matchMedia 系统偏好）与 composables/useTheme.js（localStorage 手动）双源管理，
// 互相覆盖且 Element Plus class 不一致（系统暗色时组件不跟随）。现统一由 useTheme.js 管理
// （main.js 的 initTheme 已调用），本组件不再直接操作主题，消除双源冲突。

// v21.97（用户反馈 B2）：应用启动即从 DB 加载公共配置（site_title/allow_register）。
// 此前 fetchPublicConfig 仅在系统配置页保存后调用，MainLayout/AppSidebar 只读 store 默认值
// → 修改应用标题后刷新即恢复默认、左上角与设置页脱节。放全局入口保证登录页/主界面/侧栏
// 每次刷新都拿到 DB 真实值。
import { onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { useConfigStore } from '@/stores/config'
import { useFolderStore } from '@/stores/folder'
import FolderPasswordDialog from '@/components/FolderPasswordDialog.vue'
import { IS_DESKTOP } from '@/api/local/transport'
import { fetchRuntimeStatus } from '@/api/local/desktopApi'
// ⭐ v21.220：桌面状态仓库在**应用入口**启动（不是组件里）。
// 213 实机教训：组件挂载时机不可靠（首屏恰逢 WebView2 初始化 + 换代窗口），
// 而状态仓库的生命周期必须 = 应用生命周期，且自带 20s 无条件心跳。
import { startDesktopStatus } from '@/api/local/desktopStatus'

const config = useConfigStore()
const folderStore = useFolderStore()

onMounted(() => {
  config.fetchPublicConfig()
  // v2.3.0：桌面端解锁态以 Go 进程内存为准，启动时回填前端展示态。
  if (IS_DESKTOP) {
    folderStore.restoreUnlocks()
  }
  // v21.196 O3：钉版 WebView2 若已降级到系统内核，界面可能空白/崩溃（Evergreen 153 已实证）。
  // 降级不再是静默——这里给一条不自动消失的告警，用户可据此决定是否重装。
  if (IS_DESKTOP) {
    // ⚠ 仓库先启动：它必须在任何组件取状态**之前**就开始维护，
    //   否则账号卡片挂载时拿到的仍是空对象（213 事故的直接成因）。
    startDesktopStatus()
    fetchRuntimeStatus().then(st => {
      if (st && st.degraded) {
        ElMessage({
          type: 'error',
          duration: 0,
          showClose: true,
          message: `内置运行组件不可用，已回退系统浏览器内核（${st.webview2 || '版本未知'}），可能出现界面空白或崩溃。建议重新安装本应用。技术信息：${st.error || '未知原因'}`
        })
      }
    })
  }
})
</script>
