import api from './request'

export function createNoteShare(id, expiresInDays) {
  return api.post(`/notes/${id}/share`, { expires_in_days: expiresInDays })
}

export function listNoteShares(id) {
  return api.get(`/notes/${id}/shares`)
}

export function revokeNoteShare(id, shareId) {
  return api.delete(`/notes/${id}/shares/${shareId}`)
}

export function getSharedNote(token) {
  return api.get(`/share/${token}`)
}
