import api from './request'

export function getNotes(params) {
  return api.get('/notes', { params })
}

export function getNote(id) {
  return api.get(`/notes/${id}`)
}

export function createNote(data) {
  return api.post('/notes', data)
}

export function updateNote(id, data) {
  return api.put(`/notes/${id}`, data)
}

export function deleteNote(id) {
  return api.delete(`/notes/${id}`)
}

export function getTags() {
  return api.get('/notes/tags')
}

// 回收站（v18.7 起分页：page/page_size，返回 {notes, total}）
export function getTrashNotes(params) {
  return api.get('/trash/notes', { params })
}

// 回收站可视化（v18.8）：返回 {count, upload_bytes}
export function getTrashStats() {
  return api.get('/trash/stats')
}

export function restoreNote(id) {
  return api.post(`/notes/${id}/restore`)
}

export function permanentDeleteNote(id) {
  return api.delete(`/notes/${id}/permanent`)
}

export function emptyTrash() {
  return api.delete('/trash/notes')
}

// 导出（v19.4 起仅 Markdown）：单篇 / 全部打包 zip
export function exportNote(id) {
  return api.get(`/notes/${id}/export`, { responseType: 'blob' })
}

export function exportAllNotes() {
  return api.get('/export/notes', { responseType: 'blob', headers: { Accept: 'application/zip' } })
}

// 配额（v19.1）：返回 {notes_count, notes_limit, storage_bytes, storage_limit_mb}
export function getQuota() {
  return api.get('/quota')
}

export function uploadImage(file) {
  const formData = new FormData()
  formData.append('file', file)
  return api.post('/upload', formData, {
    headers: { 'Content-Type': 'multipart/form-data' }
  })
}

// 笔记加密（v20.0）
export function encryptNote(id, password, confirmPassword) {
  return api.put(`/notes/${id}/encrypt`, { password, confirm_password: confirmPassword })
}

export function decryptNote(id, password) {
  return api.delete(`/notes/${id}/encrypt`, { data: { password } })
}

export function verifyEncrypt(id, password) {
  return api.post(`/notes/${id}/verify-encrypt`, { password })
}

// 批量操作（v20.7）
export function batchDeleteNotes(ids) {
  return api.post('/notes/batch-delete', { ids })
}

export function batchExportNotes(ids) {
  return api.post('/notes/batch-export', { ids }, { responseType: 'blob' })
}

// v21.87 移动到/复制到文件夹（folderId: null=默认"我的笔记"，数字=自定义文件夹 id）
export function moveNoteToFolder(id, folderId) {
  return api.put(`/notes/${id}/folder`, { folder_id: folderId })
}

export function copyNoteToFolder(id, folderId) {
  return api.post(`/notes/${id}/copy`, { folder_id: folderId })
}
