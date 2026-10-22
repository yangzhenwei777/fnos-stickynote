// TiptapEditor 编辑器常量（评审 B4 从组件外提；纯数据，无闭包依赖）

// 字体 / 字号选项
export const FONTS = [
  { label: '默认字体', value: '' },
  { label: '宋体', value: 'SimSun, 宋体, serif' },
  { label: '黑体', value: 'SimHei, 黑体, sans-serif' },
  { label: '楷体', value: 'KaiTi, 楷体, serif' },
  { label: '微软雅黑', value: '"Microsoft YaHei", 微软雅黑, sans-serif' },
  // v21.220：全局默认字体栈首位（global.css，v21.168 起）同款；未安装时回落 Sans 系，
  // 装有 Noto Sans SC / 思源黑体的机器上选项即真实生效
  { label: 'Noto Sans SC', value: "'Noto Sans SC', 'Source Han Sans SC', sans-serif" },
  { label: 'Arial', value: 'Arial, Helvetica, sans-serif' },
  { label: 'Times New Roman', value: '"Times New Roman", Times, serif' },
  { label: 'Courier New', value: '"Courier New", Courier, monospace' }
]
export const FONT_SIZES = ['', '12px', '14px', '16px', '18px', '20px', '24px', '28px', '32px']

// 内容回流防抖（v21.85）：窗口内合并、超过 max wait 强制发出
export const EMIT_DEBOUNCE = 250
export const EMIT_MAX_WAIT = 2000

// 图片前端压缩：canvas 缩至最长边后上传；gif/svg 跳过（动图/矢量压缩会失真）
export const IMAGE_MAX_EDGE = 1600
export const IMAGE_SKIP_TYPES = ['image/gif', 'image/svg+xml']
