// v2.3.0 文件夹访问锁：Web 端本会话解锁令牌（sessionStorage）。
//
// 为什么是 sessionStorage：刷新/切页保持解锁，关闭浏览器标签页即清空，
// 满足「解锁保持到主动退出账户或关闭浏览器」的需求；不写 localStorage。
// 桌面端不用本模块作为权威（Go 进程内存说了算），见 stores/folder.js 的桌面分支。
const STORAGE_KEY = 'sn_folder_grants'
const MAX_GRANTS = 50

function readRaw() {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (!raw) return {}
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    return {}
  }
}

function writeRaw(map) {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(map))
  } catch {
    // 存储不可用（隐私模式/配额）时静默降级：本次会话内仍可用内存态
  }
}

export function saveGrant(folderId, token) {
  if (!folderId || !token) return
  const map = readRaw()
  map[String(folderId)] = { token: String(token), at: Date.now() }
  const keys = Object.keys(map)
  if (keys.length > MAX_GRANTS) {
    keys.sort((a, b) => (map[a].at || 0) - (map[b].at || 0))
    for (const k of keys.slice(0, keys.length - MAX_GRANTS)) delete map[k]
  }
  writeRaw(map)
}

export function removeGrant(folderId) {
  const map = readRaw()
  delete map[String(folderId)]
  writeRaw(map)
}

export function clearGrants() {
  try {
    sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // ignore
  }
}

export function grantedFolderIds() {
  return Object.keys(readRaw()).map(Number).filter(n => Number.isFinite(n) && n > 0)
}

export function grantHeader() {
  const map = readRaw()
  const tokens = Object.values(map).map(v => v?.token).filter(Boolean)
  return tokens.length ? tokens.join(',') : ''
}
