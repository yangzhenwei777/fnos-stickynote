import { describe, it, expect } from 'vitest'
import {
  pickDpr, sanitizeFilename, formatPosterDate, posterFilename,
  staticizeCheckboxes, absolutizeImages, waitForImages,
  buildPosterElement, exportNotePoster, applyPreviewMetrics, POSTER_WIDTH, POSTER_CONTENT_WIDTH,
  POSTER_CODE_FONT
} from '@/utils/notePoster'

// v2.2.2「导出为长图」纯逻辑护栏：不测像素，测模板拼装/静态化/URL 绝对化/dpr 降档/异常分支
// （刀 1 Spike 已实测像素保真与超长分窗，jsdom 无真实布局不重复覆盖）

describe('pickDpr — 高度降档', () => {
  it('≤8000 → 2（清晰档，含边界）', () => {
    expect(pickDpr(0)).toBe(2)
    expect(pickDpr(7999)).toBe(2)
    expect(pickDpr(8000)).toBe(2)
  })
  it('8001~16000 → 1.5（含上边界）', () => {
    expect(pickDpr(8001)).toBe(1.5)
    expect(pickDpr(16000)).toBe(1.5)
  })
  it('>16000 → 1（保导出成功优先）', () => {
    expect(pickDpr(16001)).toBe(1)
    expect(pickDpr(60000)).toBe(1)
  })
})

describe('sanitizeFilename — 文件名清洗', () => {
  it('替换 Windows 非法字符', () => {
    expect(sanitizeFilename('a/b\\c:d*e?f"g<h>i|j')).toBe('a_b_c_d_e_f_g_h_i_j')
  })
  it('压缩空白并去首尾空白', () => {
    expect(sanitizeFilename('  我的   笔记  ')).toBe('我的 笔记')
  })
  it('去除结尾点号（Windows 目录名限制）', () => {
    expect(sanitizeFilename('笔记...')).toBe('笔记')
  })
  it('空标题兜底「未命名」', () => {
    expect(sanitizeFilename('')).toBe('未命名')
    expect(sanitizeFilename(null)).toBe('未命名')
    expect(sanitizeFilename('///')).toBe('未命名')
  })
  it('超长截断到 80 字符', () => {
    expect(sanitizeFilename('长'.repeat(100)).length).toBe(80)
  })
})

describe('formatPosterDate / posterFilename — 日期与命名', () => {
  it('YYYY-MM-DD HH:mm 格式', () => {
    expect(formatPosterDate(new Date(2026, 9, 28, 9, 5))).toBe('2026-10-28 09:05')
  })
  it('无效输入返回空串（头部隐藏日期行由调用方处理）', () => {
    expect(formatPosterDate('not-a-date')).toBe('')
  })
  it('文件名 = 标题-YYYYMMDD.png', () => {
    expect(posterFilename('我的笔记', new Date(2026, 9, 28))).toBe('我的笔记-20261028.png')
  })
})

describe('staticizeCheckboxes — checkbox 静态化', () => {
  function mount (html) {
    const root = document.createElement('div')
    root.innerHTML = html
    staticizeCheckboxes(root)
    return root
  }
  it('checked → is-checked + ✓ 符号，input 消失', () => {
    const root = mount('<input type="checkbox" checked="checked">')
    expect(root.querySelector('input')).toBeNull()
    const span = root.querySelector('span.poster-check')
    expect(span).not.toBeNull()
    expect(span.classList.contains('is-checked')).toBe(true)
    expect(span.textContent).toBe('✓')
  })
  it('未勾选 → 无 is-checked、无符号', () => {
    const root = mount('<input type="checkbox">')
    const span = root.querySelector('span.poster-check')
    expect(span.classList.contains('is-checked')).toBe(false)
    expect(span.textContent).toBe('')
  })
  it('checked="false" 视为未勾选（Tiptap 存储态兼容）', () => {
    const root = mount('<input type="checkbox" checked="false">')
    expect(root.querySelector('.poster-check').classList.contains('is-checked')).toBe(false)
  })
})

describe('absolutizeImages — 图片 URL 绝对化', () => {
  function mount (html) {
    const root = document.createElement('div')
    root.innerHTML = html
    absolutizeImages(root)
    return root
  }
  it('相对 /uploads/ → 绝对 URL（子路径部署防 404）', () => {
    const img = mount('<img src="/uploads/a.png">').querySelector('img')
    const u = new URL(img.getAttribute('src'))
    expect(u.pathname).toBe('/uploads/a.png')
    expect(u.href.startsWith('http://localhost')).toBe(true)
  })
  it('data:/https: 已是绝对形态则原样保留', () => {
    const root = mount('<img src="data:image/png;base64,AAA"><img src="https://x/y.png">')
    const [a, b] = root.querySelectorAll('img')
    expect(a.getAttribute('src')).toBe('data:image/png;base64,AAA')
    expect(b.getAttribute('src')).toBe('https://x/y.png')
  })
})

describe('waitForImages — 图片解码等待', () => {
  it('jsdom 无 decode 不抛错，正常 resolve', async () => {
    const root = document.createElement('div')
    root.innerHTML = '<img src="/uploads/a.png">'
    await expect(waitForImages(root)).resolves.toBeUndefined()
  })
})

describe('buildPosterElement — 海报容器拼装', () => {
  it('含头部（标题+日期）、正文、页脚，默认宽 800', () => {
    const el = buildPosterElement({
      title: 'T1', html: '<p>正文</p>',
      updated: '2026-10-28 09:05', created: '2026-10-27 08:00'
    })
    expect(el.style.width).toBe(`${POSTER_WIDTH}px`)
    expect(el.querySelector('.poster-title').textContent).toBe('T1')
    expect(el.querySelector('.poster-meta').textContent).toContain('2026-10-28 09:05')
    expect(el.querySelector('.poster-body').innerHTML).toContain('正文')
    expect(el.querySelector('.poster-foot').textContent).toContain('Sticky note')
    expect(el.querySelector('style').textContent).toContain('.note-poster')
  })
  it('无更新时间时回退创建时间', () => {
    const el = buildPosterElement({ title: 'T', html: '<p>x</p>', updated: '', created: '2026-01-02 03:04' })
    expect(el.querySelector('.poster-meta').textContent).toContain('2026-01-02 03:04')
  })
  it('withHeader=false 不渲染头部', () => {
    const el = buildPosterElement({ title: 'T', html: '<p>x</p>', withHeader: false })
    expect(el.querySelector('.poster-head')).toBeNull()
    expect(el.querySelector('.poster-title')).toBeNull()
  })
  it('标题走 textContent 防注入（不产生 img 元素）', () => {
    const el = buildPosterElement({ title: '<img src=x onerror=y>', html: '<p>x</p>' })
    expect(el.querySelector('.poster-title').querySelector('img')).toBeNull()
    expect(el.querySelector('.poster-title').textContent).toBe('<img src=x onerror=y>')
  })
  it('正文内容过静态化与绝对化（组装即完成副本改造）', () => {
    const el = buildPosterElement({
      title: 'T',
      html: '<ul data-type="taskList"><li data-checked="true"><label><input type="checkbox" checked="checked"><span></span></label><div><p>项</p></div></li></ul><img src="/uploads/b.png">'
    })
    expect(el.querySelector('input[type="checkbox"]')).toBeNull()
    expect(el.querySelector('.poster-check.is-checked')).not.toBeNull()
    const img = el.querySelector('img')
    expect(new URL(img.getAttribute('src')).pathname).toBe('/uploads/b.png')
  })
  // v2.2.2 R7：POSTER_CSS 随海报子树克隆进 SVG 后拿不到 :root 的 --font-code，
  // 且 .poster-body code 的优先级高于 SnapDOM 生成的类样式 → 长图中文回退 NSimSun。
  // 代码字体必须是自包含字面量栈，禁止再改回 var()。
  it('代码字体为自包含字面量栈（禁止依赖 :root 变量）', () => {
    const el = buildPosterElement({ title: 'T', html: '<pre><code>中文 English</code></pre>' })
    const css = el.querySelector('style').textContent
    expect(css).toContain(POSTER_CODE_FONT)
    expect(css).not.toContain('var(--font-code')
    expect(POSTER_CODE_FONT).toContain("'Microsoft YaHei UI'")
    expect(POSTER_CODE_FONT).toContain("'PingFang SC'")
  })
})

describe('exportNotePoster — 入口校验', () => {
  it('空内容直接抛错（不进入捕获流程）', async () => {
    await expect(exportNotePoster({ title: 'T', content: '' })).rejects.toThrow('笔记内容为空')
    await expect(exportNotePoster(null)).rejects.toThrow('笔记内容为空')
  })
})

// v2.2.2 R3 引入、R4 收窄：applyPreviewMetrics 现在**只处理图片**。
// 表格列宽自 R4 起在展示层已投影为百分比（table 固定布局 + 100% 宽），
// 海报与预览天然同比例；若此处再改写列宽会退化成 px，重新引入容器宽度依赖。
describe('applyPreviewMetrics — 仅图片等比投影', () => {
  const TBL = '<table><colgroup><col style="width: 37.5%"><col style="width: 30%"><col style="width: 32.5%"></colgroup>' +
    '<tbody><tr><th><p>A</p></th><th><p>B</p></th><th><p>C</p></th></tr></tbody></table>'

  function bodyOf (html) {
    const d = document.createElement('div')
    d.innerHTML = html
    return d
  }

  it('图片宽度按 k=海报/预览 等比缩放（只缩不放）', () => {
    const body = bodyOf(`${TBL}<img src="/uploads/a.png" width="240">`)
    // 预览内容宽 1000 → 海报 688，k=0.688
    const ok = applyPreviewMetrics(body, { contentWidth: 1000, images: [240] })
    expect(ok).toBe(true)
    expect(body.querySelector('img').getAttribute('width')).toBe(String(Math.round(240 * 0.688)))
  })

  it('表格列宽**不被改写**（保持百分比如初，避免退化成 px）', () => {
    const body = bodyOf(`${TBL}<img src="/uploads/a.png" width="240">`)
    applyPreviewMetrics(body, { contentWidth: 1000, tables: [[377, 285, 326]], images: [240] })
    const cols = body.querySelectorAll('colgroup > col')
    expect(cols[0].getAttribute('style')).toBe('width: 37.5%')
    expect(body.querySelector('table').getAttribute('data-has-colwidth')).toBeNull()
  })

  it('预览比海报窄（手机）→ k 封顶 1，不放大', () => {
    const body = bodyOf('<img src="/uploads/a.png" width="300">')
    applyPreviewMetrics(body, { contentWidth: 360, images: [300] })
    expect(body.querySelector('img').getAttribute('width')).toBe('300')
  })

  it('无实测数据 → 不改动（退回 CSS 默认布局）', () => {
    const body = bodyOf('<img src="/uploads/a.png" width="240">')
    expect(applyPreviewMetrics(body, null)).toBe(false)
    expect(applyPreviewMetrics(body, { contentWidth: 0, images: [] })).toBe(false)
    expect(body.querySelector('img').getAttribute('width')).toBe('240')
  })

  it('图片不小于 24px 下限；不超出海报内容宽', () => {
    const body = bodyOf('<img src="/uploads/a.png" width="1200">')
    applyPreviewMetrics(body, { contentWidth: 1000, images: [1200] })
    expect(Number(body.querySelector('img').getAttribute('width'))).toBeLessThanOrEqual(POSTER_CONTENT_WIDTH)
  })
})
