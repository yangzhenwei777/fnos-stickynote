/**
 * noteFolderHint —— 「全部笔记」视图选中笔记所属文件夹的提示值归一。
 *
 * 2.6.7 用户需求：在「全部笔记」列表点选笔记（预览/编辑）时，左侧文件夹列表
 * 中该笔记所属文件夹行右侧显示蓝色小圆点，帮助用户定位笔记归属。
 *
 * 返回：
 *   null      —— 没有选中笔记（不显示任何提示）
 *   'default' —— 归属默认「我的笔记」（folder_id 缺省/空/0）
 *   number    —— 归属某个自定义文件夹 id
 *
 * 纯函数，便于单测（开发纪律 4.2 / 4.4）。
 */
export function resolveNoteFolderHint(note) {
  if (!note) return null
  const raw = note.folder_id
  if (raw === null || raw === undefined || raw === '') return 'default'
  const n = Number(raw)
  if (!Number.isFinite(n) || n < 0) return null
  return n === 0 ? 'default' : n
}
