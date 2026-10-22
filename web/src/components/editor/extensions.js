import { Extension, getRenderedAttributes } from '@tiptap/core'
import Image from '@tiptap/extension-image'
import Paragraph from '@tiptap/extension-paragraph'
import { TaskItem } from '@tiptap/extension-list'
import { toDisplayUrl } from '@/utils/uploadUrl'

// 编辑器自定义扩展（评审 B4 从组件外提）。此文件仅承载“纯定义”扩展：
// 不引用组件作用域的 props/emit/ref/api/message，可安全独立。

// 字体扩展：以全局属性方式挂到 textStyle（零新增依赖，与官方 extension-font-family 同思路）
export const FontFamily = Extension.create({
  name: 'fontFamily',
  addGlobalAttributes() {
    return [{
      types: ['textStyle'],
      attributes: {
        fontFamily: {
          default: null,
          parseHTML: (el) => el.style.fontFamily || null,
          renderHTML: (attrs) => (attrs.fontFamily ? { style: `font-family: ${attrs.fontFamily}` } : {})
        }
      }
    }]
  },
  addCommands() {
    return {
      setFontFamily: (family) => ({ chain }) => chain().setMark('textStyle', { fontFamily: family }).run(),
      unsetFontFamily: () => ({ chain }) => chain().setMark('textStyle', { fontFamily: null }).removeEmptyTextStyle().run()
    }
  }
})

// 字号扩展：同上，输出 inline font-size
export const FontSize = Extension.create({
  name: 'fontSize',
  addGlobalAttributes() {
    return [{
      types: ['textStyle'],
      attributes: {
        fontSize: {
          default: null,
          parseHTML: (el) => el.style.fontSize || null,
          renderHTML: (attrs) => (attrs.fontSize ? { style: `font-size: ${attrs.fontSize}` } : {})
        }
      }
    }]
  },
  addCommands() {
    return {
      setFontSize: (size) => ({ chain }) => chain().setMark('textStyle', { fontSize: size }).run(),
      unsetFontSize: () => ({ chain }) => chain().setMark('textStyle', { fontSize: null }).removeEmptyTextStyle().run()
    }
  }
})

// 可缩放图片：在默认 Image 扩展上增加 width 属性 + 右下角拖拽手柄（原生 DOM NodeView）。
// 只写宽度，高度靠 CSS height:auto 自动等比例，不会拉伸变形。
export const ResizableImage = Image.extend({
  addAttributes() {
    return {
      ...this.parent?.(),
      width: {
        default: null,
        renderHTML: (attributes) => {
          if (!attributes.width) return {}
          return { width: attributes.width }
        },
        parseHTML: (element) => {
          const w = element.getAttribute('width')
          return w ? parseInt(w, 10) : null
        }
      }
    }
  },
  addNodeView() {
    return ({ node, editor, getPos }) => {
      const container = document.createElement('div')
      container.className = 'image-resizer'

      const img = document.createElement('img')
      // v2.1.2：子路径挂载下把存储态 /uploads/… 补上挂载前缀（只影响 DOM，不进模型）
      img.src = toDisplayUrl(node.attrs.src)
      img.alt = node.attrs.alt || ''
      if (node.attrs.title) img.title = node.attrs.title
      if (node.attrs.width) img.style.width = node.attrs.width + 'px'

      const handle = document.createElement('span')
      handle.className = 'image-resize-handle'
      handle.contentEditable = 'false'

      let startX = 0
      let startWidth = 0
      let maxWidth = 9999

      const applyMove = (clientX) => {
        const w = Math.max(50, Math.min(startWidth + (clientX - startX), maxWidth))
        img.style.width = w + 'px'
      }
      const finish = () => {
        document.removeEventListener('mousemove', onMouseMove)
        document.removeEventListener('mouseup', onEnd)
        document.removeEventListener('touchmove', onTouchMove)
        document.removeEventListener('touchend', onEnd)
        const finalWidth = Math.round(img.getBoundingClientRect().width)
        if (typeof getPos === 'function' && finalWidth > 0) {
          editor.chain().focus().command(({ tr }) => {
            tr.setNodeMarkup(getPos(), undefined, { ...node.attrs, width: finalWidth })
            return true
          }).run()
        }
      }
      const onMouseMove = (e) => { e.preventDefault(); applyMove(e.clientX) }
      const onTouchMove = (e) => { if (e.touches[0]) applyMove(e.touches[0].clientX) }
      const onEnd = () => finish()

      const startDrag = (clientX) => {
        startX = clientX
        startWidth = img.getBoundingClientRect().width
        // 上限：编辑器内容区宽度，防止拖出溢出屏幕的图
        maxWidth = container.parentElement ? container.parentElement.clientWidth : 9999
      }
      handle.addEventListener('mousedown', (e) => {
        e.preventDefault()
        e.stopPropagation()
        startDrag(e.clientX)
        document.addEventListener('mousemove', onMouseMove)
        document.addEventListener('mouseup', onEnd)
      })
      handle.addEventListener('touchstart', (e) => {
        e.preventDefault()
        e.stopPropagation()
        if (e.touches[0]) startDrag(e.touches[0].clientX)
        document.addEventListener('touchmove', onTouchMove, { passive: false })
        document.addEventListener('touchend', onEnd)
      }, { passive: false })

      container.appendChild(img)
      container.appendChild(handle)

      return {
        dom: container,
        update: (updatedNode) => {
          if (updatedNode.type.name !== node.type.name) return false
          img.src = toDisplayUrl(updatedNode.attrs.src)
          img.style.width = updatedNode.attrs.width ? updatedNode.attrs.width + 'px' : ''
          return true
        }
      }
    }
  }
})

// —— 外提自 TiptapEditor：缩进 + 触屏任务项 ——
const isTouchDevice = typeof window !== 'undefined'
  && ('ontouchstart' in window || navigator.maxTouchPoints > 0)

function changeIndent(delta) {
  return ({ state, dispatch }) => {
    const { selection } = state
    const tr = state.tr
    let changed = false
    state.doc.nodesBetween(selection.from, selection.to, (node, pos) => {
      if (node.type.name === 'paragraph') {
        const cur = node.attrs.indent || 0
        const next = Math.max(0, Math.min(5, cur + delta))
        if (next !== cur) {
          tr.setNodeMarkup(pos, undefined, { ...node.attrs, indent: next })
          changed = true
        }
        return false
      }
      return true
    })
    if (!changed) return false
    if (dispatch) dispatch(tr)
    return true
  }
}

export const IndentParagraph = Paragraph.extend({
  addAttributes() {
    return {
      ...this.parent?.(),
      indent: {
        default: 0,
        parseHTML: (el) => parseInt(el.getAttribute('data-indent') || '0', 10) || 0,
        renderHTML: (attrs) => (attrs.indent
          ? { 'data-indent': String(attrs.indent), style: `margin-left: ${attrs.indent * 2}em` }
          : {})
      }
    }
  },
  addCommands() {
    return {
      ...this.parent?.(),
      increaseIndent: () => changeIndent(1),
      decreaseIndent: () => changeIndent(-1)
    }
  }
})

export const StickyTaskItem = TaskItem.extend({
  addNodeView() {
    return ({ node, HTMLAttributes, getPos, editor }) => {
      const listItem = document.createElement('li')
      const checkboxWrapper = document.createElement('label')
      const checkboxStyler = document.createElement('span')
      const checkbox = document.createElement('input')
      const content = document.createElement('div')
      const visuallyHiddenStyle = 'position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0'
      checkboxStyler.style.cssText = visuallyHiddenStyle
      const updateA11Y = (currentNode) => {
        const label = (this.options.a11y
          && this.options.a11y.checkboxLabel
          && this.options.a11y.checkboxLabel(currentNode, currentNode.attrs.checked))
          || `Task item checkbox for ${currentNode.textContent || 'empty task item'}`
        checkbox.setAttribute('aria-label', label)
        checkboxStyler.textContent = label
      }
      updateA11Y(node)
      checkboxWrapper.contentEditable = 'false'
      checkbox.type = 'checkbox'
      checkbox.addEventListener('mousedown', (event) => event.preventDefault())
      checkbox.addEventListener('change', (event) => {
        // 只读编辑器且无 onReadOnlyChecked：回滚勾选（官方行为保留）
        if (!editor.isEditable && !this.options.onReadOnlyChecked) {
          checkbox.checked = !checkbox.checked
          return
        }
        const { checked } = event.target
        if (editor.isEditable && typeof getPos === 'function') {
          const cmd = ({ tr }) => {
            const position = getPos()
            if (typeof position !== 'number') return false
            const currentNode = tr.doc.nodeAt(position)
            tr.setNodeMarkup(position, undefined, { ...currentNode.attrs, checked })
            return true
          }
          // 触屏：跳过 focus → 不弹软键盘、不滚动视口；桌面：与官方一致回焦编辑器
          if (isTouchDevice) editor.chain().command(cmd).run()
          else editor.chain().focus(undefined, { scrollIntoView: false }).command(cmd).run()
        }
        if (!editor.isEditable && this.options.onReadOnlyChecked) {
          if (!this.options.onReadOnlyChecked(node, checked)) checkbox.checked = !checkbox.checked
        }
      })
      listItem.dataset.checked = node.attrs.checked
      checkbox.checked = node.attrs.checked
      checkboxWrapper.append(checkbox, checkboxStyler)
      listItem.append(checkboxWrapper, content)
      Object.entries(this.options.HTMLAttributes).forEach(([key, value]) => {
        listItem.setAttribute(key, value)
      })
      Object.entries(HTMLAttributes).forEach(([key, value]) => {
        listItem.setAttribute(key, value)
      })
      let prevRenderedAttributeKeys = new Set(Object.keys(HTMLAttributes))
      return {
        dom: listItem,
        contentDOM: content,
        update: (updatedNode) => {
          if (updatedNode.type !== this.type) return false
          listItem.dataset.checked = updatedNode.attrs.checked
          checkbox.checked = updatedNode.attrs.checked
          updateA11Y(updatedNode)
          const extensionAttributes = editor.extensionManager.attributes
          const newHTMLAttributes = getRenderedAttributes(updatedNode, extensionAttributes)
          const newKeys = new Set(Object.keys(newHTMLAttributes))
          const staticAttrs = this.options.HTMLAttributes
          prevRenderedAttributeKeys.forEach((key) => {
            if (!newKeys.has(key)) {
              if (key in staticAttrs) listItem.setAttribute(key, staticAttrs[key])
              else listItem.removeAttribute(key)
            }
          })
          Object.entries(newHTMLAttributes).forEach(([key, value]) => {
            if (value === null || value === undefined) {
              if (key in staticAttrs) listItem.setAttribute(key, staticAttrs[key])
              else listItem.removeAttribute(key)
            } else {
              listItem.setAttribute(key, value)
            }
          })
          prevRenderedAttributeKeys = newKeys
          return true
        }
      }
    }
  }
})
