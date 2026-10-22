<template>
  <!-- v21.87：PC 端保持左侧 AppSidebar（v21.86 起）+ 全宽内容区；
       移动端（≤768px）恢复 v121 原版蓝色顶部菜单栏（用户需求：改回原来的样式），
       顶栏含 LOGO/便签、主题切换、用户菜单（个人中心/回收站/管理项/退出）。 -->
  <div
    class="main-layout"
    :class="{ mobile: isMobile }"
  >
    <!-- PC 端：左侧菜单栏 -->
    <AppSidebar v-if="!isMobile" />

    <!-- 移动端：原版顶部菜单栏（样式取自 v121 MainLayout） -->
    <header
      v-if="isMobile"
      class="mobile-header"
    >
      <div class="header-inner">
        <div class="header-left">
          <div
            class="logo"
            @click="goHome"
          >
            <div class="logo-icon">
              <el-icon><Document /></el-icon>
            </div>
            <span class="logo-text">{{ config.siteTitle }}</span>
          </div>
        </div>
        <div class="header-right">
          <el-tooltip
            :content="isDark ? '切换亮色模式' : '切换暗色模式'"
            placement="bottom"
          >
            <button
              class="theme-toggle"
              :title="isDark ? '切换亮色模式' : '切换暗色模式'"
              @click="toggleTheme"
            >
              <el-icon>
                <template v-if="isDark">
                  <svg
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                  ><circle
                    cx="12"
                    cy="12"
                    r="5"
                  /><line
                    x1="12"
                    y1="1"
                    x2="12"
                    y2="3"
                  /><line
                    x1="12"
                    y1="21"
                    x2="12"
                    y2="23"
                  /><line
                    x1="4.22"
                    y1="4.22"
                    x2="5.64"
                    y2="5.64"
                  /><line
                    x1="18.36"
                    y1="18.36"
                    x2="19.78"
                    y2="19.78"
                  /><line
                    x1="1"
                    y1="12"
                    x2="3"
                    y2="12"
                  /><line
                    x1="21"
                    y1="12"
                    x2="23"
                    y2="12"
                  /><line
                    x1="4.22"
                    y1="19.78"
                    x2="5.64"
                    y2="18.36"
                  /><line
                    x1="18.36"
                    y1="5.64"
                    x2="19.78"
                    y2="4.22"
                  /></svg>
                </template>
                <template v-else>
                  <svg
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                  ><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" /></svg>
                </template>
              </el-icon>
            </button>
          </el-tooltip>
          <el-dropdown
            trigger="click"
            class="user-dropdown"
            @command="handleUserCmd"
          >
            <div class="user-info">
              <div class="user-avatar">
                {{ user?.username?.charAt(0)?.toUpperCase() || 'U' }}
              </div>
              <span class="user-name">{{ user?.username }}</span>
              <el-icon class="dropdown-icon">
                <ArrowDown />
              </el-icon>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="profile">
                  <el-icon><User /></el-icon>个人中心
                </el-dropdown-item>
                <el-dropdown-item
                  command="trash"
                  divided
                >
                  <el-icon><Delete /></el-icon>回收站
                </el-dropdown-item>
                <template v-if="user?.role === 'admin'">
                  <el-dropdown-item
                    command="admin-users"
                    divided
                  >
                    <el-icon><UserFilled /></el-icon>用户管理
                  </el-dropdown-item>
                  <el-dropdown-item
                    command="admin-configs"
                    divided
                  >
                    <el-icon><Setting /></el-icon>系统配置
                  </el-dropdown-item>
                  <el-dropdown-item
                    command="admin-backup"
                    divided
                  >
                    <el-icon><FolderOpened /></el-icon>系统备份
                  </el-dropdown-item>
                  <el-dropdown-item
                    command="admin-logs"
                    divided
                  >
                    <el-icon><Document /></el-icon>操作日志
                  </el-dropdown-item>
                </template>
                <!-- v21.168：移动端补「关于」入口——此前「关于」只在 PC 端侧边栏
                     （AppSidebar），移动端顶栏下拉共用同一份 DOM 但菜单内无该项
                     ⇒ 手机端完全看不到实际版本号。复用既有 useAbout.showAbout，
                     与侧边栏同一实现、同一版本来源（桌面绑定 / /api/version）。
                     v21.169：按用户要求移到「退出登录」上方（退出登录是破坏性操作，置底）。 -->
                <el-dropdown-item
                  command="about"
                  divided
                >
                  <el-icon><InfoFilled /></el-icon>关于
                </el-dropdown-item>
                <el-dropdown-item
                  command="logout"
                  divided
                >
                  <el-icon><SwitchButton /></el-icon>退出登录
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </div>
    </header>

    <main
      class="main-content"
      :class="{ 'main-content--white': isMobile && route.name === 'NotesList' }"
    >
      <router-view />
    </main>
  </div>
</template>

<script setup>
import { withBase } from '@/utils/appBase'
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Document, User, SwitchButton, Setting, UserFilled, ArrowDown, Delete, InfoFilled } from '@element-plus/icons-vue'
import AppSidebar from '@/components/AppSidebar.vue'
import { useAuthStore } from '@/stores/auth'
import { useFolderStore } from '@/stores/folder'
import { useTheme } from '@/composables/useTheme'
import { message } from '@/utils/message'
import { showAbout } from '@/composables/useAbout' // v21.168：移动端「关于」入口
import { useConfigStore } from '@/stores/config'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const folderStore = useFolderStore()
const { isDark, toggleTheme } = useTheme()
const config = useConfigStore() // F-6：页头应用标题读取全局 siteTitle，随高级设置即时更新

const user = computed(() => auth.user)
const isMobile = ref(false)

function checkMobile() {
  isMobile.value = window.innerWidth <= 768
}

onMounted(() => {
  checkMobile()
  window.addEventListener('resize', checkMobile)
})

onUnmounted(() => {
  window.removeEventListener('resize', checkMobile)
})

// LOGO/便签 → 首页
// v21.89：移动端跳转 /notes-list（卡片列表页，用户确认——文件夹选项在该页标题旁）；
//          PC 端保持原行为（切"我的笔记"文件夹回主页）。homeTick 信号已不再被
//          NotesView 使用（移动端文件夹栏整个移除），保留以免破坏 store 契约。
// v21.208：移动端点击应用名改为进入「全部笔记」（原为「我的笔记」文件夹）。
function goHome() {
  if (isMobile.value) {
    folderStore.selectFolder('all')
    router.push('/notes-list')
    return
  }
  folderStore.selectFolder('default')
  folderStore.goHomeTick()
  router.push('/')
}

async function handleUserCmd(cmd) {
  if (cmd === 'profile') router.push('/profile')
  else if (cmd === 'trash') router.push('/trash')
  else if (cmd === 'admin-users') router.push('/admin-users')
  else if (cmd === 'admin-configs') router.push('/admin-limits')
  else if (cmd === 'admin-logs') router.push('/admin-logs')
  else if (cmd === 'admin-backup') router.push('/admin-backup')
  else if (cmd === 'about') showAbout() // v21.168：移动端「关于」，与侧边栏同一实现
  else if (cmd === 'logout') {
    // v21.108 P1-10：明示登出=全设备下线（后端 logout 递增 token_version 踢掉所有设备）
    const ok = await message.confirm('确定要退出登录吗？退出后该账号在**所有设备**上都会下线，需要重新登录。', {
      title: '提示',
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'info'
    })
    if (!ok) return
    await auth.logout()
    window.location.replace(withBase('/login'))
  }
}
</script>

<style scoped>
.main-layout {
  display: flex;
  height: 100vh;
  overflow: hidden;
  background: var(--bg-secondary);
}

/* 移动端：顶栏在上、内容在下（纵向布局） */
.main-layout.mobile {
  flex-direction: column;
}

/* v21.86：内容区全宽（原 max-width:1400px 居中已移除，设备显示宽度自适应）。
   各页面容器自带 padding；NotesView 三栏布局需要满高无 padding。 */
.main-content {
  flex: 1;
  min-width: 0;
  overflow-y: auto;
  height: 100vh;
}

.main-layout.mobile .main-content {
  height: auto;
  min-height: 0;
}

/* v21.213：手机端「全部笔记」列表页底色为纯白（其余页面仍为 --bg-secondary 极浅蓝白）。
   背景放在 main 上而非页面容器上，短列表时下方空白区域也是白色 */
.main-layout.mobile .main-content--white {
  background: var(--bg-primary);
}

/* ---- 移动端顶栏（v121 原版样式回归）---- */
.mobile-header {
  flex-shrink: 0;
  /* v21.207：保留蓝色顶栏（v21.87 用户需求），去阴影/渐变/毛玻璃，改实心 */
  background: var(--header-gradient);
  padding: 0;
  height: 56px;
  position: relative;
  z-index: 100;
}

.header-inner {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 16px;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.logo {
  display: flex;
  align-items: center;
  gap: 12px;
  text-decoration: none;
  color: white;
  cursor: pointer;
}

.logo-icon {
  width: 36px;
  height: 36px;
  background: rgba(255, 255, 255, 0.18);
  border-radius: var(--radius-md);
  display: flex;
  align-items: center;
  justify-content: center;
}

.logo-icon .el-icon {
  font-size: 20px;
}

.logo-text {
  font-size: 16px;
  font-weight: 600;
  letter-spacing: -0.01em;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.theme-toggle {
  width: 40px;
  height: 40px;
  border: none;
  border-radius: var(--radius-md);
  background: rgba(255, 255, 255, 0.15);
  color: white;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background-color 0.15s;
}

.theme-toggle:hover {
  background: rgba(255, 255, 255, 0.25);
}

.theme-toggle .el-icon {
  font-size: 20px;
}

.user-dropdown {
  cursor: pointer;
}

.user-info {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 12px;
  border-radius: var(--radius-md);
  background: rgba(255, 255, 255, 0.15);
  transition: background-color 0.15s;
}

.user-info:hover {
  background: rgba(255, 255, 255, 0.25);
}

.user-avatar {
  width: 30px;
  height: 30px;
  border-radius: var(--radius-sm);
  background: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  font-weight: 600;
  color: #2563eb;
}

.user-name {
  color: white;
  font-size: 13px;
  font-weight: 500;
  max-width: 60px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dropdown-icon {
  color: rgba(255, 255, 255, 0.8);
  font-size: 14px;
}
</style>
