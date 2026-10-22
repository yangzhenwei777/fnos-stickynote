<template>
  <div
    v-if="editor"
    class="tiptap-editor"
  >
    <EditorToolbar
      :editor="editor"
      :tick="toolbarTick"
    />

    <editor-content
      class="editor-content"
      :editor="editor"
    />
  </div>
</template>

<script setup>
// 纪律 5.1（v21.85 豁免；评审 B4 拆分进行中）：本文件工具栏呈现层已外提，行数进一步下降。
// 已外提（纯定义、零组件实例依赖）→ editor/ 目录：
//   icons.js：svg/IC；constants.js：FONTS、FONT_SIZES、EMIT_DEBOUNCE、EMIT_MAX_WAIT、IMAGE_MAX_EDGE、IMAGE_SKIP_TYPES；
//   extensions.js：FontFamily、FontSize、ResizableImage、IndentParagraph(+changeIndent)、StickyTaskItem(+isTouchDevice)。
// 剩余组件耦合部分（引用 editor.value 与组件本地 ref：toolbarTick、scheduleEmit）：
//   ① ✅ 工具栏模板 + 纯命令处理器 → editor/EditorToolbar.vue（B4-4c 阶段A：呈现层/命令按钮迁入；
//        阶段B：格式刷状态机 + 插入链接 + 图片上传/粘贴（含 Markdown）全部内聚进子组件，
//        子组件自订阅 editor selectionUpdate/blur + view.dom Esc/paste，本组件不再持有 painter/上传逻辑）；
//   ② useEditor 生命周期胶水 + 内容回流（scheduleEmit/flushContent）+ toolbarTick 驱动留本组件；
//   ③ 与 NotesView 的弹框组件化联动（另立项）。
import { useEditor, EditorContent } from '@tiptap/vue-3'
import { watch, ref } from 'vue'
import StarterKit from '@tiptap/starter-kit'
// v21.109 tiptap 3.x 迁移：Placeholder 并入 @tiptap/extensions（命名导入；本项目仅用 placeholder 文本，
// 被移除的 considerAnyAsEmpty 选项未使用）
import { Placeholder } from '@tiptap/extensions'
import Color from '@tiptap/extension-color'
// v21.109 tiptap 3.x：extension-text-style 改为命名导出（v3 无 default；新增 TextStyleKit 可选，本项目沿用单包）
import { TextStyle } from '@tiptap/extension-text-style'
import Underline from '@tiptap/extension-underline'
import Link from '@tiptap/extension-link'
import TextAlign from '@tiptap/extension-text-align'
import Highlight from '@tiptap/extension-highlight'
// v21.109 tiptap 3.x 迁移：task-list / task-item 并入 @tiptap/extension-list（命名导入）
import { TaskList } from '@tiptap/extension-list'
import CodeBlockLowlight from '@tiptap/extension-code-block-lowlight'
// v21.220：代码块复制按钮（widget decoration，不动 schema/渲染，见 codeBlockCopy.js 头注）
import { CodeBlockCopy } from './editor/codeBlockCopy'
// v21.109 tiptap 3.x 迁移：table 及 row/header/cell 并入 @tiptap/extension-table（命名导入）
import { Table, TableRow, TableHeader, TableCell } from '@tiptap/extension-table'
import { lowlight } from 'lowlight/lib/common.js'
import EditorToolbar from './editor/EditorToolbar.vue'
import { EMIT_DEBOUNCE, EMIT_MAX_WAIT } from './editor/constants'
import { FontFamily, FontSize, ResizableImage, IndentParagraph, StickyTaskItem } from './editor/extensions'

const props = defineProps({
  modelValue: {
    type: String,
    default: ''
  }
})

const emit = defineEmits(['update:modelValue'])

// ---- 工具栏线性图标（Feather/Lucide 风格，参照为知笔记扁平线框设计）----

// 字体 / 字号选项

// 段落缩进扩展：data-indent(0~5) → margin-left 步进 2em

// ---- v21.85 性能优化：去除"每事务全量序列化 + v-model 回流"热路径 ----
// 旧实现每次事务都 editor.getHTML() 全量序列化并 emit，父组件更新 currentNote.content
// 又经 props 回流触发本组件重渲染 + watch 内二次 getHTML() 比对，大笔记下单次格式操作
// 100~600ms。新实现：① 内容真相留在 ProseMirror，emit 防抖合并；② watch 用
// lastEmittedHtml 字符串比对，不再二次 getHTML()；③ 工具栏高亮由 onTransaction 驱动
// 轻量 toolbarTick，不再依赖父组件重渲染；④ 关键路径经 flushContent() 立即同步。
let lastEmittedHtml = props.modelValue
let emitTimer = null
let firstDirtyAt = 0

function flushContent() {
  const ed = editor.value
  if (!ed) return ''
  const html = ed.getHTML()
  if (html !== lastEmittedHtml) {
    lastEmittedHtml = html
    emit('update:modelValue', html)
  }
  return html
}

function scheduleEmit() {
  const now = Date.now()
  if (!firstDirtyAt) firstDirtyAt = now
  if (emitTimer) clearTimeout(emitTimer)
  // 连续输入不逐键序列化；停顿 250ms 或最长 2000ms 必发一次
  const delay = now - firstDirtyAt >= EMIT_MAX_WAIT ? 0 : EMIT_DEBOUNCE
  emitTimer = setTimeout(() => {
    emitTimer = null
    firstDirtyAt = 0
    flushContent()
  }, delay)
}

function cancelPendingEmit() {
  if (emitTimer) { clearTimeout(emitTimer); emitTimer = null }
  firstDirtyAt = 0
}

// 工具栏状态响应式驱动：editor 实例非响应式，事务发生后递增本 tick。
// B4-4c 拆分后本 tick 经 :tick 传入 editor/EditorToolbar.vue，其内部 computed 依赖
// props.tick → 子组件重渲染 → 工具栏 isActive/can 重算（沿用旧版「光标移入粗体区即点亮」修复）。
const toolbarTick = ref(0)
let toolbarSig = ''

// 工具栏可见状态签名：全部高亮/禁用态相关探针的轻量拼接。
// 探针均为选区级遍历（廉价），整体远小于一次工具栏重渲染的成本。
function computeToolbarSig() {
  const ed = editor.value
  if (!ed) return ''
  const parts = []
  const names = ['bold', 'italic', 'underline', 'strike', 'bulletList', 'orderedList', 'taskList', 'blockquote', 'codeBlock', 'link', 'table']
  for (const n of names) parts.push(ed.isActive(n) ? 1 : 0)
  parts.push(ed.isActive('heading', { level: 1 }) ? 1 : 0)
  parts.push(ed.isActive('heading', { level: 2 }) ? 1 : 0)
  parts.push(ed.isActive('heading', { level: 3 }) ? 1 : 0)
  parts.push(ed.isActive({ textAlign: 'center' }) ? 1 : 0)
  parts.push(ed.isActive({ textAlign: 'right' }) ? 1 : 0)
  const ts = ed.getAttributes('textStyle')
  parts.push(ts.color || '', ts.fontFamily || '', ts.fontSize || '')
  parts.push(ed.getAttributes('highlight').color || '')
  parts.push(ed.can().undo() ? 1 : 0)
  parts.push(ed.can().redo() ? 1 : 0)
  return parts.join('|')
}

// ---- v21.113：自定义 TaskItem——修复移动端勾选清单后自动弹软键盘并跳页顶 ----
// 根因：官方 @tiptap/extension-list 3.31.3 TaskItem 的 checkbox change 处理器
// 显式调用 editor.chain().focus()（dist/task-item/index.js:259）。触屏设备上
// 聚焦 contenteditable → 弹出软键盘，且浏览器把视口滚动到编辑器初始选区
// （文档开头）→ 页面跳顶。docmost 项目同源缺陷（issue #2225）同修法。
// 本覆写与官方 nodeView 逐行同构（schema/parseHTML/renderHTML/快捷键完全
// 继承官方），唯一行为差异：触屏设备勾选时跳过 focus()，桌面端不变。
// ⚠️ 升级 Tiptap 时须对照官方 task-item nodeView 复核本覆写（交付说明挂账）。

const editor = useEditor({
  extensions: [
    // v21.109 tiptap 3.x：StarterKit 默认内置 Link/Underline/ListKeymap——本项目对
    // Link（openOnClick/HTMLAttributes）与 Underline（保持独立配置位）单独 configure，
    // 故在 StarterKit 内禁用对应版本避免双实例；paragraph:false 沿用（IndentParagraph 接管）。
    StarterKit.configure({ paragraph: false, link: false, underline: false }),
    IndentParagraph,
    Placeholder.configure({
      placeholder: '开始写作...'
    }),
    Color,
    TextStyle,
    FontFamily,
    FontSize,
    Underline,
    Link.configure({
      openOnClick: true,
      // 评审 P2：编辑态点击即生效，须在此校验 href，拒绝 javascript:/data:/vbscript: 等危险协议
      validate: (href) => /^(?:https?:|mailto:|tel:|\/|#)/i.test((href || '').trim()),
      HTMLAttributes: { target: '_blank', rel: 'noopener noreferrer' }
    }),
    TextAlign.configure({
      types: ['heading', 'paragraph']
    }),
    Highlight.configure({ multicolor: true }),
    TaskList,
    // v21.113：StickyTaskItem = 官方 TaskItem 覆写（触屏勾选跳过 focus，修复移动端弹键盘/跳顶）
    StickyTaskItem.configure({ nested: true }),
    ResizableImage.configure({
      inline: false,
      allowBase64: true
    }),
    CodeBlockLowlight.configure({
      lowlight
    }),
    // v21.220：代码块右上角复制按钮（编辑态；预览态由 NotesView 调 attachCodeCopyButtons）
    CodeBlockCopy,
    Table.configure({
      resizable: true
    }),
    TableRow,
    TableHeader,
    TableCell
  ],
  content: props.modelValue,
  onUpdate: () => {
    // v21.85：不再每事务全量 getHTML() 回流，防抖合并后统一发出
    scheduleEmit()
  },
  onTransaction: () => {
    // v21.85：工具栏高亮改由事务驱动（含纯选区变化），不再依赖 v-model 回流。
    // 仅当工具栏可见状态签名实际变化时才触发重渲染——连续输入/拖选时
    // 签名稳定 → 几乎零重渲染；单次格式操作即时刷新（无帧延迟）
    const sig = computeToolbarSig()
    if (sig !== toolbarSig) {
      toolbarSig = sig
      toolbarTick.value++
    }
  },
  // 注：粘贴监听（图片上传 + Markdown 转换）与格式刷 Esc/selectionUpdate/blur 已随 B4-4c 阶段B
  //     迁入 EditorToolbar.vue 自订阅（onMounted 注册、onBeforeUnmount 解绑）。
  onDestroy: () => {
    // v21.85：卸载前把防抖窗口内的未发内容同步给父组件，防止编辑丢失
    flushContent()
    cancelPendingEmit()
  }
})

// v21.85：flushContent 供父组件在保存/切换/预览等关键路径强制同步内容；
// editor 暴露供组件测试断言（生产代码不应直接使用）
defineExpose({ flushContent, editor })

watch(() => props.modelValue, (newValue) => {
  const ed = editor.value
  if (!ed) return
  // v21.85：与上次已发出内容比对即可识别"自身 emit 的回声"，
  // 不再调 editor.getHTML() 二次序列化；仅外部变更（草稿恢复/解密/切换笔记）才 setContent
  if (newValue === lastEmittedHtml) return
  lastEmittedHtml = newValue
  // v21.109 tiptap 3.x：v3 的 setContent 默认触发 update（v2 默认不触发）——
  // 必须显式 emitUpdate:false，否则外部 set 会回声触发 onUpdate→scheduleEmit→dirty 误置。
  ed.commands.setContent(newValue, { emitUpdate: false })
})

</script>

<style scoped>
.tiptap-editor {
  display: flex;
  flex-direction: column;
  height: 100%;
}

/* v21.125：工具栏（含颜色按钮/下拉弹层动画）样式已随组件迁入 EditorToolbar.vue 自有 scoped，
   修复 v21.121~123 拆分后父组件 scoped 样式对子组件内部元素失效导致的工具栏损毁 */

.editor-content {
  flex: 1;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  padding: 24px 32px;
}

.editor-content :deep(.ProseMirror) {
  outline: none;
  min-height: 100%;
  /* v21.179：移除 v21.207 引入的正文限宽（max-width:720px）——与预览侧口径统一。
     v21.169 修「宽屏正文只占左半」时只移除了预览侧（notes-view.css），编辑侧漏改，
     用户 218 内测实机复现（编辑态右侧大片留白）。本项目是笔记应用而非阅读器，
     内容区应占满可用宽度 —— 勿再对正文引入 max-width。 */
}

.editor-content :deep(.ProseMirror p.is-editor-empty:first-child::before) {
  color: var(--text-muted);
  float: left;
  height: 0;
  pointer-events: none;
  content: attr(data-placeholder);
}

/* v20.1 代码块语法高亮（GitHub 风格）
   v21.220：position:relative 供 .code-copy-btn（复制按钮）absolute 钉右上角 */
.editor-content :deep(pre) {
  position: relative;
  background: var(--bg-secondary);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  padding: 16px;
  overflow-x: auto;
  font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 13px;
  line-height: 1.5;
}
.editor-content :deep(pre code) {
  background: none;
  padding: 0;
  color: var(--text-primary);
}
html[data-theme='dark'] .editor-content :deep(pre) {
  background: #1e1e2e;
}
html[data-theme='dark'] .editor-content :deep(pre code) {
  color: #cdd6f4;
}

/* v20.1 表格样式 */
.editor-content :deep(table) {
  border-collapse: collapse;
  margin: 12px 0;
  width: 100%;
}
.editor-content :deep(th),
.editor-content :deep(td) {
  border: 1px solid var(--border-color);
  padding: 8px 12px;
  text-align: left;
  min-width: 60px;
}
.editor-content :deep(th) {
  background: var(--bg-secondary);
  font-weight: 600;
}
.editor-content :deep(.selectedCell) {
  background: var(--primary-color);
  opacity: 0.1;
}

.editor-content :deep(.ProseMirror p) {
  margin: 0.5em 0;
  line-height: 1.7;
}

.editor-content :deep(.ProseMirror h1) {
  font-size: 2em;
  margin: 0.67em 0;
  font-weight: 600;
}

.editor-content :deep(.ProseMirror h2) {
  font-size: 1.5em;
  margin: 0.83em 0;
  font-weight: 600;
}

.editor-content :deep(.ProseMirror h3) {
  font-size: 1.17em;
  margin: 1em 0;
  font-weight: 600;
}

.editor-content :deep(.ProseMirror ul),
.editor-content :deep(.ProseMirror ol) {
  padding-left: 1.5em;
  margin: 0.5em 0;
}

.editor-content :deep(.ProseMirror li) {
  margin: 0.25em 0;
}

/* v21.82 任务清单（插入清单） */
.editor-content :deep(ul[data-type='taskList']) {
  list-style: none;
  padding-left: 0.4em;
}
.editor-content :deep(ul[data-type='taskList'] li) {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 0.3em 0;
}
/* v21.83 对齐修复：label 高度=首行行高并内部居中，
   复选框中线与首行文字中线重合（旧写死 margin-top:3px 视觉偏上） */
.editor-content :deep(ul[data-type='taskList'] li > label) {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  height: 1.7em;
  user-select: none;
}
.editor-content :deep(ul[data-type='taskList'] li > div) {
  flex: 1 1 auto;
}
.editor-content :deep(ul[data-type='taskList'] li > div > p:first-child) {
  margin-top: 0;
}
.editor-content :deep(ul[data-type='taskList'] li > div > p:last-child) {
  margin-bottom: 0;
}
.editor-content :deep(ul[data-type='taskList'] input[type='checkbox']) {
  width: 16px;
  height: 16px;
  margin: 0;
  accent-color: var(--primary-color);
  cursor: pointer;
}

/* v21.82 背景颜色（highlight） */
.editor-content :deep(mark) {
  border-radius: var(--radius-xs);
  padding: 0 2px;
}

.editor-content :deep(.ProseMirror blockquote) {
  /* v21.207：与预览一致——2px 蓝色左条，无底色块 */
  border-left: 2px solid var(--primary-color);
  margin: 1em 0;
  color: var(--text-secondary);
  padding: 2px 0 2px 14px;
}

.editor-content :deep(.ProseMirror hr) {
  border: none;
  border-top: 1px solid var(--border-color);
  margin: 2em 0;
}

.editor-content :deep(.ProseMirror a) {
  color: inherit;
  text-decoration: underline;
  text-decoration-color: currentColor;
}
/* 彩色链接：下划线跟随内层文字元素的真实文字色（a 外层时其 currentColor 只是主题前景） */
.editor-content :deep(.ProseMirror a > *) {
  text-decoration: underline;
  text-decoration-color: currentColor;
}

.editor-content :deep(.ProseMirror img) {
  max-width: 100%;
  max-height: 90vh;
  height: auto;
  display: block;
  border-radius: var(--radius-lg);
  object-fit: contain;
}

/* 图片缩放手柄：右下角圆点，hover 或选中图片时显示 */
/* width: fit-content 让容器收缩贴合图片宽度，手柄跟随图片右缘（v16.6 修复：原为 block 撑满整行导致手柄跑到编辑栏最右边） */
.editor-content :deep(.image-resizer) {
  position: relative;
  display: block;
  width: fit-content;
  max-width: 100%;
}

.editor-content :deep(.image-resize-handle) {
  position: absolute;
  right: -6px;
  bottom: -6px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--primary-color);
  border: 2px solid #ffffff;
  cursor: nwse-resize;
  opacity: 0;
  transition: opacity 0.15s ease;
  z-index: 10;
  touch-action: none;
}

.editor-content :deep(.image-resizer:hover) .image-resize-handle,
.editor-content :deep(.image-resizer.ProseMirror-selectednode) .image-resize-handle {
  opacity: 0.95;
}

/* 窄屏：工具栏不换行，横向滚动 */
@media (max-width: 640px) {
  .editor-content {
    padding: 16px;
  }

  .editor-content :deep(.image-resize-handle) {
    width: 20px;
    height: 20px;
    right: -8px;
    bottom: -8px;
    opacity: 0.95;
  }
}
</style>
