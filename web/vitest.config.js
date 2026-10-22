import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// v21.54 P1-A：独立 Vitest 配置（避免 vite.config 的 PWA/ElementPlusResolver 干扰测试，
// 后者会在测试中解析 element-plus 真实组件并加载其 CSS）。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  test: {
    environment: 'jsdom',
    css: true,
    include: ['src/__tests__/**/*.test.js']
  }
})
