// useAbout.js —— 「关于」弹窗（A5+D4，2026-09-23）：桌面/Web 双端版本获取收口。
// 桌面（VITE_DESKTOP=1）走 Wails 绑定 GetVersion（sync_bindings.go:453，main.Version ldflags 注入）；
// Web 走 /api/version（handler.GetVersion，登录面 {version, build_time, git_commit}）。
// 版本取不到不阻塞弹窗（纯展示面），如实显示不带版本号。
import { IS_DESKTOP } from '@/api/local/transport'
import api from '@/api/request'
import { ElMessageBox } from 'element-plus'

export async function fetchAppVersion () {
  if (IS_DESKTOP && window.go && window.go.main && window.go.main.LocalService) {
    const v = await window.go.main.LocalService.GetVersion()
    return v || ''
  }
  const { data } = await api.get('/version')
  return (data && data.version) || ''
}

// v21.166 P0：版本号规范化——「关于」弹窗只在这里拼一次前缀。
// 事故背景：206 轮交付的 fpk 二进制漏注入 -ldflags，main.Version 落到源码默认值
// "v1.0.0"（本身带 v），前端再拼一个 'v' ⇒ 界面显示 "vv1.0.0"。
// 两道防线：①此函数剥掉任意前导 v，由showAbout 统一补；②verify-fpk 增加版本字面量断言，
// 让「二进制未注入版本号」在组包阶段就被拦下，而不是等用户装完才发现。
function normalizeVersion (raw) {
  return String(raw == null ? '' : raw).trim().replace(/^v/i, '')
}

export async function showAbout () {
  let ver = ''
  try {
    ver = normalizeVersion(await fetchAppVersion())
  } catch {
    // 版本面不可得时仍给「关于」入口，不静默失败也不抛栈
  }
  const lines = [
    'Sticky note' + (ver ? ' v' + ver : ''),
    'fnOS 原生便签 · Davy',
    '© 2026 Davy'
  ]
  await ElMessageBox.alert(lines.join('<br/>'), '关于', {
    confirmButtonText: '知道了',
    dangerouslyUseHTMLString: true
  })
}
