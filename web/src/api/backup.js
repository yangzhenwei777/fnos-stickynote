import api from './request'

// 系统备份管理 API 封装（v1.0.167 切片 2）。后端路径均为 /api/backup/...（admin 组）。
// 明文 WebDAV 密码只在调用点内存中短暂存在，随请求体发出；响应体永不含密码/密文/CA。

export function getBackupStatus() {
  return api.get('/backup/status')
}

export function listTargets() {
  return api.get('/backup/targets')
}

export function createTarget(data) {
  return api.post('/backup/targets', data)
}

export function updateTarget(id, data) {
  return api.put(`/backup/targets/${id}`, data)
}

export function deleteTarget(id) {
  return api.delete(`/backup/targets/${id}`)
}

// 测试连接：返回 {ok, supports_move, max_object_size} 或 {ok:false, error}
export function testTarget(id) {
  return api.post(`/backup/targets/${id}/test`)
}

// 触发一次备份：202 + {job_id, backup_id}；忙时 409
export function runTarget(id) {
  return api.post(`/backup/targets/${id}/run`)
}

export function getJob(id) {
  return api.get(`/backup/jobs/${id}`)
}

export function listJobs() {
  return api.get('/backup/jobs')
}

// v2.2.1 清除执行记录：只清已终结记录（成功/失败/已中断），运行中保留。
// 返回 {message, deleted}；deleted 为实际清除条数。
export function clearJobs() {
  return api.delete('/backup/jobs')
}

export function listSnapshots() {
  return api.get('/backup/snapshots')
}

// 扫描远端目标目录，把历史归档登记进本地账本（清空/重装后接管历史备份用）
export function discoverSnapshots() {
  return api.post('/backup/discover')
}

// 远端存档完整性校验：{ok} 或 {ok:false, error}
export function verifySnapshot(backupId) {
  return api.post(`/backup/snapshots/${backupId}/verify`)
}

// 手动删除备份：删所有目标上的远端 .snbak+.index.json + 本地快照行。
// 返回 {ok, deleted_remote, warnings}；运行中 409。
export function deleteSnapshot(backupId) {
  return api.delete(`/backup/snapshots/${backupId}`)
}

// 下载归档（.snbak 二进制，走 blob）
export function downloadArchive(backupId) {
  return api.get(`/backup/download/${backupId}`, { responseType: 'blob' })
}

// 主密钥导出：返回文本 blob（需当前口令二次校验）
export function exportKey(password) {
  return api.post('/backup/key/export', { password }, { responseType: 'blob' })
}

export function importKey(content, password) {
  return api.post('/backup/key/import', { content, password })
}

export function regenerateKey(password, confirm) {
  return api.post('/backup/key/regenerate', { password, confirm })
}

// 在线全量恢复（切片 3）：异步 job + 202。破坏性窗口内其它请求会 503，
// 前端以 /health 判恢复完成后再刷新。restoreProgress 读内存态（窗口外可用）。
export function restoreBackup(payload) {
  return api.post('/backup/restore', payload)
}

export function restoreProgress() {
  return api.get('/backup/restore/progress')
}
