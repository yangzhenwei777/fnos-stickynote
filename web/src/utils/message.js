import { toast } from './toast'

// v21.107 P1-6：errorOnce 去重表（title → 最近弹出时间）。
// 用于拦截器全局兜底与透传调用点并存时避免同文案双弹；条目为有限文案集合，
// 防御性上限清空防长会话累积。
const errorOnceAt = new Map()

export const message = {
  success: (title, description) => toast.success(title, description),
  error: (title, description) => toast.error(title, description),
  // 同文案 windowMs 内只弹一次（默认 4s）
  errorOnce: (title, windowMs = 4000) => {
    const now = Date.now()
    const last = errorOnceAt.get(title)
    if (last !== undefined && now - last < windowMs) return
    if (errorOnceAt.size > 100) errorOnceAt.clear()
    errorOnceAt.set(title, now)
    toast.error(title)
  },
  warning: (title, description) => toast.warning(title, description),
  info: (title, description) => toast.info(title, description),
  confirm: (msg, options = {}) => {
    return ElMessageBox.confirm(msg, options.title || '提示', {
      confirmButtonText: options.confirmButtonText || '确定',
      cancelButtonText: options.cancelButtonText || '取消',
      type: options.type || 'info',
      // v21.37：透传 customClass（如 'msgbox-no-icon' 隐藏 type 默认图标）
      // v21.211：删除等不可逆/高风险操作传 danger: true，确认键与警示图标用红色
      // （按钮三色：蓝=主操作 红=高风险 灰=取消）；与 customClass 叠加而非覆盖
      ...((options.customClass !== undefined || options.danger)
        ? { customClass: [options.customClass, options.danger ? 'msgbox-danger' : ''].filter(Boolean).join(' ') }
        : {}),
      ...(options.danger ? { confirmButtonClass: 'el-button--danger' } : {})
    }).then(() => true).catch(() => false)
  }
}
