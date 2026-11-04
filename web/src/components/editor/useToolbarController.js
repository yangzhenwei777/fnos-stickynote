// useToolbarController —— EditorToolbar 的全部工具栏逻辑（2026-11-03 巨型 Vue 拆分 刀5）。
// 抽取原则：零行为变化（script 体机械搬移），模板与 scoped 样式原样不动；
// 图标 IC / 字体常量（模板 v-for 引用）/ ColorPicker 留在 EditorToolbar.vue。
// 历史修复点注释（v21.85/89/200/220 等）全部原样保留——改动前先读懂，别删。
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'
import { message } from '@/utils/message'
import api from '@/api/request'
import { IC } from './icons'
import { FONTS, IMAGE_MAX_EDGE, IMAGE_SKIP_TYPES } from './constants'
import { isMarkdownPaste } from './pasteMarkdown'

// props 为父组件传入的响应式 props 对象（editor 实例 + tick 信号），原样沿用。
export function useToolbarController (props) {

// ---- 格式刷状态机（B4-4c 阶段B 内聚；行为与阶段A父端逐字一致，含 v21.83 拖选稳定 180ms 修复）----
const painterActive = ref(false)
let painterMarks = null
let painterTimer = null

function cancelPainter() {
  if (painterTimer) {
    clearTimeout(painterTimer)
    painterTimer = null
  }
  painterMarks = null
  painterActive.value = false
}

function flushPainter() {
  const ed = props.editor
  if (!painterMarks || !ed) return
  if (ed.state.selection.empty) return
  const marks = painterMarks
  cancelPainter()
  let chain = ed.chain().focus()
  marks.forEach(({ type, attrs }) => {
    const markType = ed.schema.marks[type]
    if (markType) chain = chain.setMark(markType, attrs)
  })
  chain.run()
}

function togglePainter() {
  const ed = props.editor
  if (!ed) return
  if (painterActive.value) {
    cancelPainter()
    return
  }
  const { from, to, empty } = ed.state.selection
  if (empty) {
    message.info('请先选中带有格式的文字，再点击格式刷')
    return
  }
  const marks = []
  ed.state.doc.nodesBetween(from, to, (node) => {
    node.marks.forEach((m) => {
      if (!marks.some((x) => x.type === m.type.name)) {
        marks.push({ type: m.type.name, attrs: { ...m.attrs } })
      }
    })
  })
  if (!marks.length) {
    message.info('所选文字没有可复制的格式')
    return
  }
  painterMarks = marks
  painterActive.value = true
}

// ---- 插入链接（B4-4c 阶段B 内聚）----
function insertLink() {
  const ed = props.editor
  if (!ed) return
  const prev = ed.getAttributes('link')
  const url = window.prompt('输入链接地址', prev?.href || 'https://')
  if (url === null) return
  if (url === '') {
    ed.chain().focus().unsetLink().run()
    return
  }
  ed.chain().focus().setLink({ href: url }).run()
}

// editor 事件订阅：selectionUpdate 驱动格式刷"选区稳定 180ms 后套用"；blur 作废待执行格式刷；
// Esc 取消。onMounted 注册、onBeforeUnmount 解绑（editor 实例由父组件持有，卸载时先解绑避免悬挂）。
function onSelectionUpdate() {
  if (painterActive.value && painterMarks) {
    const ed = props.editor
    if (!ed || ed.state.selection.empty) return
    if (painterTimer) clearTimeout(painterTimer)
    painterTimer = setTimeout(flushPainter, 180)
  }
}
function onBlur() {
  if (painterTimer) {
    clearTimeout(painterTimer)
    painterTimer = null
  }
}
function onKeydown(e) {
  if (e.key === 'Escape' && painterActive.value) cancelPainter()
}

let editorDom = null // onMounted 时捕获，卸载期不再回读 ed.view（彼时 view 已销毁）

onMounted(() => {
  const ed = props.editor
  if (!ed) return
  ed.on('selectionUpdate', onSelectionUpdate)
  ed.on('blur', onBlur)
  try {
    editorDom = ed.view.dom
    editorDom.addEventListener('keydown', onKeydown)
    // B4-4c 阶段B-3：粘贴监听（图片上传 + Markdown 转换）随上传逻辑迁入子组件
    editorDom.addEventListener('paste', handlePaste)
  } catch {
    // 极端情况下 view 未就绪：Esc/粘贴为增强项，事件订阅已生效，忽略
  }
})
onBeforeUnmount(() => {
  const ed = props.editor
  if (ed) {
    ed.off('selectionUpdate', onSelectionUpdate)
    ed.off('blur', onBlur)
  }
  if (editorDom) {
    editorDom.removeEventListener('keydown', onKeydown)
    editorDom.removeEventListener('paste', handlePaste)
    editorDom = null
  }
  if (painterTimer) { clearTimeout(painterTimer); painterTimer = null }
})

// 读取 tick 建立渲染依赖：editor 实例非响应式，isActive/can 结果仅靠本函数登记后随 tick 失效重算。
function trackToolbar() {
  return props.tick
}

// v21.89 颜色弹框受控开关：点【应用】后自动关闭（用户反馈：之前不关闭）
const colorPopoverVisible = ref(false)
const bgPopoverVisible = ref(false)

const textColor = computed(() => {
  trackToolbar()
  if (!props.editor) return '#000000'
  return props.editor.getAttributes('textStyle').color || '#000000'
})

const bgColor = computed(() => {
  trackToolbar()
  if (!props.editor) return '#fde047'
  return props.editor.getAttributes('highlight').color || '#fde047'
})

const isParagraph = computed(() => {
  trackToolbar()
  const ed = props.editor
  if (!ed) return true
  return !ed.isActive('heading', { level: 1 }) &&
    !ed.isActive('heading', { level: 2 }) &&
    !ed.isActive('heading', { level: 3 })
})

const blockLabel = computed(() => {
  trackToolbar()
  const ed = props.editor
  if (!ed) return '正文'
  if (ed.isActive('heading', { level: 1 })) return '标题 1'
  if (ed.isActive('heading', { level: 2 })) return '标题 2'
  if (ed.isActive('heading', { level: 3 })) return '标题 3'
  return '正文'
})

const currentFontFamily = computed(() => {
  trackToolbar()
  return props.editor?.getAttributes('textStyle').fontFamily || ''
})
const currentFontSize = computed(() => {
  trackToolbar()
  return props.editor?.getAttributes('textStyle').fontSize || ''
})

// v21.220（D1 拍板）：光标在代码块内时禁用字体/字号——CodeBlock schema marks:"" 禁一切
// textStyle mark，命令必然静默无效（用户 219 内测反馈「代码块字体换不了」的根因）。
// 与其静默失败，不如显式禁用 + title 提示。代码块锁定等宽字体为既定设计（业界惯例）。
const inCodeBlock = computed(() => {
  trackToolbar()
  const ed = props.editor
  if (!ed) return false
  return ed.isActive('codeBlock')
})
const fontLabel = computed(() => {
  const hit = FONTS.find((f) => f.value === currentFontFamily.value)
  return hit ? hit.label : '字体'
})
const sizeLabel = computed(() => currentFontSize.value || '字号')

const alignIcon = computed(() => {
  trackToolbar()
  const ed = props.editor
  if (ed?.isActive({ textAlign: 'center' })) return IC.alignCenter
  if (ed?.isActive({ textAlign: 'right' })) return IC.alignRight
  return IC.alignLeft
})

// ---- 工具栏命令 ----
function clearFormat() {
  props.editor?.chain().focus().unsetAllMarks().clearNodes().run()
}

function applyBlock(cmd) {
  const ed = props.editor
  if (!ed) return
  if (cmd === 'paragraph') {
    ed.chain().focus().setParagraph().run()
  } else {
    ed.chain().focus().toggleHeading({ level: Number(cmd[1]) }).run()
  }
}

function applyFont(cmd) {
  const ed = props.editor
  if (!ed) return
  if (cmd) ed.chain().focus().setFontFamily(cmd).run()
  else ed.chain().focus().unsetFontFamily().run()
}

function applySize(cmd) {
  const ed = props.editor
  if (!ed) return
  if (cmd) ed.chain().focus().setFontSize(cmd).run()
  else ed.chain().focus().unsetFontSize().run()
}

function applyAlign(align) {
  props.editor?.chain().focus().setTextAlign(align).run()
}

function indent() {
  const ed = props.editor
  if (!ed) return
  if (ed.can().sinkListItem('listItem')) {
    ed.chain().focus().sinkListItem('listItem').run()
  } else if (ed.can().sinkListItem('taskItem')) {
    ed.chain().focus().sinkListItem('taskItem').run()
  } else {
    ed.chain().focus().increaseIndent().run()
  }
}

function outdent() {
  const ed = props.editor
  if (!ed) return
  if (ed.can().liftListItem('listItem')) {
    ed.chain().focus().liftListItem('listItem').run()
  } else if (ed.can().liftListItem('taskItem')) {
    ed.chain().focus().liftListItem('taskItem').run()
  } else {
    ed.chain().focus().decreaseIndent().run()
  }
}

function tableOp(cmd) {
  const ed = props.editor
  if (!ed) return
  const chain = ed.chain().focus()
  switch (cmd) {
    case 'rowBefore': chain.addRowBefore().run(); break
    case 'rowAfter': chain.addRowAfter().run(); break
    case 'colBefore': chain.addColumnBefore().run(); break
    case 'colAfter': chain.addColumnAfter().run(); break
    case 'deleteRow': chain.deleteRow().run(); break
    case 'deleteCol': chain.deleteColumn().run(); break
    case 'deleteTable': chain.deleteTable().run(); break
    default: break
  }
}

function setColor(color) {
  if (!props.editor) return
  props.editor.chain().focus().setColor(color).run()
}

function setBgColor(color) {
  if (!props.editor) return
  props.editor.chain().focus().setHighlight({ color }).run()
}

// v20.1: 插入 3 行 3 列表格
function insertTable() {
  props.editor?.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()
}

// ---- 图片上传（B4-4c 阶段B-3 内聚；行为与父端逐字一致）----
const imageInput = ref(null)

function triggerImageUpload() {
  imageInput.value?.click()
}

function handleImageSelect(event) {
  const file = event.target.files[0]
  if (file) {
    uploadImage(file)
    event.target.value = ''
  }
}

function handlePaste(event) {
  const items = event.clipboardData?.items
  if (!items) return

  // v2.2.1（用户反馈 2b）：Markdown 文本转换已迁至 TiptapEditor 的
  // editorProps.handlePaste（官方钩子，防双插）。此处仅兜底图片上传；
  // 含 Markdown 文本的混合粘贴直接跳过，保持迁移前「Markdown 优先、图片忽略」语义，
  // 也避免 Markdown 已被钩子插入后图片再叠加一份。
  const text = event.clipboardData?.getData('text/plain')
  if (text && isMarkdownPaste(text)) return

  for (const item of items) {
    if (item.type.indexOf('image') === 0) {
      event.preventDefault()
      const file = item.getAsFile()
      if (file) {
        uploadImage(file)
      }
      break
    }
  }
}

// 图片前端压缩：canvas 缩至最长边 1600px 再上传（手机原图一张几 MB，拖慢笔记加载）
// 规则：jpeg/webp 按 q0.85 重编码，png 保格式（留透明通道），gif/svg 不动；
//       压缩后反而更大则回退原图，保证不会变更糟。
async function compressImage(file) {
  if (!file || IMAGE_SKIP_TYPES.includes(file.type) || !file.type.startsWith('image/')) {
    return file
  }
  try {
    // imageOrientation: 'from-image' 按 EXIF 自动矫正手机照片方向
    const bitmap = await createImageBitmap(file, { imageOrientation: 'from-image' })
    const { width, height } = bitmap
    if (Math.max(width, height) <= IMAGE_MAX_EDGE && file.size < 200 * 1024) {
      bitmap.close()
      return file // 尺寸合格且小于 200KB，无需处理
    }
    const scale = Math.min(1, IMAGE_MAX_EDGE / Math.max(width, height))
    const w = Math.max(1, Math.round(width * scale))
    const h = Math.max(1, Math.round(height * scale))
    const canvas = document.createElement('canvas')
    canvas.width = w
    canvas.height = h
    const ctx = canvas.getContext('2d')
    ctx.drawImage(bitmap, 0, 0, w, h)
    bitmap.close()

    const outType = file.type === 'image/png' ? 'image/png' : file.type === 'image/webp' ? 'image/webp' : 'image/jpeg'
    const blob = await new Promise((resolve) => canvas.toBlob(resolve, outType, 0.85))
    if (!blob || blob.size >= file.size) {
      return file // 编码失败或压缩后更大，回退原图
    }
    const ext = outType === 'image/png' ? '.png' : outType === 'image/webp' ? '.webp' : '.jpg'
    const name = (file.name || 'image').replace(/\.[a-zA-Z0-9]+$/, '') + ext
    return new File([blob], name, { type: outType })
  } catch (e) {
    console.warn('图片压缩失败，按原图上传:', e)
    return file
  }
}

async function uploadImage(file) {
  const ed = props.editor
  if (!ed) return
  try {
    const processed = await compressImage(file)
    const formData = new FormData()
    formData.append('file', processed)

    const { data } = await api.post('/upload', formData, {
      headers: {
        'Content-Type': 'multipart/form-data'
      }
    })

    ed.chain().focus().insertContent(`<img src="${data.url}" alt="图片" />`).run()
  } catch (error) {
    // v21.107 P1-6：透传后端具体原因（存储配额满/格式不符/超大小限制等），
    // 此前一律「图片上传失败，请重试」用户无法自助解决
    message.errorOnce(error?.response?.data?.error || '图片上传失败，请重试')
    console.error('Image upload error:', error)
  }
}

  return {
    painterActive, togglePainter, clearFormat, insertLink,
    colorPopoverVisible, bgPopoverVisible, textColor, bgColor,
    isParagraph, blockLabel, applyBlock,
    inCodeBlock, fontLabel, sizeLabel, applyFont, applySize,
    alignIcon, applyAlign, indent, outdent, tableOp,
    setColor, setBgColor, insertTable,
    imageInput, triggerImageUpload, handleImageSelect
  }
}
