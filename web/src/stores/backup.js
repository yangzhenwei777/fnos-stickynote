import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import * as backupApi from '../api/backup'
import { message } from '@/utils/message'

// 系统备份前端状态中枢（v1.0.167 切片 2）。
// 备份执行为异步：runBackup 拿 job_id 后轮询 getJob 直到终态，busy 期间禁用重复触发。
export const useBackupStore = defineStore('backup', () => {
  const targets = ref([])
  const snapshots = ref([])
  const jobs = ref([])
  const running = ref(false)
  const currentJob = ref(null)

  const enabledTargets = computed(() => targets.value.filter(t => t.enabled))

  const TERMINAL = new Set(['SUCCESS', 'FAILED', 'INTERRUPTED'])

  async function refresh() {
    await Promise.all([loadTargets(), loadSnapshots(), loadJobs(), loadStatus()])
  }

  async function loadTargets() {
    try {
      const { data } = await backupApi.listTargets()
      targets.value = data.targets || []
    } catch (e) {
      message.errorOnce(e.response?.data?.error || '备份目标加载失败')
    }
  }

  async function loadSnapshots() {
    try {
      const { data } = await backupApi.listSnapshots()
      snapshots.value = data.snapshots || []
    } catch (e) {
      // 不再静默：后端异常（如 500）要给出提示，否则用户只看到空白页无从判断
      message.errorOnce(e.response?.data?.error || '快照列表加载失败')
    }
  }

  async function loadJobs() {
    try {
      const { data } = await backupApi.listJobs()
      jobs.value = data.jobs || []
    } catch { /* 静默 */ }
  }

  // v2.2.1 清除执行记录（终态：成功/失败/已中断；运行中后端保留）。
  // 返回 {ok, deleted} 供 UI 提示；失败时提示并保持列表原状。
  async function clearJobs() {
    try {
      const { data } = await backupApi.clearJobs()
      await loadJobs()
      return { ok: true, deleted: data.deleted ?? 0 }
    } catch (e) {
      message.errorOnce(e.response?.data?.error || '清除执行记录失败')
      return { ok: false, deleted: 0 }
    }
  }

  async function loadStatus() {
    try {
      const { data } = await backupApi.getBackupStatus()
      running.value = !!data.running
    } catch { /* 静默 */ }
  }

  async function createTarget(payload) {
    const { data } = await backupApi.createTarget(payload)
    message.success('目标已创建')
    await loadTargets()
    return data.id
  }

  async function updateTarget(id, payload) {
    const { data } = await backupApi.updateTarget(id, payload)
    // 后端在保留份数调小时会立即轮转并把结果写进 message，透传给用户（需求6）。
    message.success(data?.message || '目标已更新')
    await Promise.all([loadTargets(), loadSnapshots()])
  }

  async function removeTarget(id) {
    await backupApi.deleteTarget(id)
    message.success('目标已删除（远端归档不受影响）')
    await loadTargets()
  }

  async function test(id) {
    const { data } = await backupApi.testTarget(id)
    return data
  }

  // runBackup：触发 + 轮询到终态。返回最终 job 或 null（忙/失败）。
  async function runBackup(id) {
    let resp
    try {
      resp = await backupApi.runTarget(id)
    } catch (e) {
      const code = e.response?.data?.code
      if (e.response?.status === 409 || code === 'BACKUP_ALREADY_RUNNING') {
        message.warning('已有备份/密钥操作在运行，请稍候')
        running.value = true
        return null
      }
      message.error(e.response?.data?.error || '触发备份失败')
      return null
    }
    running.value = true
    currentJob.value = { id: resp.data.job_id, backup_id: resp.data.backup_id, status: 'RUNNING', phase: '' }
    await pollJob(resp.data.job_id)
    await Promise.all([loadSnapshots(), loadJobs(), loadTargets()])
    return currentJob.value
  }

  function pollJob(jobId) {
    return new Promise((resolve) => {
      const tick = async () => {
        try {
          const { data } = await backupApi.getJob(jobId)
          currentJob.value = data
          if (TERMINAL.has(data.status)) {
            running.value = false
            if (data.status === 'SUCCESS') message.success('备份完成')
            else message.errorOnce(`备份失败：${data.error_message || data.error_code || data.status}`)
            resolve(data)
            return
          }
        } catch {
          running.value = false
          message.errorOnce('查询备份进度失败')
          resolve(null)
          return
        }
        setTimeout(tick, 1500)
      }
      tick()
    })
  }

  async function verify(backupId) {
    const { data } = await backupApi.verifySnapshot(backupId)
    return data
  }

  // deleteSnapshot：手动删除一份备份（所有目标远端 .snbak+.index.json + 本地快照行）。
  // 成功后刷新列表；返回 {ok, deleted_remote, warnings} 供 UI 提示部分失败。
  async function deleteSnapshot(backupId) {
    const { data } = await backupApi.deleteSnapshot(backupId)
    await loadSnapshots()
    return data
  }

  // discover：扫描远端目标，把历史归档登记进本地账本（清空/重装后接管历史备份）。
  async function discover() {
    const { data } = await backupApi.discoverSnapshots()
    await loadSnapshots()
    return data
  }

  async function download(backupId) {
    const resp = await backupApi.downloadArchive(backupId)
    const url = URL.createObjectURL(resp.data)
    const a = document.createElement('a')
    a.href = url
    a.download = `${backupId}.snbak`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  async function exportKey(password) {
    const resp = await backupApi.exportKey(password)
    const text = await resp.data.text()
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = 'backup_master_key.txt'
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  // ---- 在线全量恢复（切片 3）----

  // restore 触发恢复。破坏性窗口内后端对其余请求返回 503，故用 waitForRestoreDone 以
  // /health 判完成（恢复后新库/新会话生效，前端届时整页需重新登录）。
  async function restore(payload) {
    const { data } = await backupApi.restoreBackup(payload)
    return data
  }

  // waitForRestoreDone：轮询 /health。恢复开始→503(restoring)，完成→200(healthy)。
  // 返回 true=已恢复完成，false=超时。给恢复一点起效缓冲后再开始判定。
  async function waitForRestoreDone(timeoutMs = 120000) {
    const start = Date.now()
    await new Promise(r => setTimeout(r, 1200)) // 等窗口真正进入 restoring
    while (Date.now() - start < timeoutMs) {
      try {
        const res = await fetch('/health', { cache: 'no-store' })
        const j = await res.json().catch(() => ({}))
        if (res.status === 200 && j.status === 'healthy') {
          await refresh()
          return true
        }
      } catch { /* 网络抖动/窗口内不可达，继续等 */ }
      await new Promise(r => setTimeout(r, 1500))
    }
    return false
  }

  return {
    targets, snapshots, jobs, running, currentJob, enabledTargets,
    refresh, loadTargets, loadSnapshots, loadJobs, loadStatus, clearJobs,
    createTarget, updateTarget, removeTarget, test, runBackup, verify, deleteSnapshot, download, exportKey,
    restore, waitForRestoreDone, discover
  }
})
