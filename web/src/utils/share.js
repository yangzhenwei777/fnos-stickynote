export function shareLink(token, origin = window.location.origin) {
  return `${origin}/share?t=${encodeURIComponent(token)}`
}

export function shareStatus(share, now = Date.now()) {
  if (share?.revoked_at) return 'revoked'
  const expires = share?.expires_at ? new Date(share.expires_at).getTime() : 0
  if (expires && expires <= now) return 'expired'
  return 'active'
}

export function shareStatusLabel(status) {
  if (status === 'revoked') return '已撤销'
  if (status === 'expired') return '已过期'
  return '有效'
}
