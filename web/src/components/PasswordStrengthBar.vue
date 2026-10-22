<template>
  <!-- v18.8: 密码强度引导（优-5）——只提示不强制，不影响提交 -->
  <div
    v-if="password"
    class="pwd-strength"
  >
    <div class="pwd-strength-bars">
      <span
        v-for="i in 3"
        :key="i"
        class="pwd-strength-bar"
        :class="{ active: i <= level, [`lv${level}`]: i <= level }"
      />
    </div>
    <span
      class="pwd-strength-text"
      :class="`lv${level}`"
    >{{ label }}</span>
    <span
      v-if="level < 3"
      class="pwd-strength-tip"
    >{{ tip }}</span>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  password: { type: String, default: '' }
})

// 评分：长度 + 字符多样性（纯前端引导，与后端校验无关）
const score = computed(() => {
  const p = props.password || ''
  if (!p) return 0
  let s = 0
  if (p.length >= 8) s++
  if (p.length >= 12) s++
  if (/[a-z]/.test(p) && /[A-Z]/.test(p)) s++
  if (/\d/.test(p)) s++
  if (/[^a-zA-Z0-9]/.test(p)) s++
  return s
})

const level = computed(() => {
  if (score.value <= 2) return 1
  if (score.value <= 3) return 2
  return 3
})

const label = computed(() => ['', '弱', '中', '强'][level.value])
const tip = computed(() => level.value === 1 ? '建议 8 位以上，混合大小写、数字和符号' : '再长一点或加入符号会更安全')
</script>

<style scoped>
.pwd-strength {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
  width: 100%;
}

.pwd-strength-bars {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}

.pwd-strength-bar {
  width: 32px;
  height: 4px;
  border-radius: var(--radius-xs);
  background: #e5e7eb;
  transition: background 0.3s;
}

.pwd-strength-bar.active.lv1 { background: #f56c6c; }
.pwd-strength-bar.active.lv2 { background: #e6a23c; }
.pwd-strength-bar.active.lv3 { background: #67c23a; }

.pwd-strength-text {
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}

.pwd-strength-text.lv1 { color: #f56c6c; }
.pwd-strength-text.lv2 { color: #e6a23c; }
.pwd-strength-text.lv3 { color: #67c23a; }

.pwd-strength-tip {
  font-size: 12px;
  color: #9ca3af;
}
</style>
