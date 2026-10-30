<template>
  <!-- v21.86 左侧菜单栏（PC 端常驻，移动端由 MainLayout 隐藏）。
       展开态：LOGO/全部笔记/文件夹(含子列表)/回收站 + 暗色切换 + 用户菜单；
       收起态：仅图标列；用户菜单收起态仍可弹出。右缘 DragResizer 拖动自定义宽度。 -->
  <aside
    class="app-sidebar"
    :class="{ collapsed }"
    :style="collapsed ? undefined : { width: width + 'px' }"
  >
    <!-- 顶部：LOGO + 便签 + 收起箭头 -->
    <div class="sb-top">
      <div
        class="sb-logo"
        :title="collapsed ? 'Sticky note' : ''"
        @click="goHome"
      >
        <div class="sb-logo-icon">
          <el-icon><Document /></el-icon>
        </div>
        <span
          v-if="!collapsed"
          class="sb-logo-text"
        >{{ config.siteTitle }}</span>
      </div>
      <button
        v-if="!collapsed"
        class="sb-collapse-btn"
        title="收起菜单"
        @click="collapsed = true"
      >
        <el-icon><ArrowLeft /></el-icon>
      </button>
    </div>

    <!-- 收起态的展开按钮 -->
    <button
      v-if="collapsed"
      class="sb-expand-btn"
      title="展开菜单"
      @click="collapsed = false"
    >
      <el-icon><ArrowRight /></el-icon>
    </button>

    <!-- 中部菜单 -->
    <nav class="sb-menu">
      <!-- 全部笔记 -->
      <div
        class="sb-item"
        :class="{ active: isNotesRoute && activeFolderId === 'all' }"
        :title="collapsed ? '全部笔记' : ''"
        @click="showAllNotes"
      >
        <el-icon class="sb-item-icon">
          <Files />
        </el-icon>
        <span
          v-if="!collapsed"
          class="sb-item-text"
        >全部笔记</span>
      </div>

      <!-- 文件夹（父级） -->
      <div
        class="sb-item"
        :class="{ 'active-group': !collapsed && isNotesRoute && activeFolderId !== 'all' }"
        :title="collapsed ? '文件夹' : ''"
        @click="collapsed ? expandFromIcon() : foldersExpanded = !foldersExpanded"
      >
        <el-icon class="sb-item-icon">
          <Folder />
        </el-icon>
        <span
          v-if="!collapsed"
          class="sb-item-text"
        >文件夹</span>
        <button
          v-if="!collapsed"
          class="sb-add-btn"
          title="新建文件夹"
          @click.stop="openCreateDialog"
        >
          <el-icon><Plus /></el-icon>
        </button>
        <el-icon
          v-if="!collapsed"
          class="sb-caret"
          :class="{ open: foldersExpanded }"
        >
          <CaretBottom />
        </el-icon>
      </div>

      <!-- 文件架子列表（仅展开态） -->
      <div
        v-if="!collapsed && foldersExpanded"
        class="sb-folder-list"
      >
        <div
          class="sb-folder-item"
          :class="{ active: isNotesRoute && activeFolderId === 'default' }"
          @click="selectFolder('default')"
        >
          <el-icon class="sb-folder-icon">
            <Document />
          </el-icon>
          <span class="sb-folder-name">我的笔记</span>
        </div>
        <div
          v-for="(f, i) in customFolders"
          :key="f.id"
          class="sb-folder-item"
          :class="{ active: isNotesRoute && activeFolderId === f.id }"
        >
          <el-icon class="sb-folder-icon">
            <Lock v-if="folderStore.isFolderLocked(f)" />
            <Folder v-else />
          </el-icon>
          <span
            class="sb-folder-name"
            @click="selectFolder(f.id)"
          >{{ f.name }}</span>
          <el-dropdown
            trigger="click"
            class="sb-folder-more"
            @command="(cmd) => handleFolderCmd(cmd, f)"
          >
            <button
              class="sb-more-btn"
              title="文件夹选项"
              @click.stop
            >
              <el-icon><MoreFilled /></el-icon>
            </button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="rename">
                  <el-icon><EditPen /></el-icon>重命名
                </el-dropdown-item>
                <el-dropdown-item
                  v-if="!f.is_encrypted"
                  command="encrypt"
                >
                  <el-icon><Lock /></el-icon>加密
                </el-dropdown-item>
                <el-dropdown-item
                  v-else
                  command="decrypt"
                >
                  <el-icon><Unlock /></el-icon>解密
                </el-dropdown-item>
                <el-dropdown-item
                  command="delete"
                  class="danger-item"
                >
                  <el-icon><Delete /></el-icon>删除
                </el-dropdown-item>
                <el-dropdown-item
                  command="up"
                  divided
                  :disabled="i === 0"
                >
                  <el-icon><Top /></el-icon>向上移动
                </el-dropdown-item>
                <el-dropdown-item
                  command="down"
                  :disabled="i === customFolders.length - 1"
                >
                  <el-icon><Bottom /></el-icon>向下移动
                </el-dropdown-item>
                <el-dropdown-item
                  command="top"
                  :disabled="i === 0"
                >
                  <el-icon><Upload /></el-icon>移动至顶部
                </el-dropdown-item>
                <el-dropdown-item
                  command="bottom"
                  :disabled="i === customFolders.length - 1"
                >
                  <el-icon><Download /></el-icon>移动至底部
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </div>

      <!-- 回收站 -->
      <div
        class="sb-item"
        :class="{ active: route.path === '/trash' }"
        :title="collapsed ? '回收站' : ''"
        @click="router.push('/trash')"
      >
        <el-icon class="sb-item-icon">
          <Delete />
        </el-icon>
        <span
          v-if="!collapsed"
          class="sb-item-text"
        >回收站</span>
      </div>
    </nav>

    <!-- 底部：暗色切换 + 用户 -->
    <div class="sb-bottom">
      <div
        class="sb-item"
        :title="collapsed ? (isDark ? '切换亮色模式' : '切换暗色模式') : ''"
        @click="toggleTheme"
      >
        <el-icon class="sb-item-icon">
          <Moon v-if="isDark" />
          <Sunny v-else />
        </el-icon>
        <span
          v-if="!collapsed"
          class="sb-item-text"
        >{{ isDark ? '亮色模式' : '暗色模式' }}</span>
      </div>

      <!-- 用户菜单（收起态仍可弹出） -->
      <el-dropdown
        trigger="click"
        class="sb-user-dropdown"
        popper-class="sb-user-popper"
        :placement="collapsed ? 'right-start' : 'top-start'"
        @command="handleUserCmd"
      >
        <div
          class="sb-item sb-user-item"
          :title="collapsed ? user?.username : ''"
        >
          <div class="sb-avatar">
            {{ user?.username?.charAt(0)?.toUpperCase() || 'U' }}
          </div>
          <span
            v-if="!collapsed"
            class="sb-item-text sb-username"
          >{{ user?.username }}</span>
          <el-icon
            v-if="!collapsed"
            class="sb-user-caret"
          >
            <ArrowUp />
          </el-icon>
        </div>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="profile">
              <el-icon><User /></el-icon>个人中心
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

    <!-- 新建/重命名文件夹弹窗 -->
    <el-dialog
      v-model="dialogVisible"
      :title="dialogMode === 'create' ? '新建文件夹' : '重命名文件夹'"
      width="400px"
      :close-on-click-modal="false"
      append-to-body
    >
      <el-input
        v-model="dialogInput"
        :placeholder="dialogMode === 'create' ? '请输入文件夹名称' : '请输入新名称'"
        maxlength="32"
        show-word-limit
        clearable
        @keyup.enter="submitDialog"
      />
      <template #footer>
        <el-button @click="dialogVisible = false">
          取消
        </el-button>
        <el-button
          type="primary"
          :loading="dialogLoading"
          @click="submitDialog"
        >
          确定
        </el-button>
      </template>
    </el-dialog>
  </aside>
  <!-- 展开态右缘分割线：拖动自定义宽度（收起态隐藏） -->
  <DragResizer
    v-if="!collapsed"
    v-model="width"
    :min="160"
    :max="300"
    @resize-end="persistWidth"
  />
</template>

<script setup>
import { withBase } from '@/utils/appBase'
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessageBox } from 'element-plus'
import { useAuthStore } from '@/stores/auth'
import { useFolderStore } from '@/stores/folder'
import { useFolderSecurityStore } from '@/stores/folderSecurity'
import { useTheme } from '@/composables/useTheme'
import { message } from '@/utils/message'
import DragResizer from '@/components/DragResizer.vue'
import { useConfigStore } from '@/stores/config'
import { showAbout } from '@/composables/useAbout'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const folderStore = useFolderStore()
const folderSecurity = useFolderSecurityStore()
const { isDark, toggleTheme } = useTheme()
const config = useConfigStore() // F-6：侧栏 LOGO 文字读取全局 siteTitle，随高级设置即时更新

const user = computed(() => auth.user)
const customFolders = computed(() => folderStore.customFolders)
const activeFolderId = computed(() => folderStore.activeFolderId)
const isNotesRoute = computed(() => route.path === '/')

// ---- 收起/展开 + 宽度（localStorage 持久化）----
const collapsed = ref(localStorage.getItem('app_sidebar_collapsed') === '1')
const foldersExpanded = ref(localStorage.getItem('folders_expanded') !== '0')
const width = ref(parseInt(localStorage.getItem('app_sidebar_width'), 10) || 200)

watch(collapsed, (v) => localStorage.setItem('app_sidebar_collapsed', v ? '1' : '0'))
watch(foldersExpanded, (v) => localStorage.setItem('folders_expanded', v ? '1' : '0'))
function persistWidth() {
  localStorage.setItem('app_sidebar_width', String(width.value))
}

function expandFromIcon() {
  // 收起态点击文件夹图标：展开侧边栏（用户需求：点击图标后展开侧边栏）
  collapsed.value = false
  foldersExpanded.value = true
}

// ---- 导航 ----
function goHome() {
  // LOGO/便签 → 全部笔记（2026-09-23 用户拍板双端统一；原为默认文件夹视图/我的笔记）
  folderStore.selectFolder('all')
  router.push('/')
}
function showAllNotes() {
  folderStore.selectFolder('all')
  router.push('/')
}
function selectFolder(key) {
  if (typeof key === 'number') {
    const f = folderStore.folders.find(x => x.id === key)
    if (f && folderStore.isFolderLocked(f)) {
      // v2.3.0：点击锁定文件夹先验密，取消则不切换选中项
      folderSecurity.openUnlock(f, () => {
        folderStore.selectFolder(key)
        router.push('/')
        folderStore.bumpNotesRefresh()
      })
      return
    }
  }
  folderStore.selectFolder(key)
  router.push('/')
}

// ---- 文件夹操作 ----
const dialogVisible = ref(false)
const dialogMode = ref('create') // create | rename
const dialogInput = ref('')
const dialogLoading = ref(false)
let renameTarget = null

function openCreateDialog() {
  dialogMode.value = 'create'
  dialogInput.value = ''
  dialogVisible.value = true
}

function openRenameDialog(f) {
  dialogMode.value = 'rename'
  renameTarget = f
  dialogInput.value = f.name
  dialogVisible.value = true
}

async function submitDialog() {
  const name = dialogInput.value.trim()
  if (!name) {
    message.error('文件夹名称不能为空')
    return
  }
  dialogLoading.value = true
  try {
    if (dialogMode.value === 'create') {
      const f = await folderStore.createNewFolder(name)
      if (f) {
        dialogVisible.value = false
        foldersExpanded.value = true
        // v21.107 P1-11：创建后自动切入新文件夹——与移动端行为统一（此前 PC 停留原地易被误以为没生效）
        folderStore.selectFolder(f.id)
        message.success('创建成功')
      }
    } else if (renameTarget) {
      const ok = await folderStore.renameFolderById(renameTarget.id, name)
      if (ok) {
        dialogVisible.value = false
        message.success('重命名成功')
      }
    }
  } finally {
    dialogLoading.value = false
  }
}

async function handleFolderCmd(cmd, f) {
  if (cmd === 'rename') {
    openRenameDialog(f)
  } else if (cmd === 'encrypt') {
    folderSecurity.openEncrypt(f)
  } else if (cmd === 'decrypt') {
    folderSecurity.openDecrypt(f)
  } else if (cmd === 'delete') {
    await confirmDeleteFolder(f)
  } else if (cmd === 'up' || cmd === 'down' || cmd === 'top' || cmd === 'bottom') {
    await folderStore.moveFolderTo(f.id, cmd)
  }
}

async function confirmDeleteFolder(f) {
  const ok = await message.confirm(`确定删除文件夹【${f.name}】吗？`, {
    title: '删除文件夹',
    confirmButtonText: '删除',
    cancelButtonText: '取消',
    type: 'warning',
    danger: true
  })
  if (!ok) return
  const res = await folderStore.removeFolder(f.id)
  if (res.ok) {
    message.success('删除成功')
  } else if (res.notEmpty) {
    // 用户需求：文件夹内还有笔记时，弹窗提示且只显示确定按钮
    ElMessageBox.alert('文件夹内还有笔记，无法删除。', '提示', {
      confirmButtonText: '确定',
      type: 'warning'
    })
  } else if (res.locked) {
    ElMessageBox.alert('文件夹已加密，请先解锁后再删除。', '提示', {
      confirmButtonText: '确定',
      type: 'warning'
    })
  }
}

// ---- 用户菜单 ----
async function handleUserCmd(cmd) {
  if (cmd === 'profile') router.push('/profile')
  else if (cmd === 'about') await showAbout()
  else if (cmd === 'admin-users') router.push('/admin-users')
  else if (cmd === 'admin-configs') router.push('/admin-limits')
  else if (cmd === 'admin-logs') router.push('/admin-logs')
  else if (cmd === 'admin-backup') router.push('/admin-backup')
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

onMounted(() => {
  if (!folderStore.loaded) folderStore.loadFolders()
})
</script>

<style scoped>
.app-sidebar {
  display: flex;
  flex-direction: column;
  height: 100vh;
  /* v21.207 改版：导航栏用冷调浅蓝白底，与白色列表/编辑区形成层次 */
  background: var(--bg-secondary);
  border-right: 1px solid var(--border-color);
  overflow: hidden;
  flex-shrink: 0;
  transition: width 0.2s ease;
}

.app-sidebar.collapsed {
  width: 60px !important;
}

.sb-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 12px;
  min-height: 60px;
}

.collapsed .sb-top {
  justify-content: center;
  padding: 0;
}

.sb-logo {
  display: flex;
  align-items: center;
  gap: 10px;
  cursor: pointer;
  min-width: 0;
}

.sb-logo:hover .sb-logo-text {
  color: var(--primary-color);
}

.sb-logo-icon {
  width: 28px;
  height: 28px;
  border-radius: var(--radius-sm);
  /* v21.164：去渐变，实心主色（图2 基线：蓝色只用于主要操作与功能图标） */
  background: var(--primary-color);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 16px;
  flex-shrink: 0;
}

.sb-logo-text {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
}

.sb-collapse-btn,
.sb-expand-btn {
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.sb-collapse-btn:hover,
.sb-expand-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.sb-expand-btn {
  margin: 8px auto 0;
}

.sb-menu {
  flex: 1;
  padding: 10px 8px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.sb-item {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 36px;
  padding: 0 10px;
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--text-secondary);
  font-weight: 500;
  white-space: nowrap;
  min-width: 0;
}

/* v21.164：悬停底纹统一走 --bg-hover（冷灰蓝），替代灰块感更重的 --bg-tertiary */
.sb-item:hover {
  background: var(--bg-hover);
}

.sb-item.active {
  background: var(--primary-tint);
  color: var(--primary-ink);
  font-weight: 600;
}

.sb-item.active-group {
  color: var(--primary-ink);
}

.sb-item-icon {
  font-size: 18px;
  flex-shrink: 0;
}

.sb-item-text {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: 14px;
}

.collapsed .sb-item,
.collapsed .sb-user-item {
  justify-content: center;
  padding: 0;
}

/* 文件夹父级行的 + 与下插符 */
.sb-add-btn {
  width: 22px;
  height: 22px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.sb-add-btn:hover {
  background: var(--primary-color);
  color: #fff;
}

.sb-caret {
  font-size: 12px;
  color: var(--text-muted);
  transition: transform 0.2s;
  flex-shrink: 0;
}

.sb-caret.open {
  transform: rotate(180deg);
}

/* 文件架子列表 */
.sb-folder-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 2px 8px 6px 24px;
}

.sb-folder-item {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 32px;
  padding: 0 10px;
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--text-secondary);
  min-width: 0;
}

.sb-folder-item:hover {
  background: var(--bg-hover);
}

.sb-folder-item.active {
  background: var(--primary-tint);
  color: var(--primary-ink);
  font-weight: 600;
}

.sb-folder-icon {
  font-size: 15px;
  flex-shrink: 0;
}

.sb-folder-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
}

.sb-more-btn {
  width: 22px;
  height: 22px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  opacity: 0;
  transition: opacity 0.2s;
}

.sb-folder-item:hover .sb-more-btn {
  opacity: 1;
}

.sb-more-btn:hover {
  background: var(--border-color);
  color: var(--text-primary);
}

/* 底部 */
.sb-bottom {
  border-top: 1px solid var(--border-light);
  padding: 10px 8px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.sb-user-item {
  gap: 10px;
  /* v21.88 修正：hover 底纹须全宽跟随侧边栏宽度（与上方暗色模式行一致）。
     v21.87 曾误解为"内容固定宽度"加 fit-content，导致灰色底纹只有一小块。 */
}

/* v21.89 真凶：el-dropdown 默认 display:inline-flex 且宽度收缩包裹内容，
   外层包裹器把 sb-user-item 压成内容宽，导致底纹（hover 在 sb-item 上）
   永远只有内容宽。让包裹器与行均占满侧边栏宽度。 */
.sb-user-dropdown {
  width: 100%;
}

.sb-user-item {
  width: 100%;
}

/* 用户名占据剩余空间（底纹全宽的关键），超长截断 */
.sb-user-item .sb-item-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sb-avatar {
  width: 28px;
  height: 28px;
  border-radius: var(--radius-sm);
  background: var(--primary-tint);
  color: var(--primary-ink);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  font-weight: 600;
  flex-shrink: 0;
}

.sb-username {
  font-size: 13px;
}

.sb-user-caret {
  font-size: 12px;
  color: var(--text-muted);
  flex-shrink: 0;
}

/* 下拉菜单内 router 项样式（Teleport 到 body，全局样式兜底见 global.css） */
:global(.el-dropdown-menu__item) {
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
