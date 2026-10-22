/**
 * 上传资源 URL 的「存储态 → 展示态」换算（v2.1.2 · 移动端子路径根治配套）。
 *
 * ## 为什么需要
 * 笔记正文里图片一律以**存储态绝对路径** `/uploads/<uuid>.png` 落库（后端
 * `handler.ServeUpload` 的路由也是 `/uploads/:name`）。这在「站点根」下没问题，
 * 但应用被挂在子路径（fnOS 网关 `/app/<appname>/`）时，浏览器会把 `/uploads/…`
 * 解析到**宿主根** → 落到飞牛主站 → 图片全裂。
 *
 * ## 做法
 * **只改渲染层，绝不改存储**：
 * - 阅读/预览态：`utils/sanitize.js` 的 DOMPurify hook 用 `toDisplayUrl()` 替换 `src`
 * - 编辑态：`components/editor/extensions.js` 的 ResizableImage NodeView 用 `toDisplayUrl()`
 *
 * 存储格式保持 `/uploads/…` 不变，因此：
 * - 桌面壳（IS_DESKTOP，base 恒为 `''`）行为零变化
 * - 根路径下 `toDisplayUrl()` 原样返回，行为零变化
 * - 不会把挂载前缀写进笔记正文（避免污染数据 + 跨环境不可移植）
 */

import { detectBase } from './appBase'

/** 后端上传路由前缀（存储态口径，勿改） */
export const UPLOAD_PREFIX = '/uploads/'

/**
 * 存储态 URL → 当前挂载点下可直接访问的 URL。
 * 非上传资源（外链、data:、blob: 等）原样返回。
 * @param {string} src
 * @returns {string}
 */
export function toDisplayUrl (src) {
  if (!src || typeof src !== 'string') return src
  const base = detectBase()
  if (!base) return src
  if (src.startsWith(UPLOAD_PREFIX)) return base + src
  return src
}

/**
 * 是否为本站上传资源（兼容存储态与已换算的展示态）。
 * 用于消毒白名单——既不能放过外链图片，也不能误杀子路径下的本站图片。
 * @param {string} src
 * @returns {boolean}
 */
export function isUploadUrl (src) {
  if (!src || typeof src !== 'string') return false
  if (src.startsWith(UPLOAD_PREFIX)) return true
  const base = detectBase()
  return base ? src.startsWith(base + UPLOAD_PREFIX) : false
}
