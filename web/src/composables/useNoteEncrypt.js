import { ref, computed } from 'vue'
import { message } from '@/utils/message'
import { encryptNote, decryptNote, verifyEncrypt, getNote } from '@/api/note'
import api from '@/api/request'

/**
 * useNoteEncrypt — 笔记加解密/密码验证/下载（从 NotesView.vue 拆分，P2-1）
 * @param {import('vue').Ref<object|null>} currentNote
 * @param {Function} syncNoteInList - 同步列表缓存的回调
 */
export function useNoteEncrypt(currentNote, syncNoteInList) {
  const encryptUnlocked = ref(false)
  const encryptPassword = ref('')
  const pwdDialogVisible = ref(false)
  const pwdDialogMode = ref('view')
  const pwdInput = ref('')
  const pwdConfirm = ref('')
  const pwdLoading = ref(false)

  const pwdDialogTitle = computed(() => {
    const titles = { view: '查看加密笔记', edit: '编辑加密笔记', decrypt: '解密笔记', download: '下载加密笔记', encrypt: '设置加密密码' }
    return titles[pwdDialogMode.value] || ''
  })
  const pwdPlaceholder = computed(() => pwdDialogMode.value === 'decrypt' ? '请输入原密码以解密' : '请输入笔记密码')

  function showEncryptDialog() {
    pwdDialogMode.value = 'encrypt'
    pwdInput.value = ''
    pwdConfirm.value = ''
    pwdDialogVisible.value = true
  }

  function showPasswordDialog(mode) {
    pwdDialogMode.value = mode
    pwdInput.value = ''
    pwdConfirm.value = ''
    pwdDialogVisible.value = true
  }

  async function handlePwdSubmit(onEditEnter) {
    if (!pwdInput.value) { message.warning('请输入密码'); return }
    const mode = pwdDialogMode.value

    if (mode === 'encrypt') {
      if (pwdInput.value !== pwdConfirm.value) { message.warning('两次输入的密码不一致'); return }
      pwdLoading.value = true
      try {
        // P2 批次1（§60）：加密推进服务端 version——同步响应中的新版本，
        // 避免下次保存用旧基点误报 409
        const resp = await encryptNote(currentNote.value.id, pwdInput.value, pwdConfirm.value)
        message.success('加密成功')
        pwdDialogVisible.value = false
        currentNote.value.is_encrypted = true
        currentNote.value.content = ''
        if (Number.isFinite(resp?.data?.version)) currentNote.value.version = resp.data.version
        encryptUnlocked.value = false
        syncNoteInList(currentNote.value.id, { is_encrypted: true, content: '' })
      } catch (e) { message.error(e.response?.data?.error || '加密失败') }
      finally { pwdLoading.value = false }
      return
    }

    pwdLoading.value = true
    try {
      const { data } = await verifyEncrypt(currentNote.value.id, pwdInput.value)

      if (mode === 'view') {
        currentNote.value = data
        encryptUnlocked.value = true
        encryptPassword.value = pwdInput.value
        pwdDialogVisible.value = false
      } else if (mode === 'download') {
        pwdDialogVisible.value = false
        downloadNoteFile(pwdInput.value)
      } else if (mode === 'edit') {
        currentNote.value = data
        encryptUnlocked.value = true
        encryptPassword.value = pwdInput.value
        pwdDialogVisible.value = false
        if (onEditEnter) onEditEnter()
      } else if (mode === 'decrypt') {
        await decryptNote(currentNote.value.id, pwdInput.value)
        message.success('解密成功')
        pwdDialogVisible.value = false
        currentNote.value.is_encrypted = false
        currentNote.value.content = ''
        encryptUnlocked.value = false
        encryptPassword.value = ''
        const { data: fresh } = await getNote(currentNote.value.id)
        currentNote.value = fresh
        // v21.84：解密后把重新拉取的 version 一并同步进列表（SetEncrypted/ClearEncrypted 不递增 version，
        // 此处重在让列表模型与服务器保持一致）
        syncNoteInList(fresh.id, { is_encrypted: false, content: fresh.content, version: fresh.version })
      }
    } catch (e) {
      if (e.response?.status === 429) {
        message.error('密码尝试次数过多，请10分钟后再试')
      } else {
        message.error('密码错误')
      }
    }
    finally { pwdLoading.value = false }
  }

  function downloadNote() {
    if (currentNote.value?.is_encrypted) {
      if (encryptPassword.value) {
        downloadNoteFile(encryptPassword.value)
      } else {
        showPasswordDialog('download')
      }
    } else {
      downloadNoteFile()
    }
  }

  async function downloadNoteFile(password) {
    const id = currentNote.value.id
    if (password) {
      try {
        const resp = await api.post('/notes/' + id + '/export', { password }, { responseType: 'blob' })
        const url = URL.createObjectURL(resp.data)
        const a = document.createElement('a')
        a.href = url
        a.download = (currentNote.value.title || '未命名') + '.md'
        a.click()
        URL.revokeObjectURL(url)
      } catch (e) {
        message.error(e.response?.data?.error || '导出失败')
      }
    } else {
      window.open(api.defaults.baseURL + '/notes/' + id + '/export', '_blank')
    }
  }

  function resetOnNoteChange() {
    if (currentNote.value?.is_encrypted) {
      encryptUnlocked.value = false
      encryptPassword.value = ''
      currentNote.value.content = ''
    }
  }

  return {
    encryptUnlocked, encryptPassword,
    pwdDialogVisible, pwdDialogMode, pwdDialogTitle, pwdPlaceholder,
    pwdInput, pwdConfirm, pwdLoading,
    showEncryptDialog, showPasswordDialog, handlePwdSubmit,
    downloadNote, resetOnNoteChange
  }
}
