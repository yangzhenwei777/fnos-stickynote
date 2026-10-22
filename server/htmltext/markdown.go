package htmltext

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

var mdMultiNewline = regexp.MustCompile(`\n{3,}`)

// ToMarkdown 把 tiptap 产出的笔记 HTML 转为 Markdown。
func ToMarkdown(htmlStr string) string {
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

	md := &mdWriter{}
	walkNodes(md, root)
	out := html.UnescapeString(md.sb.String())
	out = mdMultiNewline.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// ---- 内部实现 ----------------------------------------

type mdWriter struct {
	sb strings.Builder
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
		w.write("![" + alt + "](" + src + ")")

	case "span", "mark", "u":
		// 样式无 MD 对应，直通行内文本
		walkChildren(w, n)

	case "table":
		// v21.110：GFM pipe 表格导出（补 v19.0 起缺口，与 gomarkdown 的表格导入
		// 形成回环；walkNodes 无此分支时整表退化为散文本）
		exportTableMarkdown(w, n)

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
