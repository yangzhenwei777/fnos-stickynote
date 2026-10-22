// routerPaths.test.js —— v2.1.3 路由扁平化门禁
//
// 为什么要有这条门禁：
//   fnOS 手机 App 经网关 /app/<appname>/ 子路径加载，且网关可能剥前缀。此时相对资源
//   （`./assets/…`）的解析基址 = 地址栏去掉最后一段：
//     单段 /app/stickynote/admin-users  → /app/stickynote/   ✅
//     多段 /app/stickynote/admin/users  → /app/stickynote/admin/ ❌ 资源全 404
//   v2.1.2 靠内联脚本自举 <base> 兜住了这条，但那**依赖内联脚本先于入口 module 执行**；
//   一旦 HTML 与 CSP 版本错配（缓存旧页）脚本即被拦掉。路由压成单段后，
//   正确性由 URL 结构本身保证，不依赖任何 JS。
//
// 这条门禁就是防"以后有人手滑又加个 admin/foo"。

import { describe, it, expect } from 'vitest'
import routerSource from '@/router/index.js?raw'

function collectPaths (src) {
  return Array.from(src.matchAll(/path:\s*'([^']*)'/g)).map(m => m[1])
}

describe('路由扁平化（v2.1.3）', () => {
  const paths = collectPaths(routerSource)

  it('路由表非空（解析器本身没坏）', () => {
    expect(paths.length).toBeGreaterThanOrEqual(10)
  })

  it('所有路由 path 都是单段（不允许 admin/users 这类多段路径）', () => {
    const deep = paths.filter(p => p && p.replace(/^\//, '').includes('/'))
    expect(deep, `发现多段路由，子路径网关下会被多吃一层相对基址：${deep.join(', ')}`).toEqual([])
  })

  it('管理后台五个页面均在路由表中（扁平命名）', () => {
    for (const p of ['admin-users', 'admin-logs', 'admin-configs', 'admin-limits', 'admin-backup']) {
      expect(paths, `缺少路由 ${p}`).toContain(p)
    }
  })

  it('旧 /admin/xxx 深链接保留兼容重定向，且目标路由存在', () => {
    const pairs = Array.from(
      routerSource.matchAll(/\['(\/admin\/[^']+)',\s*'(\/[^']+)'\]/g)
    ).map(m => [m[1], m[2]])
    expect(pairs.length, '兼容重定向表为空——老链接会 404').toBe(5)
    for (const [from, to] of pairs) {
      expect(from.startsWith('/admin/'), `源路径不像旧深链接：${from}`).toBe(true)
      expect(paths, `重定向目标 ${to} 不在路由表中`).toContain(to.replace(/^\//, ''))
    }
  })

  it('重定向必须保留 query 与 hash（备份页带 ?id=… 不能丢）', () => {
    expect(routerSource).toContain('query: route.query')
    expect(routerSource).toContain('hash: route.hash')
  })
})
