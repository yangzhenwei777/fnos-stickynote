// desktopApi.js —— 桌面专属绑定调用收口（P6 便携备份；后续 P4 数据中心复用同一入口）。
// 只在 VITE_DESKTOP=1 构建且 Wails 桥存在时可用；错误映射集中在此（Go 哨兵文案 → 人话）。
import { IS_DESKTOP } from './transport'

function bridge () {
  const s = window.go && window.go.main && window.go.main.LocalService
  return s || null
}

export function desktopBridgeAvailable () {
  return IS_DESKTOP && bridge() !== null
}

// formatPortableError 把 Go 侧哨兵错误映射为可读文案（其余原样透传，禁静默吞错）。
// 哨兵源：backup/errors.go（BACKUP_*）+ desktop/portable（英文语义串）。
export function formatPortableError (err) {
  const m = String((err && err.message) || err || '')
  if (m.includes('password too short')) return '备份口令至少 6 位'
  if (m.includes('BACKUP_DECRYPT_FAILED')) return '口令错误或备份文件已损坏'
  if (m.includes('attachments missing')) return '本地附件缺失，已中止导出（不会产出不完整备份）'
  if (m.includes('BACKUP_TOO_LARGE') || m.includes('> cap')) return '数据超过 256MB 便携备份上限，请精简附件后重试'
  if (m.includes('not a PORTABLE')) return '所选文件不是 Sticky note 便携备份（.snbak）'
  if (m.includes('do not match manifest') || m.includes('undeclared member')) return '备份包内容校验失败（可能被篡改或不完整），已拒绝导入'
  if (m.includes('no active generation')) return '本地数据尚未初始化，无法导出'
  if (m.includes('DESKTOP_BRIDGE_UNAVAILABLE')) return '桌面桥不可用'
  if (m.includes('NOT_LOGGED_IN')) return '当前未登录，请先登录'
  return m || '操作失败'
}

async function callBridge (fn, ...args) {
  const svc = bridge()
  if (!svc || typeof svc[fn] !== 'function') {
    throw new Error('DESKTOP_BRIDGE_UNAVAILABLE')
  }
  return svc[fn](...args)
}

async function call (fn, password) {
  return callBridge(fn, password || '')
}

// portableExport 触发原生保存对话框 + 导出；返回 {cancelled}|{path,backup_id,...}。
export const portableExport = (password) => call('PortableExport', password)

// portableImport 触发原生打开对话框 + 导入；返回 {cancelled}|{generation,rebind_required,...}。
export const portableImport = (password) => call('PortableImport', password)

// ---- 冲突中心（批次 8-Alt 刀 2；数据面见 desktop/synclocal/conflicts.go）----

export const RESOLUTION_LABELS = {
  'keep-local': '以本机为准重投',
  'keep-remote': '接受远端（覆盖本机未同步修改）',
  'drop-local': '放弃本机这笔',
  // 1.0.218：ENTITY_GONE + 本机行仍在时开放（键名与 Go synclocal.ResolveRecreate 一致）。
  'recreate': '以本机内容重建到服务器'
}

// 冲突种类中文文案。与 Go 侧 kind 一一对应（synclocal：CONFLICT_ON_PULL…ATTACHMENT_DIVERGED 共 7 类）；
// 未登记的 kind 原样显示，绝不静默隐藏（新 kind 出现时 UI 必须先看得见，再决定是否补文案）。
export const KIND_LABELS = {
  CONFLICT_ON_PULL: '同步时本机有未推送的修改，与远端新版本相撞',
  PUSH_CONFLICT: '推送时被服务端判为版本冲突（他人已改）',
  ENTITY_GONE: '远端该实体已被删除',
  OP_ID_REUSE: '同一笔操作被不同内容复用（已按陈旧处理）',
  STALE: '操作已过时（身份/顺序被服务端拒绝）',
  RETRY_EXHAUSTED: '连续三次推送失败，已停手等你处理',
  ATTACHMENT_DIVERGED: '同名附件内容不一致'
}

// conflictList / outboxList / conflictResolve —— 参数顺序与 wails 绑定一致（刀 1 生成物）。
// 分页参数在前端就先归位（limit 落默认值、offset 不为负），Go 侧同规则兜底。
const pageSize = (v, dft) => { const n = Number(v) || 0; return n > 0 ? Math.min(n, 200) : dft }
const pageOffset = (v) => Math.max(0, Number(v) || 0)
export const conflictList = (onlyOpen = true, limit = 50, offset = 0) =>
  callBridge('ConflictList', !!onlyOpen, pageSize(limit, 50), pageOffset(offset))
export const outboxList = (statuses = '', limit = 50, offset = 0) =>
  callBridge('OutboxList', String(statuses || ''), pageSize(limit, 50), pageOffset(offset))
export const conflictResolve = (id, resolution, confirmed = false) =>
  callBridge('ConflictResolve', Number(id), String(resolution), !!confirmed)
// syncStatus 概况条（cursor/epoch/pending_ops/conflict_count/conflict_total/logged_in）。
export const syncStatus = () => callBridge('SyncStatus')

// formatConflictError：Go 侧哨兵本身已是中文（可直接上屏），这里只兜桥不可用与网络类；
// 未知错误原样透传，禁静默吞错（同便携备份口径）。
export function formatConflictError (err) {
  const m = String((err && err.message) || err || '')
  if (m.includes('DESKTOP_BRIDGE_UNAVAILABLE')) return '桌面桥不可用'
  if (m.includes('sync engine not initialized') || m.includes('同步引擎未初始化')) return '同步引擎未就绪，请先进入工作区'
  return m || '操作失败'
}

// summarizeConflictPayload 只读展示用的载荷摘要（display-only，不参与任何处置判定）。
// 载荷形状由 server/syncspec 权威产出（NotePayload / FolderPayload / FolderOrderPayload），
// 这里只做"人类可读"降级：认不出的形态原样截断显示，不猜语义、不丢信息。
export function summarizeConflictPayload (raw) {
  const text = String(raw || '')
  if (!text.trim()) return '（无内容）'
  let o
  try { o = JSON.parse(text) } catch { return text.slice(0, 400) }
  if (o && Array.isArray(o.order)) {
    return `文件夹排序：共 ${o.order.length} 项`
  }
  if (o && typeof o.name === 'string') {
    return `文件夹「${o.name}」（版本 ${o.version ?? '?'}）`
  }
  if (o && (typeof o.title === 'string' || typeof o.content === 'string')) {
    const bits = []
    bits.push(`标题「${o.title || '（无标题）'}」`)
    if (o.is_encrypted) bits.push('加密内容（需口令才可读）')
    else {
      const plain = String(o.content || '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()
      bits.push(plain ? `正文 ${plain.slice(0, 120)}` : '正文为空')
    }
    if (o.deleted_at) bits.push('处于回收站')
    bits.push(`版本 ${o.version ?? '?'}`)
    return bits.join(' · ')
  }
  return text.slice(0, 400)
}

// 另存副本（刀 1 推迟到此刀的处置补充）：**换身份新建**（新 sync_id）——红线 27
//（冲突副本不得再次进入 Sync）由"换身份新建"天然满足，不需要任何特殊通道。
// 1.0.218：落库改走 Go 侧 ConflictSaveCopy 绑定（在此之上补"落回原文件夹"），
// 本函数仅保留门槛判定供按钮显隐（真正的校验 Go 侧独立再做一遍，不信任 UI）。
export function conflictCopyCandidate (raw) {
  const text = String(raw || '')
  if (!text.trim()) return { ok: false, reason: '本机内容缺失，无法另存' }
  let o
  try { o = JSON.parse(text) } catch { return { ok: false, reason: '本机内容不是笔记载荷，无法另存' } }
  if (!o || typeof o.title !== 'string' || typeof o.content !== 'string') {
    return { ok: false, reason: '仅笔记支持另存副本（文件夹/排序冲突不适用）' }
  }
  if (o.is_encrypted) {
    return { ok: false, reason: '加密笔记请在本机解锁后手动另存（载荷内只有密文）' }
  }
  return { ok: true, title: o.title, content: o.content, tags: o.tags || '' }
}

// conflictSaveCopy 另存为本机新笔记（1.0.218：改走 Go 侧 ConflictSaveCopy 绑定）。
// Go 侧：读冲突行 → 解析本机载荷（加密件/非笔记件拒绝，与上方门槛同语义）→ 解析
// folder_sync_id **落回原文件夹**（本地已无该文件夹则回落「我的笔记」并在返回值标记）→
// 复用本地 CreateNote 核心（新 sync_id 新建 + 正常入 outbox）。
// 返回 { note, folder_name, folder_fallback }。
export async function conflictSaveCopy (conflictID) {
  return callBridge('ConflictSaveCopy', Number(conflictID))
}

// ---- 后台同步调度（批次 8-Alt 刀 3；ADR-005 §二.4 触发器集合）----

// syncScheduleStatus 只读：自动同步健康度（下次时间/连续失败/是否停手/离线探测中）。
export const syncScheduleStatus = () => callBridge('SyncScheduleStatus')

// notifyResume 告知 Go 侧"页面回到前台了"，可考虑立刻补一轮（调度器自己会合并，不会叠加）。
export const notifyResume = () => callBridge('NotifyResume')

// syncPaused 只读（v21.158 · X5）：换代/导入窗口内调度闸门是否关闭。
// UI 据此提示「正在初始化全量同步，期间同步会排队」——纯可见性，不改任何同步语义。
export const syncPaused = () => callBridge('SyncPaused')

// bootstrapStatus 只读（2.0.3 · P1-3）：换代进行到哪个阶段 + 进入时刻。
// 返回 {phase, since_ms} | null（本进程内无换代记录）。阶段值契约见
// desktop/bootstrap/progress.go（只能追加不能改名，中文映射在 SyncInitBanner）。
export const bootstrapStatus = () => callBridge('BootstrapStatus')

let resumeTriggerInstalled = false

// installDesktopResumeTrigger 把"回前台/拿到焦点"接成一次唤醒（仅桌面构建调用）。
// 返回值是卸载函数（测试与热更新用）。注册失败/桥不可用一律静默——它只是加速器，不是正确性依赖。
export function installDesktopResumeTrigger () {
  if (resumeTriggerInstalled || typeof document === 'undefined' || typeof window === 'undefined') {
    return () => {}
  }
  resumeTriggerInstalled = true
  const fire = () => {
    if (typeof document !== 'undefined' && document.visibilityState && document.visibilityState !== 'visible') return
    Promise.resolve().then(notifyResume).catch(() => {}) // 尽力而为：未登录/无桥都不得冒泡成错误提示
  }
  document.addEventListener('visibilitychange', fire)
  window.addEventListener('focus', fire)
  return () => {
    resumeTriggerInstalled = false
    document.removeEventListener('visibilitychange', fire)
    window.removeEventListener('focus', fire)
  }
}

// 测试/热更新用：查询当前是否已装（避免重复注册叠加监听）。
export function desktopResumeTriggerInstalled () {
  return resumeTriggerInstalled
}

// ---- 账号与登录（批次 8-Alt 刀 6）----
//
// 桌面壳此前没有远端登录入口：登录页打的 /auth/login 被 transport 映射到 P0-A 桩
// LocalLogin（丢弃账密、恒成功），而"退出登录"打的 /auth/logout 是空映射——Go 侧
// Logout（会清 DPAPI 封存票 + 卸工）从未被调用。这几个封装就是把真绑定接到 UI 上。

// authStatus 会话与档案可见性（logged_in/accounting/server_url/username/
// relogin_required/revalidate_due/profile_openable…，键集见 desktop/sync_bindings.go AuthStatus）。
export const authStatus = () => callBridge('AuthStatus')

// remoteLogin 真实账号登录远端服务器。参数顺序与 wails 绑定一致：baseURL, username, password。
// 返回额外字段：first_sync_done(bool) / first_sync_note(string) —— Go 侧"登录即等首轮同步"
// 的有界等待结果；未完成时 note 是可直接上屏的说明（数据仍在后台陆续到达，不是失败）。
// v21.151：2FA 账号返回 {totp_required: true, totp_token}（未过二因子，未挂工），
// 前端收动态码后走 remoteLoginTOTP 第二步。
export const remoteLogin = (baseURL, username, password) =>
  callBridge('RemoteLogin', String(baseURL || '').trim(), String(username || '').trim(), String(password || ''))

// remoteLoginTOTP v21.151 两步验证第二步：临时票 + 动态码（或恢复码）换正式会话。
// 参数顺序与 wails 绑定一致：baseURL, username, totpToken, code。密码不在本步出现。
export const remoteLoginTOTP = (baseURL, username, totpToken, code) =>
  callBridge('RemoteLoginTOTP', String(baseURL || '').trim(), String(username || '').trim(),
    String(totpToken || ''), String(code || '').trim())

// hardReloadHome 会话变更 / 库代际变更后回到「干净起始页」。
//
// 为什么不是 window.location.reload()：它重载的是**当前 URL**。用户在 /profile 这类深链接页
// 触发会话变更时，重载会去请求 /profile —— 桌面资源服务器在资产未命中且未配 Handler 时返回
// 404，WebView2 弹出"找不到 http://wails.localhost 页"（v21.140 已给桌面补上 SPA fallback，
// 该 404 不再出现）。即便如此这里仍显式回到根路径，原因有二：
//   ① 语义正确 —— 会话/库换代后应回到干净起始页，而不是把用户留在"库里已换代"的旧页面上；
//   ② 双保险 —— 不依赖 fallback 是否生效（DevServer 场景下 fallback 本来就不生效）。
// 参数 loc 仅用于测试注入（默认 window.location）：本函数要断言"调到根路径"，
// 而 jsdom 不实现真实导航，注入后即可用 spy 钉住行为。
export function hardReloadHome (loc) {
  const target = loc || (typeof window !== 'undefined' ? window.location : null)
  if (target && typeof target.replace === 'function') target.replace('/')
}

// logoutDesktop 走 Go Logout：卸工 + DropRefreshSealed（清 DPAPI 封存的 refresh）。
// 与 auth store 的本地 clearAuth 是两件事，两边都要做，顺序由调用方保证（先远端后本地）。
export const logoutDesktop = () => callBridge('Logout')

// bootstrapNow 显式重建同步（换代/游标落后/跨服务器重绑后的出路）。耗时长（下载快照）。
export const bootstrapNow = () => callBridge('BootstrapNow')

// syncNow 立即同步一轮（面板显示"待推送 N 笔"时必须给出手动触发的出路，不能只让用户等空闲周期）。
export const syncNow = () => callBridge('SyncNow')

// formatAuthError 把登录链错误翻成人话。Go 侧已区分账密错(BAD_CREDENTIALS)/吊销(AUTH_REVOKED)/
// 锁定(429)/传输错(sync transport)，这里只做兜底与措辞统一；未知错误原样透传，禁静默吞错。
export function formatAuthError (err) {
  const m = String((err && err.message) || err || '')
  if (m.includes('DESKTOP_BRIDGE_UNAVAILABLE')) return '桌面桥不可用'
  if (m.includes('BAD_CREDENTIALS')) return '用户名或密码错误'
  if (m.includes('AUTH_REVOKED')) return '登录会话已失效（可能在别处登出或被吊销），请重新登录'
  if (m.includes('429') || m.includes('尝试次数过多')) return '登录尝试次数过多，请稍后再试'
  if (m.includes('sync transport') || m.includes('dial tcp') || m.includes('no such host') ||
      m.includes('i/o timeout') || m.includes('connection refused')) {
    return '连不上这台服务器，请检查地址、网络与端口（生产环境请用 https://）'
  }
  if (m.includes('NOT_LOGGED_IN')) return '当前未登录，请先登录'
  // v21.152：封存凭据缺失/被吊销 → 旧行为把裸 "NO_REFRESH" 弹给用户（生产实测），
  // 用户无从判断该做什么；统一给可行动文案（Go 侧新哨兵文案同义，此处兜底）。
  if (m.includes('NO_REFRESH') || m.includes('凭据已失效')) return '本机登录凭据已失效，请在「账号与设备」重新登录'
  return m || '操作失败'
}

// 登录表单的前端预校验：与 Go 侧 RemoteLogin 同规则（双端各挡一次，UI 即时反馈、
// 后端不信任前端）。返回 null 表示通过，否则为可直接上屏的提示。
export function validateLoginInput (baseURL, username, password) {
  const b = String(baseURL || '').trim()
  const u = String(username || '').trim()
  if (!b || !u || !password) return '服务器地址、用户名、密码都要填'
  let parsed
  try { parsed = new URL(b) } catch { return '服务器地址要写成 http:// 或 https:// 开头，例如 http://192.0.2.10:13389' }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return '服务器地址只支持 http:// 或 https://'
  if (!parsed.host) return '服务器地址缺少主机名'
  return null
}

// forceFullResync 强制全量重拉：重置游标 → 下次调度轮从服务端 change_log 头部重拉全部。
// 适用于「cursor 与服务端实际数据不匹配」（服务端重建/换代/数据修复后）。
export const forceFullResync = () => callBridge('ForceFullResync')

// syncDiag 同步诊断：返回游标/epoch/轮次/错误/笔记数等全貌。
export const syncDiag = () => callBridge('SyncDiag')

// installSyncRefreshPolling 轮询同步轮次，检测到新轮次完成后触发回调。
// 解决：登录→整页重载后，后台同步可能仍在进行，但前端没有机制感知"同步完了该刷新列表了"。
//
// v21.165：除 `rounds` 外**同时盯 `gen_epoch`（换代代数）**。
// 定罪（204 轮桌面走查）：首装登录走的是 bootstrap 换代，它**不产生同步轮次**，
// 于是列表在换代前渲染成空后永远不刷新 —— 库里已有服务端同步来的笔记，UI 却恒显
// 「共 0 条笔记」直到重启。换代代数由 Go 侧在接管成功后推进，此处零新增绑定即可感知。
//
// 轮询间隔 15s → 5s（v21.165）：首装换代通常在登录后 1~2s 内完成，用户对空列表的
// 容忍窗口远短于 15s；5s 是「换代感知及时」与「桥调用开销」的折中（仅桌面端、仅在
// 视图挂载期间轮询）。
export function installSyncRefreshPolling (onSyncDone) {
  if (typeof window === 'undefined' || !window.go) return
  let lastRounds = -1
  let lastGen = -1
  let failStreak = 0
  // ⭐ v21.220（213 实机定罪）：**心跳兜底** —— 每 HEARTBEAT_TICKS 拍无条件回调一次。
  //
  // 定罪：原实现只认 `gen_epoch` 变大与 `rounds` 增长。而 213 实机里，
  // 换代在**监听器建立之前**就完成了（首拍 `lastGen` 即追平当前值 1），
  // 此后 gen_epoch 与 rounds 都不再变化 ⇒ **回调永不触发** ⇒
  // 首屏 `authStatus` 失败后没有任何重试路径 ⇒ 账号卡片永久停在 `st={}` ⇒
  // 显示「未登录：处于离线单机状态」，而 Go 侧 48 笔记/凭据完好。
  //
  // 教训（写死成不变式）：**事件驱动只能做加速，不能做唯一重试路径。**
  // 「变化才通知」在「变化早于监听建立」时必然丢失，必须配一条无条件心跳。
  const HEARTBEAT_TICKS = 4 // 5s × 4 = 20s
  let tick = 0
  const timer = setInterval(async () => {
    tick++
    try {
      const svc = window.go.main.LocalService
      if (!svc || typeof svc.SyncScheduleStatus !== 'function') return
      const s = await svc.SyncScheduleStatus()
      if (!s) return
      // v21.170：轮询本身失败（换代窗口 database is closed / 网络抖动）时，
      // 原先 catch 静默 ⇒ 轮询链彻底断掉、界面永不刷新。
      // 现在：失败后**主动回调一次**让调用方重取状态（账号卡片据此退避重试）。
      failStreak = 0
      // 换代代数变化 = 数据所在的库已换（列表必须重取）
      if (typeof s.gen_epoch === 'number') {
        if (lastGen >= 0 && s.gen_epoch > lastGen && typeof onSyncDone === 'function') {
          onSyncDone()
        }
        lastGen = s.gen_epoch
      }
      if (typeof s.rounds === 'number') {
        if (lastRounds >= 0 && s.rounds > lastRounds && typeof onSyncDone === 'function') {
          onSyncDone()
        }
        lastRounds = s.rounds
      }
      // 心跳：无论有没有变化，到点就通知一次，让 UI 有机会自愈到正确状态。
      if (tick % HEARTBEAT_TICKS === 0 && typeof onSyncDone === 'function') {
        onSyncDone()
      }
    } catch {
      // v21.170：连续失败 ≥2 次才回调，避免偶发单次抖动刷屏
      failStreak++
      if (failStreak >= 2 && typeof onSyncDone === 'function') onSyncDone()
    }
  }, 5000)
  return () => clearInterval(timer)
}

// reportJSError 把前端全局异常摘要写入 Go 日志（v21.194 白屏取证轮）。
// 白屏类缺陷（页面活着但 Vue 未挂载）此前零取证通道——本封装是永久性的 jserr 面。
// 非桌面/桥不可用静默（Web 端 console 已足够）。
export function reportJSError (summary) {
  try {
    const s = window.go && window.go.main && window.go.main.LocalService
    if (s && typeof s.ReportJSError === 'function') {
      s.ReportJSError(String(summary || '').slice(0, 500))
    }
  } catch { /* 上报通道本身不得再抛 */ }
}

// v21.196 O3：钉版 runtime 来源状态（是否已降级到系统 Evergreen）。
// 降级意味着可能撞上 Evergreen 153 的渲染崩溃（X1），必须让用户知道而不是静默。
// 桥不可用 / 旧版本无此绑定 → 返回 null（调用方按「未知」处理，不报错）。
export async function fetchRuntimeStatus () {
  try {
    const s = window.go && window.go.main && window.go.main.LocalService
    if (!s || typeof s.RuntimeStatus !== 'function') return null
    return await s.RuntimeStatus()
  } catch {
    return null
  }
}

// v21.196 O2：一键导出诊断包（日志 + runtime 来源 + WebView2 实测版本 + 档案代数/实体计数）。
// 只读采集、不含笔记正文；返回本地落盘路径，由用户自行决定是否外发。
export async function exportDiagnostics () {
  const s = window.go && window.go.main && window.go.main.LocalService
  if (!s || typeof s.ExportDiagnostics !== 'function') {
    throw new Error('当前版本不支持诊断包导出，请升级到最新版')
  }
  return await s.ExportDiagnostics()
}

// installGlobalErrorHooks 全局异常钩子（window.onerror + unhandledrejection）。
// 行为：①首条异常即经 reportJSError 落 Go 日志；②异常累计 ≥2 条 → 在 #app 之上
// 叠加错误横幅（boot-guard 占位被移除后仍可见），给出「重载」出口——**保证任何
// JS 层失败都不再表现为纯白屏**（2026-10-08 真机三症状取证轮的根治兜底）。
// 返回卸载函数。Web 构建同样生效（仅去掉 Go 上报）。
export function installGlobalErrorHooks () {
  if (typeof window === 'undefined' || window.__snErrHooksInstalled) return () => {}
  window.__snErrHooksInstalled = true
  let count = 0
  const banner = () => {
    if (count < 2) return
    let bar = document.getElementById('sn-error-banner')
    if (bar || !document.body) return
    bar = document.createElement('div')
    bar.id = 'sn-error-banner'
    bar.style.cssText = 'position:fixed;left:0;right:0;bottom:0;z-index:99999;background:#fef0f0;color:#c45656;' +
      'border-top:1px solid #fbc4c4;padding:10px 16px;font:13px/1.6 system-ui,sans-serif;'
    bar.innerHTML = '应用遇到异常，界面可能不完整。<a href="#" id="sn-error-reload" style="color:#409eff;margin-left:8px">重载应用</a>'
    document.body.appendChild(bar)
    document.getElementById('sn-error-reload').addEventListener('click', (e) => {
      e.preventDefault()
      window.location.reload()
    })
  }
  const record = (kind, msg, src) => {
    count++
    reportJSError(`${kind}: ${msg} @ ${src || ''} (#${count})`)
    banner()
  }
  const onErr = (e) => record('error', (e && e.message) || 'unknown', e && (e.filename ? `${e.filename}:${e.lineno}` : ''))
  const onRej = (e) => {
    const r = e && e.reason
    record('unhandledrejection', (r && (r.message || String(r))) || 'unknown', '')
  }
  window.addEventListener('error', onErr)
  window.addEventListener('unhandledrejection', onRej)
  return () => {
    window.removeEventListener('error', onErr)
    window.removeEventListener('unhandledrejection', onRej)
    window.__snErrHooksInstalled = false
  }
}
