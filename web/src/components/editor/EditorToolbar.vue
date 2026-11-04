<!--
  文件行数豁免登记（开发纪律 §5.1）
  ─────────────────────────────────────────────────────────────
  当前行数：以机器门禁实测为准（2.3.2 基线实测 1097；本注释不再手写行数——见
           docs/技术债清单.md 2026-11-03 修正条）
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
// 2026-11-03 巨型 Vue 拆分 刀5：全部工具栏逻辑再抽至 useToolbarController.js
// （script 体机械搬移，零行为变化）；本文件保留模板 + scoped 样式 + 模板直接引用的
// IC/FONTS/FONT_SIZES/ColorPicker。editor 实例与工具栏重渲染 tick 仍由父组件传入
// （editor 非响应式，事务后父组件递增 toolbarTick → computeds 依赖 tick 失效重算）。
import ColorPicker from '../ColorPicker.vue'
import { IC } from './icons'
import { FONTS, FONT_SIZES } from './constants'
import { useToolbarController } from './useToolbarController'

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

const {
  painterActive, togglePainter, clearFormat, insertLink,
  colorPopoverVisible, bgPopoverVisible, textColor, bgColor,
  isParagraph, blockLabel, applyBlock,
  inCodeBlock, fontLabel, sizeLabel, applyFont, applySize,
  alignIcon, applyAlign, indent, outdent, tableOp,
  setColor, setBgColor, insertTable,
  imageInput, triggerImageUpload, handleImageSelect
} = useToolbarController(props)
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
