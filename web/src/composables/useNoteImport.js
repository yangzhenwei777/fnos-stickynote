import { ref } from 'vue'
import { message } from '@/utils/message'
import api from '@/api/request'

/**
 * isImportedNote — 导入接口返回体形状校验（纵深防御）。
 *
 * 为什么必须有：导入链路曾出现「提示导入成功但笔记标题为空、关闭后消失」——
 * 根因是桌面 LocalTransport 未接线该端点、且未接线路由当时返回的是**成功形状的 501**，
 * 调用点无条件 message.success 并把错误对象当笔记 unshift 进列表（未落库，重拉即消失）。
 * transport 侧已改为 reject（止血），这里再挡一道：**返回体没有有效 id 就不是一篇笔记**，
 * 绝不把它塞进列表、也绝不弹成功。
 */
export function isImportedNote (data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) return false
  const id = Number(data.id)
  return Number.isFinite(id) && id > 0
}

/**
 * importNoteFile — 导入的**共用底层**：表单上传 + 返回体校验 + 统一提示。
 *
 * v21.166 计划项 4（双视图去重）：此前网格页 NotesListView.vue **手写了一份
 * 与本文件逐行重复的导入逻辑**（FormData + /notes/import + isImportedNote 校验 +
 * 错误提示 + finally 清 input）。两处同源逻辑分叉，正是「一处修了另一处没修」
 * 的典型温床（205 选中态修复需同时改两处 CSS 即是前车之鉴）。
 *
 * 抽此底层后：两视图共用「上传+校验+提示」，各自只保留**交互差异**
 * （阅读页=写编辑态 currentNote；网格页=跳转到新笔记），语义不再分叉。
 *
 * ⭐ v21.213（211b 实机：导入的笔记一律落到「我的笔记」）：新增 folderId 参数。
 * 导入应当落在**用户发起导入时所在的文件夹**，而不是恒定的「我的笔记」。
 * 通过 FormData 的 `folder_id` 字段传给 transport（Web 端 handler 忽略、桌面端消费）。
 * 省略该参数 ⇒ 不传 ⇒ 两端都保持旧行为（「我的笔记」），**向后兼容**。
 *
 * 返回：导入成功的笔记对象；失败返回 null（提示已由本函数发出）。
 */
export async function importNoteFile (file, folderId) {
  if (!file) return null
  const formData = new FormData()
  formData.append('file', file)
  // folder_id 走独立表单字段（multipart body 放不了 JSON 字段；见 transport.folderOfForm）。
  if (folderId !== undefined && folderId !== null && folderId !== '') {
    formData.append('folder_id', String(folderId))
  }
  try {
    const { data } = await api.post('/notes/import', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
    if (!isImportedNote(data)) {
      message.error('导入失败：未返回有效的笔记数据')
      return null
    }
    message.success('导入成功')
    return data
  } catch (e) {
    message.error(e.response?.data?.error || '导入失败')
    return null
  }
}

/**
 * useNoteImport — Markdown 文件导入（阅读页 NotesView.vue 专用包装）
 *
 * ⭐ v21.213：新增 `currentFolderId` 形参 —— 阅读页把它接到自己的当前文件夹，
 * 使导入落在用户所在文件夹。省略时行为不变。
 */
export function useNoteImport() {
  const fileInput = ref(null)

  async function handleImportFile(e, notes, currentNote, currentTagInput, currentFolderId) {
    const file = e.target.files?.[0]
    if (!file) return
    try {
      const data = await importNoteFile(file, currentFolderId)
      if (!data) return null
      notes.value.unshift(data)
      currentNote.value = { ...data }
      currentTagInput.value = []
      return data
    } finally {
      if (fileInput.value) fileInput.value.value = ''
    }
  }

  function triggerFileInput() {
    if (fileInput.value) fileInput.value.click()
  }

  return { fileInput, handleImportFile, triggerFileInput }
}
