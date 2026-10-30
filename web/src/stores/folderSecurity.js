import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { message } from '@/utils/message'
import { useFolderStore } from './folder'

// v2.3.0 文件夹访问锁：全局密码弹框状态。
// 加密/解密/解锁三个入口共用同一个弹框实例（App.vue 挂载），避免 PC/移动端重复实现。
export const useFolderSecurityStore = defineStore('folderSecurity', () => {
  const visible = ref(false)
  const mode = ref('unlock') // encrypt | decrypt | unlock
  const folder = ref(null)
  const input = ref('')
  const confirm = ref('')
  const loading = ref(false)
  // 解锁成功后要执行的动作（如切入文件夹、移动笔记）
  const pending = ref(null)

  const title = computed(() => {
    if (mode.value === 'encrypt') return '加密文件夹'
    if (mode.value === 'decrypt') return '解密文件夹'
    return '解锁加密文件夹'
  })
  const placeholder = computed(() => {
    if (mode.value === 'decrypt') return '请输入原密码以解密'
    return '请输入文件夹密码'
  })
  const warningText = computed(() => {
    if (mode.value !== 'encrypt') return ''
    return '加密后该文件夹内的笔记将从列表中隐藏，查看需输入密码。忘记密码将无法在应用内解除加密，需管理员在设备上手工处理。请妥善保管密码。'
  })

  function reset() {
    input.value = ''
    confirm.value = ''
    loading.value = false
    pending.value = null
  }

  function openEncrypt(f) {
    reset()
    folder.value = f
    mode.value = 'encrypt'
    visible.value = true
  }

  function openDecrypt(f) {
    reset()
    folder.value = f
    mode.value = 'decrypt'
    visible.value = true
  }

  function openUnlock(f, onSuccess) {
    reset()
    folder.value = f
    mode.value = 'unlock'
    pending.value = onSuccess || null
    visible.value = true
  }

  function close() {
    visible.value = false
    reset()
  }

  async function submit() {
    if (!input.value) {
      message.warning('请输入密码')
      return
    }
    if (mode.value === 'encrypt' && input.value !== confirm.value) {
      message.warning('两次输入的密码不一致')
      return
    }
    const f = folder.value
    if (!f) return
    const store = useFolderStore()
    loading.value = true
    try {
      if (mode.value === 'encrypt') {
        await store.encryptFolderById(f.id, input.value, confirm.value)
        message.success('文件夹已加密')
        close()
      } else if (mode.value === 'decrypt') {
        await store.decryptFolderById(f.id, input.value)
        message.success('文件夹已解密')
        close()
      } else {
        await store.unlockFolderById(f.id, input.value)
        const cb = pending.value
        message.success('已解锁')
        close()
        if (cb) await cb()
      }
    } catch (e) {
      const status = e.response?.status
      if (status === 429) {
        message.error(e.response?.data?.error || '密码尝试次数过多，请稍后再试')
      } else if (status === 423) {
        message.error('文件夹已加密，请先解锁')
      } else {
        message.error(e.response?.data?.error || '操作失败')
      }
    } finally {
      loading.value = false
    }
  }

  return {
    visible, mode, folder, input, confirm, loading,
    title, placeholder, warningText,
    openEncrypt, openDecrypt, openUnlock, close, submit
  }
})
