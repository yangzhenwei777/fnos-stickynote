<!--
  文件行数豁免登记（开发纪律 §5.1）
  ─────────────────────────────────────────────────────────────
  当前行数：以机器门禁实测为准（本注释不手写行数）。
  豁免原因：设置页容器与页签路由（2026-11-03 拆分轮已把 password/security/
           quota/totp 四个页签抽为 components/profile/* 子组件，共享面板
           样式在 styles/profile-panels.css，均以 .profile-container 前缀限定，
           防与 admin/Login 等同名类互渗）。
  拆分计划：TD-005 已偿还（原 972 行 → 子组件化），本文件为拆分后容器。
  追踪：docs/技术债清单.md · TD-005
-->
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
          <ProfilePasswordPanel />
        </div>

        <div
          v-else-if="activeTab === 'security'"
          class="tab-panel"
        >
          <ProfileSecurityPanel />
        </div>

        <div
          v-else-if="activeTab === 'quota'"
          class="tab-panel"
        >
          <ProfileQuotaPanel />
        </div>

        <div
          v-else-if="activeTab === 'totp'"
          class="tab-panel"
        >
          <ProfileTotpPanel />
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
import { ref } from 'vue'
import { IS_DESKTOP } from '@/api/local/transport'
import PortableBackupCard from '@/components/PortableBackupCard.vue'
import ConflictCenterCard from '@/components/ConflictCenterCard.vue'
import DesktopAccountCard from '@/components/DesktopAccountCard.vue'
import ProfilePasswordPanel from '@/components/profile/ProfilePasswordPanel.vue'
import ProfileSecurityPanel from '@/components/profile/ProfileSecurityPanel.vue'
import ProfileQuotaPanel from '@/components/profile/ProfileQuotaPanel.vue'
import ProfileTotpPanel from '@/components/profile/ProfileTotpPanel.vue'
import { UserFilled, Key, QuestionFilled, TrendCharts, FolderOpened, Refresh, Connection } from '@element-plus/icons-vue'
import '@/styles/profile-panels.css'

const activeTab = ref('password')
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
}
</style>
