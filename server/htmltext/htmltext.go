// Package htmltext 将 tiptap 产出的笔记 HTML 转为可读纯文本。
// 用途：① 笔记导出 TXT；② FTS5 全文索引的 content_text（剥标签后的纯文本）。
package htmltext

import (
	"html"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
)

var multiNewline = regexp.MustCompile(`\n{3,}`)
var multiSpace = regexp.MustCompile(`[ \t]{2,}`)

// ToText 把 HTML 转为纯文本：
//   - p / h1-h6 / blockquote / pre：段落前后空行
//   - li："- " 前缀，br：换行，hr："---"
//   - img："[图片]"，a：保留文字（丢弃链接地址）
//   - 其他标签剥除留文字；合并多余空行与空格
func ToText(htmlStr string) string {
	if htmlStr == "" {
		return ""
	}
	doc, err := xhtml.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return ""
	}
	var sb strings.Builder
	var walk func(n *xhtml.Node)
	blockEnd := func(n *xhtml.Node, extra string) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		sb.WriteString(extra)
	}
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			sb.WriteString(n.Data)
			return
		}
		if n.Type != xhtml.ElementNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}
		switch n.Data {
		case "script", "style":
			return // 整体丢弃（内容不可见）
		case "br":
			sb.WriteString("\n")
		case "hr":
			sb.WriteString("\n---\n")
		case "img":
			sb.WriteString("[图片]")
		case "li":
			sb.WriteString("- ")
			blockEnd(n, "\n")
		case "p", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "pre":
			sb.WriteString("\n")
			blockEnd(n, "\n\n")
		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
	}
	walk(doc)

	out := html.UnescapeString(sb.String())
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(multiSpace.ReplaceAllString(l, " "), " ")
	}
	out = strings.TrimSpace(strings.Join(lines, "\n"))
	out = multiNewline.ReplaceAllString(out, "\n\n")
	return out
}
