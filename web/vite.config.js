import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import { VitePWA } from 'vite-plugin-pwa'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig(({ mode }) => ({
  // v2.1.2 移动端启动卡死根治 —— 基址按构建形态隔离：
  // - Web 构建用相对基址 './'：资源路径按「当前文档 URL」解析，从而同时适配
  //   ① fnOS Web 桌面（type:url + port 直连 http://host:13389/，站点根）
  //   ② fnOS 手机 App / 远程访问（经网关以 /app/<appname>/ 子路径加载）
  //   旧口径是根绝对路径 '/assets/…'，子路径下会被解析到宿主根 → 登录飞牛主站
  //   返回 404+text/html → <script type="module"> MIME 不符被拒执行 → 永久卡启动页。
  // - 桌面构建保持 '/'：Wails assetserver 恒在根，**桌面端路径语义零变化**。
  base: mode === 'desktop' ? '/' : './',
  plugins: [
    vue(),
    // Element Plus 按需导入：模板组件与 ElMessage/ElMessageBox 等编程式 API
    // 均由 resolver 自动注入并附带对应样式，替代全量 CSS 导入
    AutoImport({
      resolvers: [ElementPlusResolver()]
    }),
    Components({
      resolvers: [ElementPlusResolver()]
    }),
    // PWA（v21.29）：移动端可安装 + App 壳离线缓存
    // 只缓存静态资源（App 壳），不缓存 /api 数据（避免登录态/敏感数据落本地）
    VitePWA({
      // v1.0.175 P0-A（v20 §120）：桌面构建不注入 Service Worker 注册脚本
      injectRegister: mode === 'desktop' ? false : undefined,
      registerType: 'autoUpdate',
      includeAssets: ['favicon.svg', 'favicon.png'],
      manifest: {
        name: 'Sticky note',
        short_name: 'Sticky note',
        description: '轻量、精美的多用户笔记管理应用',
        lang: 'zh-CN',
        display: 'standalone',
        // v2.1.2：相对 start_url/scope——子路径挂载（/app/<appname>/）下
        // 由 manifest 自身位置解析；根路径下等价于 '/'，行为不变。
        start_url: './',
        scope: './',
        theme_color: '#3B82F6',
        background_color: '#ffffff',
        icons: [
          {
            src: 'favicon.svg',
            sizes: 'any',
            type: 'image/svg+xml',
            purpose: 'any'
          },
          {
            src: 'icon-192.png',
            sizes: '192x192',
            type: 'image/png',
            purpose: 'any'
          },
          {
            src: 'icon-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'any'
          },
          {
            src: 'icon-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable'
          }
        ]
      },
      workbox: {
        globPatterns: ['**/*.{js,css,html,svg,png,ico,woff2}'],
        // Edge 安卓安装要求：SW 立即接管当前页面（clientsClaim）+ 新版立即激活（skipWaiting）
        clientsClaim: true,
        skipWaiting: true,
        // SPA 导航：网络优先，离线回退 index.html（前端路由可离线直达）
        // /api 与 /uploads 明确排除——数据/上传走网络，绝不缓存到本地
        // v2.1.2：回退地址改**相对**，子路径挂载时不会回退到宿主根的 /index.html。
        navigateFallback: 'index.html',
        // v2.1.2：denylist 去掉行首锚点——子路径下导航 URL 形如
        // /app/<appname>/api/…，带 ^ 的正则匹配不到，会把 API 误吞进 fallback。
        navigateFallbackDenylist: [/\/api\//, /\/uploads\//],
        runtimeCaching: [
          {
            urlPattern: /^\/api\//,
            handler: 'NetworkOnly'
          },
          {
            urlPattern: /^\/uploads\//,
            handler: 'NetworkOnly'
          }
        ]
      }
    })
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  build: {
    // 浏览器兼容目标（-51 白屏根因修复之一）：
    // vite 8 默认 'baseline-widely-available'（Chrome 111+），产物含 ES2021 逻辑赋值
    // （||=/??=）与 import.meta.resolve 依赖，fnOS 内置浏览器内核解析失败 → 白屏。
    // 显式钉回 vite 5 时代 'modules' 档（Chrome 87+ / Edge 88 / Firefox 78 / Safari 14），
    // 等价于 -50 及更早版本已验证可用的兼容水平。
    target: ['es2020', 'edge88', 'firefox78', 'chrome87', 'safari14'],
    // 拆包：不使用 manualChunks（-52 白屏根因）——
    // vite 8/rolldown 下 manualChunks 函数式拆包的 chunk 归属与 rollup 不同，
    // 导致 vue 运行时被塞进 element-plus chunk、vendor 反向依赖 element-plus
    // （依赖倒置 → 模块初始化 TypeError: e is not a function → 白屏）。
    // 改由 rolldown 自动拆包（内部 codeSplitting 策略成熟，可正确处理共享依赖）。
    // 拆包优化留待需要时再评估（评审 P1-3 的 chunk 缓存优化暂缓）。
  },
  server: {
    port: 3000,
    proxy: {
      '/api': 'http://localhost:13389',
      // dev 代理补 /uploads：本地 dev 下笔记内图片直连后端上传目录（仅 dev server 配置，不影响构建产物）
      '/uploads': 'http://localhost:13389'
    }
  },
  // v21.54 P1-A：Vitest 配置——jsdom 环境 + 处理 CSS（element-plus 按需样式在测试中静默处理，不抛 Unknown extension）
  test: {
    environment: 'jsdom',
    css: { include: /\.css$/ }
  }
}))
