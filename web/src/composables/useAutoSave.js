import { ref } from 'vue'
import { message } from '@/utils/message'
import { updateNote } from '@/api/note'
import { get as idbGet, set as idbSet, del as idbDel } from 'idb-keyval'
import { useAuthStore } from '@/stores/auth'

const AUTO_SAVE_DELAY = 800
const DRAFT_DELAY = 800

/**
 * useAutoSave — 自动保存 + IndexedDB 草稿 + 离开拦截（从 NotesView.vue 拆分，P2-1）
 * @param {import('vue').Ref<object|null>} currentNote
 * @param {import('vue').Ref<string[]>} currentTagInput
 * @param {import('vue').Ref<boolean>} isDirty
 * @param {Function} applySavedToLists - 保存后就地更新列表
 * @param {Function} [getEncryptPassword] - 返回当前加密笔记密码的回调（v21.46）：
 *   markDirty/Ctrl+S/切换前保存均走本组件内部 saveNote，外层包装函数传参不可达，
 *   必须由回调在保存时注入密码，否则加密笔记编辑保存被后端 400 拒绝
 * @param {Function} [ensureFreshContent] - 返回编辑器最新 HTML 的回调（v21.85）：
 *   编辑器内容改为防抖回流后，构造保存 payload 前必须强制同步，
 *   否则防抖窗口内的最后一段编辑不会进入本次保存
 */
export function useAutoSave(currentNote, currentTagInput, isDirty, applySavedToLists, getEncryptPassword, ensureFreshContent) {
  const saving = ref(false)
  const saveStatus = ref('')
  // v21.106 P1-1：保存失败状态显式化——409 冲突 / 404 远端已删 是两个「重试也无法成功」的
  // 确定性失败，须让调用方（NotesView）据此弹处理对话框、暂停自动重试，而非无限静默失败。
  const saveConflict = ref(false)    // true = 上次保存撞 409（其他设备已改），等待用户处理
  const remoteDeleted = ref(false)   // true = 笔记已被远端删除（回收站），保存已无意义
  let noticeShown = false            // 同类失败 toast 去重（自动保存反复失败不连环弹），成功后复位
  let autoSaveTimer = null
  let draftTimer = null
  let savedHideTimer = null
  let saveSeq = 0 // v21.68 P2-07：保存序号，防并发保存乱序（旧响应覆盖新状态）

  const authStore = useAuthStore()

  function draftKey(noteId) {
    const uid = authStore.user?.id || 'anon'
    return `draft_${uid}_${noteId}`
  }

  async function saveDraft() {
    if (!currentNote.value || !isDirty.value) return
    // v21.58 F-11：加密笔记不持久化明文草稿——解锁后 currentNote.content 为明文，
    // 存入 IndexedDB 会泄露隐私（公共/共享电脑、浏览器 profile 被复制等场景）
    if (currentNote.value.is_encrypted) return
    try {
      await idbSet(draftKey(currentNote.value.id), {
        title: currentNote.value.title,
        content: currentNote.value.content,
        tags: currentTagInput.value.join(', '),
        saved_at: Date.now()
      })
    } catch { /* IndexedDB 不可用时静默降级 */ }
  }

  async function clearDraft(noteId) {
    try { await idbDel(draftKey(noteId)) } catch { /* ignore */ }
  }

  async function checkDraft(note) {
    // v21.58 F-11：加密笔记不恢复明文草稿（新版本已不再为其持久化草稿）
    if (note.is_encrypted) return
    try {
      const draft = await idbGet(draftKey(note.id))
      if (!draft) return
      const serverTime = new Date(note.updated_at).getTime()
      if (draft.saved_at <= serverTime) {
        await clearDraft(note.id)
        return
      }
      // v21.66 P1-2：等待弹窗完成（await），恢复前校验当前笔记仍是 note，
      // 防止用户在弹窗未确认时切换到别的笔记、草稿误覆盖新笔记。
      try {
        await ElMessageBox.confirm(
          `检测到 ${new Date(draft.saved_at).toLocaleString()} 有一份未保存的本地草稿，比服务器版本新。是否恢复？`,
          '发现未保存草稿',
          { confirmButtonText: '恢复草稿', cancelButtonText: '丢弃草稿', type: 'warning' }
        )
        if (currentNote.value?.id !== note.id) return // 已切换到其他笔记，放弃恢复
        currentNote.value = { ...currentNote.value, title: draft.title, content: draft.content }
        currentTagInput.value = draft.tags ? draft.tags.split(',').map(t => t.trim()).filter(Boolean) : []
        markDirty()
        message.success('草稿已恢复')
      } catch {
        await clearDraft(note.id)
      }
    } catch { /* ignore */ }
  }

  function markDirty() {
    if (!isDirty.value) isDirty.value = true
    saveStatus.value = 'editing'
    clearTimeout(savedHideTimer)
    clearTimeout(autoSaveTimer)
    // v21.106 P1-1：409 冲突 / 404 远端已删时暂停自动重试（重试必再失败、只会刷角标），
    // 等用户处理冲突或切换笔记后由 clearSaveState 恢复调度；草稿保存不受影响照常进行。
    if (!saveConflict.value && !remoteDeleted.value) {
      autoSaveTimer = setTimeout(() => saveNote(false), AUTO_SAVE_DELAY)
    }
    clearTimeout(draftTimer)
    draftTimer = setTimeout(saveDraft, DRAFT_DELAY)
  }

  async function saveNote(showToast = true, encryptPassword = '') {
    if (!currentNote.value) return
    // v21.85：保存前强制同步编辑器防抖窗口内的最新内容（预览态无编辑器时回退现值）
    if (typeof ensureFreshContent === 'function') {
      const fresh = ensureFreshContent()
      if (typeof fresh === 'string' && fresh) currentNote.value.content = fresh
    }
    // v21.150 E7：捕获本次保存对象与标签——响应归来时用户可能已切换笔记，
    // 后续补丁/复位必须锚定「被保存的笔记」，不得再读响应时刻的 currentNote
    const savedNote = currentNote.value
    const savedTags = currentTagInput.value.join(', ')
    const seq = ++saveSeq // v21.68 P2-07：本次保存的序号
    saving.value = true
    saveStatus.value = 'saving'
    try {
      // v21.72 P2-08：携带乐观锁版本号，后端 WHERE version 匹配实现 CAS
      const payload = { title: savedNote.title, content: savedNote.content, tags: savedTags, version: savedNote.version || 0 }
      // v21.46：密码取显式参数或回调（自动保存/Ctrl+S 走内部函数，回调兜底注入）
      const pwd = encryptPassword || (typeof getEncryptPassword === 'function' ? getEncryptPassword() : '')
      if (savedNote.is_encrypted && pwd) {
        payload.password = pwd
      }
      const resp = await updateNote(savedNote.id, payload)
      // 同步后端递增后的 version，避免下次保存误报冲突（axios 拦截器返回完整 response，数据在 resp.data）
      if (resp && resp.data && typeof resp.data.version === 'number') {
        savedNote.version = resp.data.version
        if (currentNote.value?.id === savedNote.id) currentNote.value.version = resp.data.version
      }
      // 仅当本次仍是最新一次保存时才清 dirty，防止旧响应覆盖新状态
      if (seq !== saveSeq) return
      // v21.150 E7：dirty/定时器复位只作用于仍在编辑的同一篇笔记（迟到响应不得清掉新笔记的自动保存调度）
      if (currentNote.value?.id === savedNote.id) {
        isDirty.value = false
        clearTimeout(autoSaveTimer)
        clearTimeout(draftTimer)
      }
      await clearDraft(savedNote.id)
      // v21.150 E7：切换窗口内（clearDraft 让渡点）不得把复位打到新笔记上
      if (currentNote.value?.id === savedNote.id) {
        // v21.106 P1-1：保存成功即代表冲突/删除状态已不成立（或由后端拒绝前的重试成功），全部复位
        saveConflict.value = false
        remoteDeleted.value = false
        noticeShown = false
      }
      saveStatus.value = 'saved'
      clearTimeout(savedHideTimer)
      savedHideTimer = setTimeout(() => {
        if (saveStatus.value === 'saved') saveStatus.value = ''
      }, 2000)
      if (showToast) message.success('保存成功')
      // v21.84/v21.150：把新 version 与服务端 updated_at（排序键唯一时间源）同步进列表；
      // 补丁目标固定为 savedNote——根治「快速切换后未编辑项被置顶」
      applySavedToLists(savedNote.id, savedNote.title, savedNote.content, savedTags, savedNote.version, resp?.data?.updated_at)
    } catch (e) {
      if (seq === saveSeq && currentNote.value?.id === savedNote.id) {
        saveStatus.value = 'error'
        // v21.106 P1-1：失败分类反馈（此前 409 仅 Ctrl+S 才提示、自动保存静默 ✗，
        // 用户以为已保存；404/网络错误同样被吞成「保存失败」无出路）。
        const status = e?.response?.status
        if (status === 404) {
          remoteDeleted.value = true
          if (!noticeShown) {
            noticeShown = true
            message.error('笔记已被删除（可在回收站恢复），未保存内容已保留本地草稿')
          }
        } else if (status === 409) {
          // 409 弹处理对话框由 NotesView 监听 saveConflict 承担（每轮冲突只弹一次）
          saveConflict.value = true
        } else {
          if (!noticeShown) {
            noticeShown = true
            message.error(e?.response?.data?.error || '保存失败（网络或服务器异常），内容已保留本地草稿，继续输入将自动重试')
          }
        }
      }
    } finally {
      if (seq === saveSeq) saving.value = false
    }
  }

  async function saveBeforeSwitch(prevNote) {
    if (!isDirty.value || !prevNote) return true
    // v21.85：切换前保存同样强制同步最新内容（现流程 prevNote 即 currentNote；
    // 调用方 NotesView.selectNote 入口已先 flush，此处为兜底）
    if (typeof ensureFreshContent === 'function' && prevNote === currentNote.value) {
      const fresh = ensureFreshContent()
      if (typeof fresh === 'string' && fresh) prevNote.content = fresh
    }
    clearTimeout(autoSaveTimer)
    try {
      const tags = currentTagInput.value.join(', ')
      // v21.72 P2-08：切换前保存同样携带乐观锁版本号
      const payload = { title: prevNote.title, content: prevNote.content, tags, version: prevNote.version || 0 }
      // v21.46：切换前保存同样注入加密密码（此前加密笔记切换时保存被静默吞掉）
      const pwd = typeof getEncryptPassword === 'function' ? getEncryptPassword() : ''
      if (prevNote.is_encrypted && pwd) {
        payload.password = pwd
      }
      const resp = await updateNote(prevNote.id, payload)
      // v21.75：同步 version 取 resp.data（axios 拦截器返回完整 response）
      if (resp && resp.data && typeof resp.data.version === 'number') {
        prevNote.version = resp.data.version
      }
      await clearDraft(prevNote.id)
      // v21.84：切换前保存同样把结果同步进列表（含新 version），防止列表缓存过期版本导致后续 409
      // v21.150 E7：排序键带服务端 updated_at（单一时间源）
      applySavedToLists(prevNote.id, prevNote.title, prevNote.content, tags, prevNote.version, resp?.data?.updated_at)
      saveStatus.value = 'saved'
      clearTimeout(savedHideTimer)
      savedHideTimer = setTimeout(() => {
        if (saveStatus.value === 'saved') saveStatus.value = ''
      }, 2000)
      return true
    } catch (e) {
      // v21.58 F-01：切换前保存失败不得静默吞掉——默认阻止切换防止静默丢数据。
      // v21.106 P1-1：但「阻止」不能变成死路——分类给出明确原因与放弃逃生口：
      //   404（远端已删）：保存永无可能成功 → 直接放行切换（草稿保留）；
      //   409/网络等：弹确认框由用户决定「放弃并继续」或「留在当前笔记重试」。
      saveStatus.value = 'error'
      const status = e?.response?.status
      if (status === 404) {
        remoteDeleted.value = true
        isDirty.value = false
        message.error('笔记已被删除（可在回收站恢复），未保存内容已保留本地草稿')
        return true
      }
      if (status === 409) saveConflict.value = true
      const reason = status === 409 ? '该笔记已在其他设备被修改' : '网络或服务器异常'
      const hint = (prevNote.is_encrypted || e?.response?.data?.note_encrypted)
        ? '加密笔记没有本地草稿，放弃将永久丢失本次未保存内容。仍要放弃并继续吗？'
        : '未保存内容会保留在本地草稿中，下次打开该笔记可恢复。要放弃并继续吗？'
      try {
        await ElMessageBox.confirm(
          `${reason}，保存失败。${hint}`,
          '切换前保存失败',
          { confirmButtonText: '放弃并继续', cancelButtonText: '留在当前笔记重试', type: 'warning' }
        )
        isDirty.value = false
        saveConflict.value = false
        return true
      } catch {
        return false
      }
    }
  }

  // v21.106 P1-1：切换/重新加载到另一篇笔记后由调用方清除确定性失败状态，恢复自动保存调度
  function clearSaveState() {
    saveConflict.value = false
    remoteDeleted.value = false
    noticeShown = false
  }

  function handleKeydown(e) {
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault()
      saveNote(true)
    }
  }

  function handleBeforeUnload(e) {
    if (isDirty.value) {
      e.preventDefault()
      e.returnValue = ''
    }
  }

  function cleanupTimers() {
    clearTimeout(autoSaveTimer)
    clearTimeout(draftTimer)
    clearTimeout(savedHideTimer)
  }

  return {
    saving, saveStatus,
    saveConflict, remoteDeleted,
    markDirty, saveNote, saveBeforeSwitch, checkDraft, clearDraft, clearSaveState,
    handleKeydown, handleBeforeUnload, cleanupTimers
  }
}
