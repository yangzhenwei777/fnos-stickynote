import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist/**', 'node_modules/**']),

  js.configs.recommended,

  ...pluginVue.configs['flat/recommended'],

  {
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node,
        // Element Plus auto-imported globals (unplugin-vue-components)
        ElMessage: 'readonly',
        ElMessageBox: 'readonly',
        ElNotification: 'readonly',
        ElLoading: 'readonly',
        // axios instance with interceptors
        http: 'readonly',
      },
    },
    rules: {
      'no-unused-vars': ['warn', { argsIgnorePattern: '^_' }],
      'no-console': ['warn', { allow: ['warn', 'error'] }],
      'vue/multi-word-component-names': 'off',
      // v21.54 P2：所有 v-html 均经 DOMPurify 白名单消毒（sanitizedContent/noteHighlight 等），
      // 属有意使用的安全模式，关闭该规则的 warning（避免逐处 disable 噪音）
      'vue/no-v-html': 'off',
      'no-useless-escape': 'warn',
      'no-empty': ['warn', { allowEmptyCatch: true }],
    },
  },
])
