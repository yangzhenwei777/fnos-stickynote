package htmltext

import (
	"encoding/base64"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

var mdMultiNewline = regexp.MustCompile(`\n{3,}`)

// ToMarkdown 把 tiptap 产出的笔记 HTML 转为 Markdown。
func ToMarkdown(htmlStr string) string {
	return ToMarkdownWithImages(htmlStr, nil)
}

// ImageResolver 按图片 src 返回 (MIME 类型, 文件内容, 是否命中)。
// 返回 ok=false 时该图保持原样引用（不阻断导出）。
type ImageResolver func(src string) (mime string, data []byte, ok bool)

// ToMarkdownWithImages 同 ToMarkdown，但对 <img> 调用 resolver：
// 命中时输出 base64 data URI 内联（单文件自包含），未命中保持原样引用。
// v2.2.2：MD 导出图片嵌入——/uploads 相对引用脱离应用不可达（且带鉴权），
// 脱文件打开必然裂图，故按用户拍板统一 base64 嵌入。
func ToMarkdownWithImages(htmlStr string, resolveImage ImageResolver) string {
	if htmlStr == "" {
		return ""
	}
	doc, err := xhtml.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return ""
	}

	// 定位到 <body> 避免输出版本声明等文档级噪声
	var root *xhtml.Node
	var findBody func(*xhtml.Node) *xhtml.Node
	findBody = func(n *xhtml.Node) *xhtml.Node {
		if n.Type == xhtml.ElementNode && n.Data == "body" {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if b := findBody(c); b != nil {
				return b
			}
		}
		return nil
	}
	root = findBody(doc)
	if root == nil {
		root = doc
	}

	md := &mdWriter{resolveImage: resolveImage}
	walkNodes(md, root)
	out := html.UnescapeString(md.sb.String())
	out = mdMultiNewline.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// ---- 内部实现 ----------------------------------------

type mdWriter struct {
	sb           strings.Builder
	resolveImage ImageResolver
}

func (w *mdWriter) write(s string) { w.sb.WriteString(s) }

// getAttr 从节点属性集中取值
func getAttr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// innerText 收集节点子树中的所有文本（跳过 script/style，br 转空格）
func innerText(n *xhtml.Node) string {
	var sb strings.Builder
	var walk func(nn *xhtml.Node)
	walk = func(nn *xhtml.Node) {
		if nn.Type == xhtml.TextNode {
			sb.WriteString(nn.Data)
		} else if nn.Type == xhtml.ElementNode {
			switch nn.Data {
			case "script", "style":
				return
			case "br":
				sb.WriteString(" ")
			default:
				for c := nn.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return sb.String()
}

// walkNodes 递归遍历节点树并输出 Markdown
func walkNodes(w *mdWriter, n *xhtml.Node) {
	if n.Type == xhtml.TextNode {
		w.write(n.Data)
		return
	}
	if n.Type != xhtml.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkNodes(w, c)
		}
		return
	}

	switch n.Data {
	case "script", "style":
		return

	case "br":
		w.write("\n")

	case "hr":
		w.write("\n---\n\n")

	case "p":
		walkChildren(w, n)
		w.write("\n\n")

	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		prefix := strings.Repeat("#", level) + " "
		w.write(prefix + strings.TrimSpace(innerText(n)) + "\n\n")

	case "strong", "b":
		w.write("**")
		walkChildren(w, n)
		w.write("**")

	case "em", "i":
		w.write("*")
		walkChildren(w, n)
		w.write("*")

	case "s", "del", "strike":
		w.write("~~")
		walkChildren(w, n)
		w.write("~~")

	case "code":
		w.write("`")
		walkChildren(w, n)
		w.write("`")

	case "pre":
		w.write("\n```\n")
		// pre 内只取原始文本，不转 MD 标记
		walkPre(w, n)
		if w.sb.Len() > 0 && w.sb.String()[w.sb.Len()-1] != '\n' {
			w.write("\n")
		}
		w.write("```\n\n")

	case "blockquote":
		w.write("\n> ")
		walkChildren(w, n)
		w.write("\n\n")

	case "ul":
		walkChildren(w, n)
		w.write("\n")

	case "ol":
		walkChildren(w, n)
		w.write("\n")

	case "li":
		// v21.82：任务清单项导出为 GFM task list（- [ ] / - [x]）。
		// v21.110 tiptap 3.x 适配：v3 的 taskItem 不再输出 li 上的 data-type="taskItem"
		// （勾选态在 data-checked、列表标记在父 ul 的 data-type="taskList"），
		// 判定放宽为「自带 taskItem（v2 格式）或父 ul 为 taskList（v2/v3 通吃）」；
		// 并跳过 v3 无障碍 label 子树（span 内含 "Task item checkbox for …" 辅助文本，
		// 不跳过会混入导出项正文）。
		if isTaskListItem(n) {
			if getAttr(n, "data-checked") == "true" {
				w.write("- [x] ")
			} else {
				w.write("- [ ] ")
			}
			walkTaskItemChildren(w, n)
		} else {
			w.write("- ")
			walkChildren(w, n)
		}
		w.write("\n")

	case "a":
		href := getAttr(n, "href")
		w.write("[")
		walkChildren(w, n)
		w.write("](" + href + ")")

	case "img":
		src := getAttr(n, "src")
		alt := getAttr(n, "alt")
		width := strings.TrimSpace(getAttr(n, "width"))
		// v2.2.2 R3（用户实测：编辑里缩放过图片，下载 md 未同步）：
		// 纯 Markdown 图片语法没有宽度位 → 带显式宽度的图输出**原生 HTML img**
		// （width 属性跨渲染器通用，见交付说明）。无宽度仍走 Markdown 语法。
		tagged := isPositiveIntStr(width)
		writeImg := func(url string) {
			if tagged {
				w.write(`<img src="` + url + `" alt="` + html.EscapeString(alt) + `" width="` + width + `">`)
				return
			}
			w.write("![" + alt + "](" + url + ")")
		}
		// v2.2.2：resolver 命中 → base64 data URI 内联；未命中/未注入 → 保持原样引用
		if w.resolveImage != nil && src != "" {
			if mime, data, ok := w.resolveImage(src); ok && len(data) > 0 {
				writeImg("data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data))
				return
			}
		}
		writeImg(src)

	case "span", "mark", "u":
		// 样式无 MD 对应，直通行内文本
		walkChildren(w, n)

	case "table":
		// v21.110：GFM pipe 表格导出（补 v19.0 起缺口，与 gomarkdown 的表格导入
		// 形成回环；walkNodes 无此分支时整表退化为散文本）。
		// v2.2.2 R2（用户实测：拖宽能存能上长图、下载 md 却丢列宽）：GFM pipe 语法
		// 无列宽位，带显式列宽的表格整表转储为**原生 HTML 块**（colgroup 宽度原样
		// 保留，主流 MD 渲染器 Typora/VSCode/Obsidian 均支持）；无列宽仍走 pipe。
		// 内容安全由调用方导出前统一消毒（notetext.NoteToMarkdown → notetext.Content）
		// ——同步 push 链路入库不经 Web 消毒，此处绝不能裸转储。
		if hasExplicitColWidth(n) {
			renderTableHTMLBlock(w, n)
		} else {
			exportTableMarkdown(w, n)
		}

	default:
		walkChildren(w, n)
	}
}

func walkChildren(w *mdWriter, n *xhtml.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkNodes(w, c)
	}
}

// isTaskListItem v21.110：li 是否为任务清单项（兼容 v2 的 data-type=taskItem 与
// v3 仅靠父 ul data-type=taskList 标记两种序列化）。
func isTaskListItem(n *xhtml.Node) bool {
	if getAttr(n, "data-type") == "taskItem" {
		return true
	}
	if p := n.Parent; p != nil && p.Type == xhtml.ElementNode && p.Data == "ul" &&
		getAttr(p, "data-type") == "taskList" {
		return true
	}
	return false
}

// walkTaskItemChildren 任务项文本提取：跳过 li 的顶层控件/辅助子树——
// label（v2 直存 / v3 a11y）、input（勾选控件）、顶层 span（v3 a11y 文本；
// 经 bluemonday unwrap 后可能升为 li 直接子元素）。条目正文在 div>p（v2/v3 通吃），
// div 内部继续正常遍历（行内 span 样式文本不受影响）。
func walkTaskItemChildren(w *mdWriter, n *xhtml.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode {
			switch c.Data {
			case "label", "input", "span":
				continue
			}
		}
		walkNodes(w, c)
	}
}

// exportTableMarkdown 表格 → GFM pipe 表。限制（交付说明-150 记录）：
// GFM 不支持合并单元格，colrowspan 合并列展开为空单元格、合并行只输出首行内容；
// 单元格内块级结构（多段落/列表）展平为单行行内文本；表头缺失时补空表头行。
func exportTableMarkdown(w *mdWriter, table *xhtml.Node) {
	type tableRow struct {
		cells    []string
		isHeader bool
	}
	var rows []tableRow
	var collectRows func(n *xhtml.Node)
	collectRows = func(n *xhtml.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != xhtml.ElementNode {
				continue
			}
			switch c.Data {
			case "tr":
				var row []string
				isHeader := false
				for tc := c.FirstChild; tc != nil; tc = tc.NextSibling {
					if tc.Type != xhtml.ElementNode || (tc.Data != "td" && tc.Data != "th") {
						continue
					}
					if tc.Data == "th" {
						isHeader = true
					}
					sub := &mdWriter{}
					walkChildren(sub, tc)
					txt := strings.Join(strings.Fields(strings.ReplaceAll(sub.sb.String(), "\n", " ")), " ")
					txt = strings.ReplaceAll(txt, "|", `\|`)
					row = append(row, txt)
					// 合并列展开（colrowspan="列,行"）
					if cs := getAttr(tc, "colrowspan"); cs != "" {
						if i := strings.Index(cs, ","); i > 0 {
							if v, err := strconv.Atoi(cs[:i]); err == nil {
								for s := 1; s < v; s++ {
									row = append(row, "")
								}
							}
						}
					}
				}
				if len(row) > 0 {
					rows = append(rows, tableRow{row, isHeader})
				}
			case "thead", "tbody", "tfoot":
				collectRows(c)
			}
		}
	}
	collectRows(table)
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, r := range rows {
		if len(r.cells) > cols {
			cols = len(r.cells)
		}
	}
	renderRow := func(r []string) {
		w.write("|")
		for i := 0; i < cols; i++ {
			if i < len(r) {
				w.write(" " + r[i] + " |")
			} else {
				w.write("  |")
			}
		}
		w.write("\n")
	}
	body := rows
	if rows[0].isHeader {
		// 首行即表头（tiptap 默认）
		renderRow(rows[0].cells)
		body = rows[1:]
	} else {
		// 无表头：补空表头行使 GFM 合法
		renderRow(nil)
	}
	w.write("|")
	for i := 0; i < cols; i++ {
		w.write(" --- |")
	}
	w.write("\n")
	for _, r := range body {
		renderRow(r.cells)
	}
	w.write("\n")
}

// walkPre 收集 pre 标签内原始文本（不转 MD），br 换行
func walkPre(w *mdWriter, n *xhtml.Node) {
	if n.Type == xhtml.TextNode {
		w.write(n.Data)
		return
	}
	if n.Data == "br" {
		w.write("\n")
		return
	}
	if n.Type == xhtml.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkPre(w, c)
		}
	}
}

// ---- v2.2.2 R2 表格列宽 → 原生 HTML 块导出 ----------------------------

// colStyleWidthRe col 的 style 中的显式像素宽度（width:Npx；min-width 不算——
// Tiptap 未拖动列恒写 min-width，计为显式宽会把全表误转 HTML）。
var colStyleWidthRe = regexp.MustCompile(`(?:^|;)\s*width\s*:\s*(\d+(?:\.\d+)?)px\s*(?:;|$)`)

// colStylePctRe col 的 style 中的百分比宽度（width:37.5%；R4 起 md 导出/投影形态）
var colStylePctRe = regexp.MustCompile(`(?:^|;)\s*width\s*:\s*(\d+(?:\.\d+)?)%\s*(?:;|$)`)

// cellWidthDeclRe 单元格 style 中已有的 width 声明（追加/覆盖时定位用）
var cellWidthDeclRe = regexp.MustCompile(`(?:^|;)\s*width\s*:\s*[^;]+;?`)

// hasExplicitColWidth 表格是否带显式列宽。四形态全覆盖：
//  1. colgroup > col 的 width 属性（本仓 sanitize 放行的回退通道）
//  2. colgroup > col 的 style width:Npx（Tiptap TableView 原生序列化 / 存量入库形态）
//  3. colgroup > col 的 style width:X%（R4 起 Tiptap 序列化形态）
//  4. th/td 的 colwidth 属性（Tiptap 拖宽单元格序列化，R2 起入库保留）
func hasExplicitColWidth(table *xhtml.Node) bool {
	var scan func(n *xhtml.Node) bool
	scan = func(n *xhtml.Node) bool {
		if n.Type == xhtml.ElementNode {
			switch n.Data {
			case "col":
				if getAttr(n, "width") != "" ||
					colStyleWidthRe.MatchString(getAttr(n, "style")) ||
					colStylePctRe.MatchString(getAttr(n, "style")) {
					return true
				}
			case "td", "th":
				if getAttr(n, "colwidth") != "" {
					return true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if scan(c) {
				return true
			}
		}
		return false
	}
	return scan(table)
}

// renderTableHTMLBlock 带列宽表格整表转储为原生 HTML 块（CommonMark raw HTML
// block，前后空行分隔）。子树重渲染自**已消毒**的入库内容（调用方保证），标签/
// 属性不再手工过滤——转义由 x/net/html Render 统一处理。渲染失败退回 pipe 表
// （不阻断导出，仅丢列宽，与旧版行为一致）。
//
// v2.2.2 R4（方案 A）：列宽投影为**百分比**并让表格 fixed + 100% 宽——
// 与 Web 编辑/预览/长图同一口径（「未拖列」按已拖列均值补齐），
// 这样 md 在任意查看器/任意窗口下比例都与预览一致，不再依赖容器宽度。
func renderTableHTMLBlock(w *mdWriter, table *xhtml.Node) {
	pct := tableColPercents(table)
	if len(pct) > 0 {
		applyTablePercents(table, pct)
	}
	var sb strings.Builder
	if err := xhtml.Render(&sb, table); err != nil {
		exportTableMarkdown(w, table)
		return
	}
	w.write("\n" + strings.TrimSpace(sb.String()) + "\n\n")
}

// isPositiveIntStr 是否为正整数串（列宽/图宽属性值域校验）
func isPositiveIntStr(s string) bool {
	if s == "" || len(s) > 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// setAttr 设置/覆盖节点属性（渲染前同步单元格宽度用）
func setAttr(n *xhtml.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, xhtml.Attribute{Key: key, Val: val})
}

// removeAttr 删除节点属性（不存在则无操作）
func removeAttr(n *xhtml.Node, key string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			return
		}
	}
}

// colWidthOf 单列显式宽度（px）：width 属性优先（sanitize 放行的回退通道），
// 否则取 style 的 width:Npx；无显式宽度返回 0（min-width / 百分比不计）。
func colWidthOf(col *xhtml.Node) int {
	if v := strings.TrimSpace(getAttr(col, "width")); isPositiveIntStr(v) {
		n, _ := strconv.Atoi(v)
		return n
	}
	if m := colStyleWidthRe.FindStringSubmatch(getAttr(col, "style")); m != nil {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil && f > 0 {
			return int(f + 0.5)
		}
	}
	return 0
}

// colPctOf 单列已存的百分比宽度（`style: width:37.5%`）；无则 0。
func colPctOf(col *xhtml.Node) float64 {
	if m := colStylePctRe.FindStringSubmatch(getAttr(col, "style")); m != nil {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil && f > 0 {
			return f
		}
	}
	return 0
}

// colNodesOf 取 colgroup 的 col 节点（无则 nil）。
func colNodesOf(table *xhtml.Node) []*xhtml.Node {
	var cols []*xhtml.Node
	for c := table.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != xhtml.ElementNode || c.Data != "colgroup" {
			continue
		}
		for col := c.FirstChild; col != nil; col = col.NextSibling {
			if col.Type == xhtml.ElementNode && col.Data == "col" {
				cols = append(cols, col)
			}
		}
	}
	return cols
}

// tableColPercents 把 colgroup 列宽换算为百分比（合计 100，1 位小数）。
//
// 与前端 `utils/tableColPct.js` **同一规则**（跨端一致，改一处必须同步另一处）：
//   - 全部为百分比 → 归一化到 100；
//   - 存量 px（含部分列未指定）→ **未指定列按已指定列平均值补齐**再算比例
//     （不用 min-width 25px，否则窄容器里该列会被压成竖排单字——用户 R4 实测）；
//   - 无任何显式宽度 → nil（保持自适应布局）。
func tableColPercents(table *xhtml.Node) []float64 {
	cols := colNodesOf(table)
	if len(cols) == 0 {
		return nil
	}
	vals := make([]float64, len(cols))
	hasPct, hasPx, explicit := 0, 0, 0
	for i, col := range cols {
		if p := colPctOf(col); p > 0 {
			vals[i] = p
			hasPct++
			explicit++
			continue
		}
		if w := colWidthOf(col); w > 0 {
			vals[i] = float64(w)
			hasPx++
			explicit++
		}
	}
	if explicit == 0 {
		return nil
	}
	// 纯百分比形态：直接归一化；否则按 px 均值补齐（混合形态以数值近似处理）
	if hasPct == 0 || hasPx > 0 {
		var sumExplicit float64
		for _, v := range vals {
			if v > 0 {
				sumExplicit += v
			}
		}
		avg := sumExplicit / float64(explicit)
		for i := range vals {
			if vals[i] <= 0 {
				vals[i] = avg
			}
		}
	}
	var total float64
	for _, v := range vals {
		total += v
	}
	if total <= 0 {
		return nil
	}
	pct := make([]float64, len(vals))
	for i, v := range vals {
		pct[i] = math.Round(v/total*1000) / 10
	}
	// 舍入差额并入最大列，保证合计 100.0
	var sum float64
	maxIdx := 0
	for i, p := range pct {
		sum += p
		if p > pct[maxIdx] {
			maxIdx = i
		}
	}
	if drift := math.Round((100-sum)*10) / 10; drift != 0 {
		pct[maxIdx] = math.Round((pct[maxIdx]+drift)*10) / 10
	}
	return pct
}

// applyTablePercents 渲染前把表格改为「百分比列宽 + fixed + 100% 宽」：
//  1. colgroup 每列 style 写 `width: X%`；
//  2. 单个跨度的 th/td 在 style 末尾追加 `width: X%`（部分渲染器忽略 colgroup，
//     单元格 style 是第二通道；跨列单元格无法用单一宽度表达，跳过）；
//  3. table 自身写 `table-layout: fixed; width: 100%`，保证任意容器下同比例。
func applyTablePercents(table *xhtml.Node, pct []float64) {
	cols := colNodesOf(table)
	if len(cols) != len(pct) {
		return
	}
	for i, col := range cols {
		setAttr(col, "style", fmt.Sprintf("width: %s%%", formatPct(pct[i])))
		// 清掉旧的 px width 属性，避免与百分比双声明（渲染器取值顺序不确定）
		removeAttr(col, "width")
	}
	setAttr(table, "style", "table-layout: fixed; width: 100%")

	var walkRows func(n *xhtml.Node)
	walkRows = func(n *xhtml.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != xhtml.ElementNode {
				continue
			}
			switch c.Data {
			case "tr":
				col := 0
				for cell := c.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type != xhtml.ElementNode || (cell.Data != "td" && cell.Data != "th") {
						continue
					}
					span := 1
					if v := getAttr(cell, "colspan"); v != "" {
						if n, err := strconv.Atoi(v); err == nil && n > 0 {
							span = n
						}
					}
					if span == 1 && col < len(pct) && pct[col] > 0 {
						appendCellWidthStyle(cell, pct[col])
					}
					col += span
				}
			case "thead", "tbody", "tfoot":
				walkRows(c)
			}
		}
	}
	walkRows(table)
}

// appendCellWidthStyle 给单元格 style 追加/覆盖 width 声明（保留已有对齐等声明）。
func appendCellWidthStyle(cell *xhtml.Node, pct float64) {
	style := strings.TrimSpace(getAttr(cell, "style"))
	decl := "width: " + formatPct(pct) + "%"
	if style == "" {
		setAttr(cell, "style", decl)
		return
	}
	// 已存在 width 声明则替换（避免与旧 px 宽度并存）
	if cellWidthDeclRe.MatchString(style) {
		setAttr(cell, "style", cellWidthDeclRe.ReplaceAllString(style, decl))
		return
	}
	setAttr(cell, "style", strings.TrimRight(style, ";")+"; "+decl)
}

// formatPct 百分比格式化：去掉多余的 .0（37.5 / 25）
func formatPct(p float64) string {
	s := strconv.FormatFloat(p, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}
