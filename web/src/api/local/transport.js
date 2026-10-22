// LocalTransport（离线方案 v20-Project §12/§14 + 千问 v5 单一 axios choke point）
//
// 桌面构建（VITE_DESKTOP=1）下，把 web 全部 /api 请求路由到 Wails Binding ——
// **前端 store 与 7 个 api 模块零改动**；未接线端点 **reject 501**（绝不静默伪装成功——
// 早期版本返回「形状像响应的 501」，被 axios 当成功交给调用方，导致"导入成功但笔记空白"）。
// 每个映射的响应形状都对照 server/handler 逐字段复刻（含 {message,version} 这类
// 非直觉形状）；Go 侧错误按 sentinel 文案分类成同构 HTTP 状态码，让既有
// 拦截器/toast/冲突提示逻辑原样工作。

export const IS_DESKTOP = import.meta.env.VITE_DESKTOP === '1'

const svc = () => {
  const s = window.go && window.go.main && window.go.main.LocalService
  if (!s) throw new Error('LocalService bridge unavailable')
  return s
}

// Go facade 哨兵文案 → HTTP 状态（与服务端 handler 行为对齐）
const ERROR_RULES = [
  [/version conflict/i, 409, 'VERSION_CONFLICT'],
  [/quota exceeded/i, 403, 'QUOTA_EXCEEDED'],
  [/wrong password/i, 403, 'WRONG_PASSWORD'],
  [/folder not empty/i, 409, 'FOLDER_NOT_EMPTY'],
  [/folder not found/i, 404, ''],
  [/already encrypted|not encrypted|file type rejected|invalid folder|两次输入的密码不一致|密码过长/i, 400, ''],
  // 导入（ImportNote 绑定）：Go 侧中文文案直接面向用户
  [/仅支持 \.md|导入文件为空|导入文件过大/, 400, ''],
  [/已达到笔记数量上限/, 403, 'QUOTA_EXCEEDED'],
  [/no rows|笔记不存在|不存在或不在回收站/i, 404, '']
]

function toHttpError (config, e) {
  const msg = (e && e.message) || String(e)
  let status = 500
  let code = ''
  for (const [re, st, cd] of ERROR_RULES) {
    if (re.test(msg)) { status = st; code = cd; break }
  }
  const err = new Error(msg)
  err.config = config
  err.response = {
    data: code ? { error: msg, code } : { error: msg },
    status, statusText: '', headers: {}, config
  }
  return err
}

const num = (v, d) => (v === undefined || v === null || v === '' ? d : Number(v))
// folder_id 归一：与服务端 handler 同规则（缺省=全部；default/0=我的笔记；正整数=指定夹）
const normFolder = (v) => {
  if (v === undefined || v === null || v === '') return null
  if (v === 'default' || v === 0 || v === '0') return 0
  const n = Number(v)
  return Number.isFinite(n) ? n : null
}
const bodyOf = (config) => {
  if (!config.data) return {}
  if (typeof config.data === 'string') { try { return JSON.parse(config.data) } catch { return {} } }
  return config.data
}
const idOf = (m) => Number(m[1])

// 路由表：[METHOD, 路径正则(不含 /api 前缀), async (匹配组, config) => {data, status?}]
const ROUTES = [
  ['POST', /^\/auth\/login$/, async () => ({ data: await svc().LocalLogin() })],
  ['POST', /^\/auth\/logout$/, async () => ({ data: {} })],
  // v21.144 补接线：桌面守卫冷启动会调 `auth.tryRefresh()` → `POST /auth/refresh`。
  // 桌面没有 cookie/refresh 语义（设备会话由 Go 侧 DPAPI 管理），故返回空对象即可让
  // tryRefresh「无害成功」—— 绝不能走到未接线的 501 reject：那会触发 request.js 的
  // 401 分支 `window.location.replace('/login')`，在 WebView2 初始化中途整页导航，
  // 轻则丢桥、重则 Chromium.Eval 报 not in correct state 使应用退出（181 回归根因之一）。
  ['POST', /^\/auth\/refresh$/, async () => ({ data: {} })],
  ['GET', /^\/auth\/setup-status$/, async () => ({ data: await svc().SetupStatus() })],
  ['GET', /^\/public-config$/, async () => ({ data: await svc().GetPublicConfig() })],
  ['GET', /^\/quota$/, async () => ({ data: await svc().GetQuota() })],

  ['GET', /^\/notes$/, async (m, c) => {
    const p = c.params || {}
    return { data: await svc().ListNotes(num(p.page, 1), num(p.page_size, 20), p.q || '', p.tag || '', p.sort_by || 'updated_at', normFolder(p.folder_id)) }
  }],
  ['GET', /^\/notes\/tags$/, async () => ({ data: { tags: await svc().ListTags() } })],
  ['GET', /^\/notes\/(\d+)$/, async m => ({ data: await svc().GetNote(idOf(m)) })],
  ['POST', /^\/notes$/, async (m, c) => {
    const b = bodyOf(c)
    return { data: await svc().CreateNote(b.title || '', b.content || '', b.tags || '', normFolder(b.folder_id)), status: 201 }
  }],
  // 导入笔记（.md/.txt/.html/.htm）。形状与 Web 端 POST /api/notes/import 的 201 一致（返回 model.Note）。
  // 转换/消毒由 Go 侧 notetext 完成，与在线导入**同一实现**（见 desktop/import_bindings.go）。
  //
  // v21.213（211b 实机：导入的笔记一律落到「我的笔记」而非当前文件夹）：透传 folder_id。
  // 此前 Go 侧 ImportNote 硬编码 folderID=nil，前端传什么都不起作用。
  // 兼容：Web 端 /notes/import 忽略该字段（表单里没有），故 desktop 侧传 undefined 即 nil。
  ['POST', /^\/notes\/import$/, async (m, c) => {
    const file = pickFile(c)
    if (!file) {
      throw httpErr('请选择文件', 400)
    }
    const bytes = new Uint8Array(await file.arrayBuffer())
    const folderId = normFolder(folderOfForm(c))
    return { data: await svc().ImportNote(file.name, Array.from(bytes), folderId), status: 201 }
  }],
  ['PUT', /^\/notes\/(\d+)$/, async (m, c) => {
    const b = bodyOf(c)
    if (b.password) {
      return { data: await svc().UpdateEncrypted(idOf(m), b.title || '', b.tags || '', b.content || '', b.password, num(b.version, 0)) }
    }
    const note = await svc().UpdateNote(idOf(m), b.title || '', b.content || '', b.tags || '', num(b.version, 0))
    return { data: { message: '更新成功', version: note.version } } // handler 非加密分支同形
  }],
  ['DELETE', /^\/notes\/(\d+)$/, async m => ({ data: (await svc().DeleteNote(idOf(m)), { message: '删除成功' }) })],
  ['PUT', /^\/notes\/(\d+)\/folder$/, async (m, c) => {
    await svc().MoveNote(idOf(m), normFolder(bodyOf(c).folder_id))
    return { data: { message: '移动成功' } }
  }],
  ['POST', /^\/notes\/(\d+)\/copy$/, async (m, c) => ({
    data: await svc().CopyNote(idOf(m), normFolder(bodyOf(c).folder_id)), status: 201
  })],
  ['POST', /^\/notes\/batch-delete$/, async (m, c) => ({ data: await svc().BatchDelete(bodyOf(c).ids || []) })],

  ['PUT', /^\/notes\/(\d+)\/encrypt$/, async (m, c) => {
    const b = bodyOf(c)
    await svc().EncryptNote(idOf(m), b.password, b.confirm_password)
    return { data: { message: '加密成功', is_encrypted: true } }
  }],
  ['DELETE', /^\/notes\/(\d+)\/encrypt$/, async (m, c) => {
    const content = await svc().DecryptNote(idOf(m), bodyOf(c).password)
    return { data: { message: '解密成功', is_encrypted: false, content } }
  }],
  ['POST', /^\/notes\/(\d+)\/verify-encrypt$/, async (m, c) => ({ data: await svc().VerifyEncrypt(idOf(m), bodyOf(c).password) })],

  ['GET', /^\/trash\/notes$/, async (m, c) => {
    const p = c.params || {}
    return { data: await svc().ListTrash(num(p.page, 1), num(p.page_size, 20)) }
  }],
  ['GET', /^\/trash\/stats$/, async () => ({ data: await svc().TrashStats() })],
  ['POST', /^\/notes\/(\d+)\/restore$/, async m => ({ data: await svc().RestoreNote(idOf(m)) })],
  ['DELETE', /^\/notes\/(\d+)\/permanent$/, async m => ({ data: await svc().PermanentDeleteNote(idOf(m)) })],
  ['DELETE', /^\/trash\/notes$/, async () => ({ data: await svc().EmptyTrash() })],

  ['GET', /^\/folders$/, async () => ({ data: { folders: await svc().ListFolders() } })],
  ['POST', /^\/folders$/, async (m, c) => ({ data: await svc().CreateFolder(bodyOf(c).name), status: 201 })],
  ['PUT', /^\/folders\/(\d+)$/, async (m, c) => {
    const b = bodyOf(c)
    if (b.name) await svc().RenameFolder(idOf(m), b.name)
    if (b.position) await svc().MoveFolder(idOf(m), b.position)
    return { data: { message: '更新成功' } }
  }],
  ['DELETE', /^\/folders\/(\d+)$/, async m => ({ data: (await svc().DeleteFolder(idOf(m)), { message: '删除成功' }) })]
]

// httpErr 构造带 __status 的错误对象；localAdapter 的 catch 据此映射为同构 HTTP 响应。
// 注意：**必须 throw 而不是 return**——axios 的自定义 adapter 不会被自动 settle()，
// 返回一个「形状像响应的非 2xx」会被 axios 当成成功结果交给调用方（历史缺陷根因：
// 未接线路由返回 resolved 501 → 前端把错误对象当笔记数据插入并弹「导入成功」）。
function httpErr (msg, status) {
  const err = new Error(msg)
  err.__status = status
  return err
}

// pickFile 从 config.data（FormData）里取上传文件；缺失或形态不对返回 null。
function pickFile (config) {
  const fd = config.data
  if (!(fd instanceof FormData)) return null
  const file = fd.get('file')
  return file && typeof file.arrayBuffer === 'function' ? file : null
}

// folderOfForm 从 FormData 里取目标文件夹（v21.213）。
//
// 为什么导入要用 FormData 传文件夹：/notes/import 的 body 是 multipart，
// 没法像 JSON 那样放 folder_id 字段，而 axios 侧又不宜把 config.params
// 拼进 FormData（服务端 multipart 解析不读query）。
// 约定用**独立表单字段** `folder_id`：Web 端 handler 忽略它（零影响），
// 桌面 transport 读它并透传给 Go —— 同一份前端代码在两端行为一致。
// 缺失/非法 ⇒ undefined ⇒ normFolder 得null ⇒ Go侧 folderID=nil ⇒「我的笔记」，
// 与既有语义完全一致（老调用点零破坏）。
function folderOfForm (config) {
  const fd = config.data
  if (!(fd instanceof FormData)) return undefined
  const v = fd.get('folder_id')
  return v === null || v === '' ? undefined : v
}

async function handleUpload (config) {
  const file = pickFile(config)
  if (!file) {
    throw httpErr('未选择文件', 400)
  }
  const bytes = new Uint8Array(await file.arrayBuffer())
  const r = await svc().UploadBytes(file.name, Array.from(bytes))
  return { data: { url: r.url } }
}

export async function localAdapter (config) {
  const raw = config.url || ''
  const path = raw.replace(/^\/api/, '').split('?')[0] || '/'
  const method = (config.method || 'get').toUpperCase()
  let result
  try {
    if (method === 'POST' && path === '/upload') {
      result = await handleUpload(config)
    } else {
      const route = ROUTES.find(([mt, re]) => mt === method && re.test(path))
      if (!route) {
        // 未接线端点：**必须 reject**（见 httpErr 注释）。文案面向用户，不暴露内部路径细节之外的信息。
        throw httpErr(`桌面版尚未支持该功能（${method} ${path}），请在网页版操作`, 501)
      }
      result = await route[2](path.match(route[1]), config)
    }
  } catch (e) {
    throw e.__status
      ? Object.assign(new Error(e.message), { config, response: { data: { error: e.message }, status: e.__status, statusText: '', headers: {}, config } })
      : toHttpError(config, e)
  }
  return { data: result.data, status: result.status || 200, statusText: 'OK', headers: { 'content-type': 'application/json' }, config }
}

export function installLocalTransport (service) {
  service.defaults.adapter = localAdapter
}

// ⭐ v21.218：清空 Go 侧设备会话（封存 refresh/secret）。
//
// 供 request.js 的桌面端 401 分支调用：**只有用户明确确认要重登时才清**。
// 清 Go 侧封存凭据是破坏性动作（清掉就必须重新输密码），因此绝不能由拦截器自动触发；
// Web 端由 clearTokens() 清本地 token 即可，桌面端必须走这里。
//
// 幂等：重复调用不报错。绑定不可用时抛错由调用方吞（此时至少本地展示态已清）。
export async function clearDesktopCredentials () {
  const s = svc()
  if (typeof s.Logout === 'function') {
    await s.Logout()
  }
}
