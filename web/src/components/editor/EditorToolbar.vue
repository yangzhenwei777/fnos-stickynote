<!--
  文件行数豁免登记（开发纪律 §5.1）
  ─────────────────────────────────────────────────────────────
  当前行数：1097（.vue 软上限 800，超出 37%）
  豁免原因：v21.121~123 分阶段合并的产物（撤销/重做/字号/颜色/列表/
           对齐/链接/表格/图片上传/粘贴等 10+ 类工具栏按钮的模板 +
           处理器 + scoped 样式），功能高度内聚在"编辑器工具栏"这一
           单一职责下。v21.125 刚完成 scoped 样式迁移事故修复（样式
           从 TiptapEditor.vue 完整迁出到本组件），短期内二次拆分
           风险大于收益。
  拆分计划：待下一次工具栏功能性变更（如新增按钮组）时同步拆分，候选
           按工具组切分为 ToolbarGroup* 子组件，主组件保留 editor 生
           命周期与 toolbarTick 广播。
  追踪：docs/技术债清单.md · TD-002
-->
<template>
  <div class="editor-toolbar">
    <!-- 撤销 / 恢复 -->
    <button
      class="tb-btn"
      title="撤销"
      :disabled="!editor.can().undo()"
      @click="editor.chain().focus().undo().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.undo"
      />
    </button>
    <button
      class="tb-btn"
      title="恢复"
      :disabled="!editor.can().redo()"
      @click="editor.chain().focus().redo().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.redo"
      />
    </button>

    <span class="tb-sep" />

    <!-- 格式刷 / 清除格式 -->
    <button
      class="tb-btn"
      :class="{ active: painterActive }"
      :title="painterActive ? '格式刷已开启：选中目标文字即套用（再次点击或按 Esc 取消）' : '格式刷：先复制选中文字的格式，再刷到其他文字'"
      @click="togglePainter"
    >
      <span
        class="tb-ic"
        v-html="IC.brush"
      />
    </button>
    <button
      class="tb-btn"
      title="清除格式"
      @click="clearFormat"
    >
      <span
        class="tb-ic"
        v-html="IC.eraser"
      />
    </button>

    <span class="tb-sep" />

    <!-- 样式 -->
    <el-dropdown
      trigger="click"
      popper-class="tb-toolbar-dd"
      @command="applyBlock"
    >
      <button
        class="tb-btn tb-select"
        title="段落样式"
      >
        <span class="tb-label">{{ blockLabel }}</span>
        <span
          class="tb-caret"
          v-html="IC.caret"
        />
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item command="paragraph">
            <span class="tb-check">{{ isParagraph ? '✓' : '' }}</span>正文
          </el-dropdown-item>
          <el-dropdown-item command="h1">
            <span class="tb-check">{{ editor.isActive('heading', { level: 1 }) ? '✓' : '' }}</span>标题 1
          </el-dropdown-item>
          <el-dropdown-item command="h2">
            <span class="tb-check">{{ editor.isActive('heading', { level: 2 }) ? '✓' : '' }}</span>标题 2
          </el-dropdown-item>
          <el-dropdown-item command="h3">
            <span class="tb-check">{{ editor.isActive('heading', { level: 3 }) ? '✓' : '' }}</span>标题 3
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>

    <!-- 字体（v21.220：代码块内禁用 + 提示，等宽锁定为既定设计） -->
    <el-dropdown
      trigger="click"
      popper-class="tb-toolbar-dd"
      :disabled="inCodeBlock"
      @command="applyFont"
    >
      <button
        class="tb-btn tb-select"
        :class="{ 'tb-disabled': inCodeBlock }"
        :title="inCodeBlock ? '代码块使用等宽字体，不支持更换' : '字体'"
      >
        <span class="tb-label">{{ fontLabel }}</span>
        <span
          class="tb-caret"
          v-html="IC.caret"
        />
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item
            v-for="f in FONTS"
            :key="f.value || 'default'"
            :command="f.value"
            :style="f.value ? { fontFamily: f.value } : {}"
          >
            <span class="tb-check">{{ currentFontFamily === f.value ? '✓' : '' }}</span>{{ f.label }}
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>

    <!-- 字号（v21.220：代码块内同样禁用，textStyle mark 进不了代码块） -->
    <el-dropdown
      trigger="click"
      popper-class="tb-toolbar-dd"
      :disabled="inCodeBlock"
      @command="applySize"
    >
      <button
        class="tb-btn tb-select tb-size-select"
        :class="{ 'tb-disabled': inCodeBlock }"
        :title="inCodeBlock ? '代码块内不支持调整字号' : '字号'"
      >
        <span class="tb-label">{{ sizeLabel }}</span>
        <span
          class="tb-caret"
          v-html="IC.caret"
        />
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item
            v-for="s in FONT_SIZES"
            :key="s || 'default'"
            :command="s"
          >
            <span class="tb-check">{{ currentFontSize === s ? '✓' : '' }}</span>{{ s || '默认字号' }}
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>

    <span class="tb-sep" />

    <!-- 粗体 / 斜体 / 下划线 / 删除线 -->
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('bold') }"
      title="粗体"
      @click="editor.chain().focus().toggleBold().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.bold"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('italic') }"
      title="斜体"
      @click="editor.chain().focus().toggleItalic().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.italic"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('underline') }"
      title="下划线"
      @click="editor.chain().focus().toggleUnderline().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.underline"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('strike') }"
      title="删除线"
      @click="editor.chain().focus().toggleStrike().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.strike"
      />
    </button>

    <!-- 文字颜色（保持既有取色器样式不变） -->
    <el-popover
      placement="bottom"
      :width="280"
      trigger="click"
      :visible="colorPopoverVisible"
      @hide="colorPopoverVisible = false"
    >
      <template #reference>
        <button
          class="color-btn"
          title="文字颜色"
          @click="colorPopoverVisible = !colorPopoverVisible"
        >
          <span
            class="color-btn-letter"
            :style="{ color: textColor || 'currentColor' }"
          >A</span>
          <span
            class="color-btn-bar"
            :style="{ background: textColor || '#000000' }"
          />
        </button>
      </template>
      <ColorPicker
        :model-value="textColor || '#000000'"
        @apply="(c) => { setColor(c); colorPopoverVisible = false }"
      />
    </el-popover>

    <!-- 背景颜色（参照为知笔记：与文字颜色同款取色器，高亮底色） -->
    <el-popover
      placement="bottom"
      :width="280"
      trigger="click"
      :visible="bgPopoverVisible"
      @hide="bgPopoverVisible = false"
    >
      <template #reference>
        <button
          class="color-btn"
          title="背景颜色"
          @click="bgPopoverVisible = !bgPopoverVisible"
        >
          <span
            class="tb-ic color-btn-ic"
            v-html="IC.highlight"
          />
          <span
            class="color-btn-bar"
            :style="{ background: bgColor || '#fde047' }"
          />
        </button>
      </template>
      <ColorPicker
        :model-value="bgColor || '#fde047'"
        @apply="(c) => { setBgColor(c); bgPopoverVisible = false }"
      />
    </el-popover>

    <span class="tb-sep" />

    <!-- 列表 / 插入 -->
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('bulletList') }"
      title="项目符号"
      @click="editor.chain().focus().toggleBulletList().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.bulletList"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('orderedList') }"
      title="编号列表"
      @click="editor.chain().focus().toggleOrderedList().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.orderedList"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('taskList') }"
      title="插入清单"
      @click="editor.chain().focus().toggleTaskList().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.taskList"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('table') }"
      title="插入表格"
      @click="insertTable"
    >
      <span
        class="tb-ic"
        v-html="IC.table"
      />
    </button>
    <button
      class="tb-btn"
      title="插入图片"
      @click="triggerImageUpload"
    >
      <span
        class="tb-ic"
        v-html="IC.image"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('blockquote') }"
      title="插入引用"
      @click="editor.chain().focus().toggleBlockquote().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.quote"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('link') }"
      title="插入链接"
      @click="insertLink"
    >
      <span
        class="tb-ic"
        v-html="IC.link"
      />
    </button>
    <button
      class="tb-btn"
      :class="{ active: editor.isActive('codeBlock') }"
      title="插入代码"
      @click="editor.chain().focus().toggleCodeBlock().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.code"
      />
    </button>
    <button
      class="tb-btn"
      title="插入分割线"
      @click="editor.chain().focus().setHorizontalRule().run()"
    >
      <span
        class="tb-ic"
        v-html="IC.hr"
      />
    </button>

    <span class="tb-sep" />

    <!-- 缩进 -->
    <button
      class="tb-btn"
      title="增加缩进"
      @click="indent"
    >
      <span
        class="tb-ic"
        v-html="IC.indentIn"
      />
    </button>
    <button
      class="tb-btn"
      title="减少缩进"
      @click="outdent"
    >
      <span
        class="tb-ic"
        v-html="IC.indentOut"
      />
    </button>

    <!-- 对齐（下拉） -->
    <el-dropdown
      trigger="click"
      popper-class="tb-toolbar-dd"
      @command="applyAlign"
    >
      <button
        class="tb-btn"
        title="对齐方式"
      >
        <span
          class="tb-ic"
          v-html="alignIcon"
        />
        <span
          class="tb-caret"
          v-html="IC.caret"
        />
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item command="left">
            <span class="tb-check">{{ editor.isActive({ textAlign: 'left' }) ? '✓' : '' }}</span>左对齐
          </el-dropdown-item>
          <el-dropdown-item command="center">
            <span class="tb-check">{{ editor.isActive({ textAlign: 'center' }) ? '✓' : '' }}</span>居中对齐
          </el-dropdown-item>
          <el-dropdown-item command="right">
            <span class="tb-check">{{ editor.isActive({ textAlign: 'right' }) ? '✓' : '' }}</span>右对齐
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>

    <!-- 表格操作：仅选中表格时显示（与既有展示逻辑一致，收纳为下拉） -->
    <template v-if="editor.isActive('table')">
      <span class="tb-sep" />
      <el-dropdown
        trigger="click"
        placement="bottom-end"
        popper-class="tb-toolbar-dd"
        @command="tableOp"
      >
        <button
          class="tb-btn tb-select"
          title="表格操作"
        >
          <span
            class="tb-ic"
            v-html="IC.tableOps"
          />
          <span class="tb-label">表格</span>
          <span
            class="tb-caret"
            v-html="IC.caret"
          />
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="rowBefore">
              上方插入行
            </el-dropdown-item>
            <el-dropdown-item command="rowAfter">
              下方插入行
            </el-dropdown-item>
            <el-dropdown-item command="colBefore">
              左侧插入列
            </el-dropdown-item>
            <el-dropdown-item command="colAfter">
              右侧插入列
            </el-dropdown-item>
            <el-dropdown-item
              command="deleteRow"
              divided
            >
              删除本行
            </el-dropdown-item>
            <el-dropdown-item command="deleteCol">
              删除本列
            </el-dropdown-item>
            <el-dropdown-item
              command="deleteTable"
              class="danger-item"
              divided
            >
              删除表格
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </template>

    <!-- B4-4c 阶段B-3：图片上传隐藏 file input（从 TiptapEditor.vue 迁入子组件） -->
    <input
      ref="imageInput"
      type="file"
      accept="image/*"
      style="display:none"
      @change="handleImageSelect"
    >
  </div>
</template>

<script setup>
// 评审 B4-4c：工具栏呈现层与「纯 editor 命令」处理器由 TiptapEditor.vue 外提至此。
// 拆分纪律：行为逐字节不变；editor 实例与工具栏重渲染 tick 由父组件传入（editor 非响应式，
// 事务发生后父组件递增 toolbarTick → 本组件 computed 依赖 props.tick 失效 → 重渲染 → 模板内
// isActive/can 重算）。B4-4c 阶段B：格式刷状态机、插入链接、图片上传（文件 I/O + 粘贴兜底）
// 均已内聚进本组件（自订阅 editor selectionUpdate/blur + view.dom Esc/paste）。
// v2.2.1（用户反馈 2b）：Markdown 文本粘贴转换迁至 TiptapEditor 的
// editorProps.handlePaste（官方接管点，防双插），本组件仅保留图片上传兜底。
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'
import ColorPicker from '../ColorPicker.vue'
import { message } from '@/utils/message'
import api from '@/api/request'
import { IC } from './icons'
import { FONTS, FONT_SIZES, IMAGE_MAX_EDGE, IMAGE_SKIP_TYPES } from './constants'
import { isMarkdownPaste } from './pasteMarkdown'

const props = defineProps({
  editor: {
    type: Object,
    required: true
  },
  // 工具栏状态响应式信号：父组件 onTransaction 中递增，驱动本组件重算高亮/禁用态。
  tick: {
    type: Number,
    default: 0
  }
})

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
</script>

<style scoped>
/* v21.82 工具栏重设计：分组 + 分隔符，按钮统一线性 SVG 图标（参照为知笔记） */
.editor-toolbar {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 8px 12px;
  /* 白底 + 上下分割线——标题/工具栏/内容同底色，由两条 1px 分割线分层 */
  border-top: 1px solid var(--border-color);
  border-bottom: 1px solid var(--border-color);
  background: var(--bg-primary);
  flex-wrap: wrap;
  row-gap: 4px;
}

.tb-btn {
  height: 30px;
  min-width: 30px;
  padding: 0 4px;
  border: none;
  background: transparent;
  border-radius: var(--radius-sm);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  color: var(--text-secondary);
  font-size: 13px;
  /* v21.85 清扫：transition: all → 仅过渡实际变化属性（高频重渲染下减少样式计算） */
  transition: background-color 0.15s, color 0.15s;
  flex-shrink: 0;
}

.tb-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.tb-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
  background: transparent;
}

/* v21.220：代码块内字体/字号禁用态（el-dropdown disabled 不穿透到自定义 trigger 按钮，
   视觉禁用与提示 title 由组件自绑；语义同 .tb-btn:disabled） */
.tb-btn.tb-disabled {
  opacity: 0.35;
  cursor: not-allowed;
  background: transparent;
}
.tb-btn.tb-disabled:hover {
  background: transparent;
  color: var(--text-secondary);
}

.tb-btn.active {
  /* v21.207：激活态由「实心蓝底白字」改为「浅蓝底深蓝字」，视觉重量更轻 */
  background: var(--primary-tint);
  color: var(--primary-ink);
}

.tb-ic {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 17px;
  height: 17px;
  flex-shrink: 0;
}

.tb-ic :deep(svg) {
  width: 17px;
  height: 17px;
}

.tb-select {
  padding: 0 8px;
  gap: 4px;
}

.tb-size-select {
  min-width: 56px;
}

.tb-label {
  max-width: 96px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tb-caret {
  display: inline-flex;
  width: 12px;
  height: 12px;
  opacity: 0.6;
}

.tb-caret :deep(svg) {
  width: 12px;
  height: 12px;
}

.tb-check {
  display: inline-block;
  width: 16px;
  color: var(--primary-color);
  font-weight: 600;
}

/* 分组分隔符 */
.tb-sep {
  width: 1px;
  height: 18px;
  background: var(--border-color);
  margin: 0 6px;
  flex-shrink: 0;
}

/* v21.85：工具栏下拉（段落样式/字体/字号/对齐/表格操作）开合动画由 EP 默认
   ~0.3s 缩短为 0.12s——下拉动画是字号/字体操作"迟钝感"的主要视觉来源。
   弹层 teleport 到 body，须 :global；仅命中带 tb-toolbar-dd popper-class 的下拉 */
:global(.tb-toolbar-dd.el-zoom-in-top-enter-active),
:global(.tb-toolbar-dd.el-zoom-in-top-leave-active) {
  transition-duration: 0.12s;
}

/* 颜色按钮：字母/图标 + 底部颜色条（文字颜色保持既有取色样式） */
.color-btn {
  position: relative;
  width: 30px;
  height: 30px;
  border: none;
  background: transparent;
  border-radius: var(--radius-sm);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-secondary);
  transition: background-color 0.15s; /* v21.85 清扫：原 transition: all */
  padding: 0;
  flex-shrink: 0;
}

.color-btn:hover {
  background: var(--border-light);
}

.color-btn-letter {
  font-size: 14px;
  font-weight: 600;
  line-height: 1;
}

.color-btn-ic {
  width: 16px;
  height: 16px;
}

.color-btn-ic :deep(svg) {
  width: 16px;
  height: 16px;
}

.color-btn-bar {
  position: absolute;
  bottom: 3px;
  left: 50%;
  transform: translateX(-50%);
  width: 16px;
  height: 3px;
  border-radius: var(--radius-xs);
  border: 1px solid var(--border-light, #d1d5db);
}

/* 窄屏：工具栏不换行，横向滚动（v21.125 随组件自 TiptapEditor 迁入） */
@media (max-width: 640px) {
  .editor-toolbar {
    flex-wrap: nowrap;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  }
}
</style>
