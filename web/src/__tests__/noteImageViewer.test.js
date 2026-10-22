// noteImageViewer.test.js —— 图片查看器（v21.179 重写为 NoteImagePreview 全功能版）行为钉。
// 覆盖：打开渲染 / 多图三通道切换（缩略图·箭头·键盘循环）/ 缩放 clamp / 旋转步进与重置 /
//       关闭链路（Esc·空白·按钮，点图不关）/ 下载文件名 / 缩略图开关 / body 滚动锁。
// 说明：Teleport 以 stub 方式就地渲染（内容进 wrapper，便于标准 API 断言）。
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import NoteImagePreview from '../components/NoteImagePreview.vue'

const IMG_A = 'http://x.test/uploads/a.png'
const IMG_B = 'http://x.test/uploads/b.png'
const IMG_C = 'http://x.test/uploads/c.png'

function mountViewer (props = {}) {
  return mount(NoteImagePreview, {
    props: {
      visible: true,
      list: [IMG_A, IMG_B, IMG_C],
      index: 0,
      title: '测试笔记',
      ...props
    },
    global: {
      stubs: {
        teleport: true,
        'el-icon': { template: '<span class="ei"><slot /></span>' }
      }
    }
  })
}

afterEach(() => {
  document.body.style.overflow = ''
  vi.restoreAllMocks()
})

describe('图片查看器（viewer）', () => {
  it('visible=false 不渲染；打开时主图/工具栏/箭头/缩略图齐备', () => {
    const hidden = mountViewer({ visible: false })
    expect(hidden.find('.image-viewer').exists()).toBe(false)

    const w = mountViewer()
    expect(w.find('.iv-img').attributes('src')).toBe(IMG_A)
    // 左旋/右旋/下载/缩略图/关闭 = 5 个工具按钮
    expect(w.findAll('.iv-btn').length).toBe(5)
    expect(w.findAll('.iv-nav').length).toBe(2)
    expect(w.findAll('.iv-thumb').length).toBe(3)
    expect(w.findAll('.iv-thumb')[0].classes()).toContain('is-active')
  })

  it('单图：无箭头、无缩略图条、工具栏无缩略图开关按钮', () => {
    const w = mountViewer({ list: [IMG_A] })
    expect(w.findAll('.iv-nav').length).toBe(0)
    expect(w.find('.iv-thumbs').exists()).toBe(false)
    expect(w.findAll('.iv-btn').length).toBe(4)
  })

  it('点击缩略图回传 update:index，主图与高亮跟随', async () => {
    const w = mountViewer()
    await w.findAll('.iv-thumb')[2].trigger('click')
    expect(w.emitted('update:index')[0]).toEqual([2])

    await w.setProps({ index: 2 })
    expect(w.find('.iv-img').attributes('src')).toBe(IMG_C)
    expect(w.findAll('.iv-thumb')[2].classes()).toContain('is-active')
  })

  it('键盘 ←/→ 循环切换；两侧箭头按钮同源', async () => {
    const w = mountViewer()
    await w.find('.image-viewer').trigger('keydown', { key: 'ArrowRight' })
    expect(w.emitted('update:index')[0]).toEqual([1])
    // index 仍为 0（v-model 由父组件驱动）→ 左移应循环到末张
    await w.find('.image-viewer').trigger('keydown', { key: 'ArrowLeft' })
    expect(w.emitted('update:index')[1]).toEqual([2])

    await w.find('.iv-nav-next').trigger('click')
    expect(w.emitted('update:index')[2]).toEqual([1])
    await w.find('.iv-nav-prev').trigger('click')
    expect(w.emitted('update:index')[3]).toEqual([2])
  })

  it('滚轮缩放：以锚点公式变换且上下限 clamp 到 [0.2, 5]', async () => {
    const w = mountViewer()
    const stage = w.find('.iv-stage')
    await stage.trigger('wheel', { deltaY: -300 })
    expect(w.vm.scale).toBeGreaterThan(1)
    expect(w.find('.iv-img').attributes('style')).toContain('scale(')

    for (let i = 0; i < 40; i++) await stage.trigger('wheel', { deltaY: -300 })
    expect(w.vm.scale).toBe(5)
    for (let i = 0; i < 80; i++) await stage.trigger('wheel', { deltaY: 300 })
    expect(w.vm.scale).toBe(0.2)
  })

  it('旋转 90° 步进；切图时缩放/旋转/平移全部重置', async () => {
    const w = mountViewer()
    await w.find('[aria-label="向右旋转"]').trigger('click')
    expect(w.vm.rotate).toBe(90)
    expect(w.find('.iv-img').attributes('style')).toContain('rotate(90deg)')

    await w.setProps({ index: 1 })
    expect(w.vm.rotate).toBe(0)
    expect(w.vm.scale).toBe(1)

    await w.find('[aria-label="向左旋转"]').trigger('click')
    expect(w.vm.rotate).toBe(270)
  })

  it('关闭链路：Esc / 空白关闭；点图片不关闭；关闭按钮回传 false', async () => {
    const w = mountViewer()
    // 点图片：不关闭（冒泡到 stage 的 handler 内做 img 判定）
    await w.find('.iv-img').trigger('click')
    expect(w.emitted('update:visible')).toBeUndefined()
    // 点空白：关闭
    await w.find('.iv-stage').trigger('click')
    expect(w.emitted('update:visible')[0]).toEqual([false])

    // Esc
    await w.find('.image-viewer').trigger('keydown', { key: 'Escape' })
    expect(w.emitted('update:visible')[1]).toEqual([false])

    // 工具栏关闭按钮
    await w.find('[aria-label="关闭预览"]').trigger('click')
    expect(w.emitted('update:visible')[2]).toEqual([false])
  })

  it('缩略图条开关：按钮切换显隐', async () => {
    const w = mountViewer()
    expect(w.find('.iv-thumbs').exists()).toBe(true)
    await w.find('[aria-label="切换缩略图"]').trigger('click')
    expect(w.find('.iv-thumbs').exists()).toBe(false)
    await w.find('[aria-label="切换缩略图"]').trigger('click')
    expect(w.find('.iv-thumbs').exists()).toBe(true)
  })

  it('下载：文件名 = 笔记标题-序号.扩展名（非法字符替换）', async () => {
    let captured = null
    const spy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function () {
      captured = this
    })
    const w = mountViewer({ index: 1, title: 'VPS/账号: 测试' })
    await w.find('[aria-label="下载图片"]').trigger('click')
    expect(spy).toHaveBeenCalledTimes(1)
    expect(captured.getAttribute('download')).toBe('VPS_账号_测试-2.png')
    expect(captured.getAttribute('href')).toBe(IMG_B)
  })

  it('body 滚动锁：打开锁定、关闭恢复、卸载兜底恢复', async () => {
    const w = mountViewer({ visible: false })
    await w.setProps({ visible: true })
    expect(document.body.style.overflow).toBe('hidden')
    await w.setProps({ visible: false })
    expect(document.body.style.overflow).toBe('')

    await w.setProps({ visible: true })
    w.unmount()
    expect(document.body.style.overflow).toBe('')
  })

  it('重新打开：缩放/旋转/缩略图开关状态归位', async () => {
    const w = mountViewer()
    await w.find('[aria-label="向右旋转"]').trigger('click')
    await w.find('[aria-label="切换缩略图"]').trigger('click')
    expect(w.vm.rotate).toBe(90)
    expect(w.vm.showThumbs).toBe(false)

    await w.setProps({ visible: false })
    await w.setProps({ visible: true })
    expect(w.vm.rotate).toBe(0)
    expect(w.vm.scale).toBe(1)
    expect(w.vm.showThumbs).toBe(true)
  })
})
