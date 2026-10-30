import api from './request'

// v21.86 文件夹 API：后端"我的笔记"为虚拟默认项（id=null），自定义文件夹存 note_folders 表
export function getFolders() {
  return api.get('/folders')
}

export function createFolder(name) {
  return api.post('/folders', { name })
}

// position: up / down / top（重命名与移动可单独或同时调用）
export function updateFolder(id, payload) {
  return api.put(`/folders/${id}`, payload)
}

export function renameFolder(id, name) {
  return updateFolder(id, { name })
}

export function moveFolder(id, position) {
  return updateFolder(id, { position })
}

export function deleteFolder(id) {
  return api.delete(`/folders/${id}`)
}

// v2.3.0 文件夹访问锁
export function encryptFolder(id, password, confirmPassword) {
  return api.put(`/folders/${id}/encrypt`, { password, confirm_password: confirmPassword })
}

export function decryptFolder(id, password) {
  return api.delete(`/folders/${id}/encrypt`, { data: { password } })
}

export function unlockFolder(id, password) {
  return api.post(`/folders/${id}/unlock`, { password })
}
