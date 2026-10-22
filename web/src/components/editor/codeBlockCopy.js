// v21.220 代码块复制按钮（用户 219 内测反馈需求 ②，D3 拍板：编辑态 + 预览态全覆盖）
//
// 两态实现、一套工厂：
//   编辑态：CodeBlockCopy（Tiptap Extension）——widget decoration 插件，与 lowlight 语法高亮
//           完全同机制（Plugin state 管理 DecorationSet），不动 CodeBlock schema、不碰默认
//           nodeView 渲染、不破坏高亮。按钮插在每个 codeBlock 内容开头（pos+1），CSS absolute
//           定位到 pre 右上角（pre 加 position:relative）。
//   预览态：attachCodeCopyButtons(container)——v-html 渲染后为容器内每个 pre 挂同一款按钮
//           （.view-content 查看态 + .note-preview-content 编辑内预览，NotesView 负责调用时机）。
//
// 关键设计：
//   ① 复制内容实时取——编辑态经 view.posAtDOM(btn,0) 反查按钮所在 codeBlock 节点的当前
//      textContent（而非闭包捕获 node），widget DOM 可跨 DecorationSet 重建稳定复用，
//      编辑代码后复制永远是最新内容；预览态 clone pre 去掉按钮节点再取 text（防"已复制"
//      提示文本混入代码）。
//   ② mousedown preventDefault——编辑态防编辑器抢焦点/光标跳到按钮处；预览态无副作用。
//   ③ navigator.clipboard 需安全上下文（https / localhost / 桌面 WebView2 均满足），
//      局域网 http（如 192.0.2.10:13389）下不存在 → 必须 execCommand 兜底。
import { Extension } from '@tiptap/core'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { Decoration, DecorationSet } from '@tiptap/pm/view'

// ---- 图标（Lucide 线性风格，与工具栏一致；currentColor 随按钮色浅/深） ----
const COPY_ICON =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ' +
  'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>' +
  '<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>'
const CHECK_ICON =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" ' +
  'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<polyline points="20 6 9 17 4 12"></polyline></svg>'

// ---- 复制：clipboard API 优先，execCommand 兜底（局域网 http 非安全上下文） ----

// 传统兜底：临时 textarea 选中 + execCommand('copy')。返回是否成功。
export function legacyCopyText(text) {
  const ta = document.createElement('textarea')
  ta.value = text
  // fixed + 移出视口：不触发布局跳动/滚动；opacity 0 但不能 display:none（无法选中）
  ta.style.position = 'fixed'
  ta.style.top = '-9999px'
  ta.style.left = '-9999px'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.focus()
  ta.select()
  let ok
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  } finally {
    ta.remove()
  }
  return ok
}

// 统一入口：返回 Promise<boolean>（最终是否复制成功）
export function copyCodeText(text) {
  if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    return navigator.clipboard.writeText(text).then(
      () => true,
      () => legacyCopyText(text)
    )
  }
  return Promise.resolve(legacyCopyText(text))
}

// ---- 按钮工厂（两态共用） ----

// 已复制反馈：data-copied=1 经 CSS 切换 图标↔对勾，1.5s 后还原
function flashCopied(btn) {
  btn.dataset.copied = '1'
  clearTimeout(btn._copyFlashTimer)
  btn._copyFlashTimer = setTimeout(() => {
    btn.dataset.copied = ''
  }, 1500)
}

// getText: 点击时调用，返回要复制的文本（由调用方保证实时性）
export function createCodeCopyButton(getText) {
  const btn = document.createElement('button')
  btn.type = 'button'
  btn.className = 'code-copy-btn'
  btn.contentEditable = 'false' // 编辑态：不参与编辑
  btn.draggable = false
  btn.setAttribute('aria-label', '复制代码')
  btn.setAttribute('title', '复制代码')
  btn.dataset.copied = ''
  btn.innerHTML =
    '<span class="code-copy-ic">' + COPY_ICON + '</span>' +
    '<span class="code-copy-ok">' + CHECK_ICON + '</span>'
  // 拦截按下：防编辑器把光标移到按钮处/抢走选区焦点
  btn.addEventListener('mousedown', (e) => e.preventDefault())
  btn.addEventListener('click', (e) => {
    e.preventDefault()
    e.stopPropagation()
    copyCodeText(getText()).then((ok) => {
      if (ok) flashCopied(btn)
    })
  })
  return btn
}

// ---- 预览态：为容器内每个 pre 挂按钮（幂等，可重复调用） ----
export function attachCodeCopyButtons(container) {
  if (!container || !container.querySelectorAll) return 0
  let n = 0
  container.querySelectorAll('pre').forEach((pre) => {
    if (pre.querySelector(':scope > .code-copy-btn')) return
    pre.classList.add('has-code-copy')
    const btn = createCodeCopyButton(() => {
      // clone 后剔除按钮再取文本：避免"已复制"等按钮内文本混入代码
      const clone = pre.cloneNode(true)
      clone.querySelectorAll('.code-copy-btn').forEach((el) => el.remove())
      return clone.textContent || ''
    })
    pre.appendChild(btn)
    n++
  })
  return n
}

// ---- 编辑态：Tiptap Extension（widget decoration，照 lowlight 插件同模式） ----

// 点击时经 view 反查按钮所在 codeBlock 的实时文本（编辑后复制永远最新）
function readCodeBlockTextFromView(view, btn) {
  try {
    const pos = view.posAtDOM(btn, 0)
    const $pos = view.state.doc.resolve(pos)
    for (let d = $pos.depth; d > 0; d--) {
      const node = $pos.node(d)
      if (node.type.name === 'codeBlock') return node.textContent || ''
    }
  } catch {
    // 视图销毁竞态等极端情况：静默失败（按钮已随 DOM 卸载）
  }
  return ''
}

const codeCopyKey = new PluginKey('codeBlockCopy')

function buildCodeCopyDecorations(doc, view) {
  const decorations = []
  doc.descendants((node, pos) => {
    if (node.type.name !== 'codeBlock') return
    // widget 置于代码块内容开头（pos+1），CSS absolute 钉到 pre 右上角；
    // side:-1 使其排在首行文本之前，不干扰选区语义；
    // key 稳定 → 编辑相邻内容时 widget DOM 复用不闪烁，复制内容经 posAtDOM 实时反查不受影响
    decorations.push(
      Decoration.widget(
        pos + 1,
        () => {
          const btn = createCodeCopyButton(() => readCodeBlockTextFromView(view, btn))
          return btn
        },
        { side: -1, key: 'codeblock-copy' }
      )
    )
  })
  return DecorationSet.create(doc, decorations)
}

export const CodeBlockCopy = Extension.create({
  name: 'codeBlockCopy',
  addProseMirrorPlugins() {
    const view = this.editor.view
    return [
      new Plugin({
        key: codeCopyKey,
        state: {
          init: (_, { doc }) => buildCodeCopyDecorations(doc, view),
          apply: (tr, old) => {
            // 纯选区变化不重建：按钮"已复制"反馈不被打断；doc 变化全量重建（代码块增删/位置迁移）
            if (!tr.docChanged) return old
            return buildCodeCopyDecorations(tr.doc, view)
          }
        },
        props: {
          decorations(state) {
            return codeCopyKey.getState(state)
          }
        }
      })
    ]
  }
})
