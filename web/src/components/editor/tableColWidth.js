// tableColWidth.js — 表格列宽相关的**存量数据修复**（v2.2.2 R3 重写）。
//
// ⭐ 历史教训（R2 引入的 P0 缺陷，R3 根治，勿再犯）：
// R2 曾在「载入编辑器前」用正则把 `<col style="width:Npx">` 补写成 `width` 属性。
// 该实现只匹配到 style 的收尾引号、**没吞掉标签的 `>`**——每列残留一个多余的 `>`，
// 残留文本落在 `<colgroup>` 内，经浏览器/ProseMirror 解析后被提升为表格前的
// `<p>&gt;&gt;&gt;</p>`；而编辑器卸载时 flushContent 会把该串回写并自动保存，
// 于是「每点一次编辑就多一行 `>`」，且随每次保存累积（用户 R2 fpk 实测复现）。
//
// **结论：编辑器载入路径绝不做字符串级 HTML 改写**。列宽回读改由 Tiptap 节点
// parseHTML 承担（见 editor/tableExt.js：单元格 colwidth 属性 → col 的 width 属性
// → col 的 style width 三条通道通吃，内容零改写、天然幂等）。
//
// 本文件仅保留一个**有界**的存量修复函数：清除上述缺陷在表格前留下的「纯 `>` 段落」。
// 命中条件刻意收紧（只对**紧邻表格**的纯 '>' 段落生效），真实正文里的 `>` 不受影响。

/** 表格前纯 '>' 段落（`>` 可能已实体化为 `&gt;`）。要求后面紧跟（可有空白）`<table` */
const ARTIFACT_P_RE = /<p>\s*(?:&gt;|>)+?\s*<\/p>\s*(?=<table\b)/gi

/**
 * 清除 R2 列宽缺陷遗留的「表格前纯 `>` 段落」（可连续多行）。
 * @param {string} html
 * @returns {string}
 */
export function stripLegacyColWidthArtifacts (html) {
  if (!html || typeof html !== 'string' || !html.includes('<table')) return html
  let out = html
  // 每次只剥紧邻表格的那一个；连续多行需逐层剥（上限防御，正常 ≤ 列数）
  for (let i = 0; i < 32; i++) {
    const next = out.replace(ARTIFACT_P_RE, '')
    if (next === out) break
    out = next
  }
  return out
}
