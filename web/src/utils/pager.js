// pager.js —— 笔记列表分页「数字窗」纯函数（1.0.218，参考图三段式）。
//
// 规则（N=总页数，c=当前页）：
//   首部态（c ≤ 4）：   1 2 3 4 … N
//   中部态（其余中段）： 1 … c-1 c c+1 … N
//   尾部态（c ≥ N-3）： 1 … N-3 N-2 N-1 N
//   N ≤ 7：全量直列（不折叠）。
// 返回项：{ type:'page', page } | { type:'ellipsis', id }。纯函数，边界单测见 __tests__/notePager.test.js。

export function buildPagerItems (totalPages, current) {
  const n = Math.max(1, Math.floor(Number(totalPages) || 1))
  const c = Math.min(Math.max(1, Math.floor(Number(current) || 1)), n)
  const pages = (from, to) => {
    const out = []
    for (let p = from; p <= to; p++) out.push({ type: 'page', page: p })
    return out
  }
  if (n <= 7) return pages(1, n)
  if (c <= 4) {
    return [...pages(1, 4), { type: 'ellipsis', id: 'tail' }, { type: 'page', page: n }]
  }
  if (c >= n - 3) {
    return [{ type: 'page', page: 1 }, { type: 'ellipsis', id: 'head' }, ...pages(n - 3, n)]
  }
  return [
    { type: 'page', page: 1 },
    { type: 'ellipsis', id: 'head' },
    ...pages(c - 1, c + 1),
    { type: 'ellipsis', id: 'tail' },
    { type: 'page', page: n }
  ]
}
