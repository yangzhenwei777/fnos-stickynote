import api from './request'

export function getNoteHistory(id, params) {
  return api.get(`/notes/${id}/history`, { params })
}

export function getNoteHistoryDetail(id, historyId) {
  return api.get(`/notes/${id}/history/${historyId}`)
}

export function restoreNoteHistory(id, historyId, version) {
  return api.post(`/notes/${id}/history/${historyId}/restore`, { version })
}
