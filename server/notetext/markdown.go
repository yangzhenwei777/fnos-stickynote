package notetext

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

// allowedExts 在线导入允许的文件扩展名（小写、含点）。
var allowedExts = map[string]bool{
	".md":   true,
	".txt":  true,
	".html": true,
	".htm":  true,
}

// AllowedExt 判断扩展名是否可导入（内部统一转小写，调用方无需预处理）。
func AllowedExt(ext string) bool {
	return allowedExts[strings.ToLower(ext)]
}

// ExtOf 取文件名的规范扩展名（小写、含点）。
func ExtOf(filename string) string {
	return strings.ToLower(filepath.Ext(filename))
}

// ExtractTitle 从文本中提取标题：首个 # 标题 → 文件名去扩展名。
func ExtractTitle(text string, filename string) string {
	re := regexp.MustCompile(`(?m)^#\s+(.+)$`)
	if m := re.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	name := filepath.Base(filename)
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// TextToHTML 纯文本 → 按空行分段，<p> 包裹。
func TextToHTML(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var paras []string
	var buf []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			if len(buf) > 0 {
				paras = append(paras, "<p>"+strings.Join(buf, "<br>")+"</p>")
				buf = nil
			}
		} else {
			buf = append(buf, EscapeHTML(t))
		}
	}
	if len(buf) > 0 {
		paras = append(paras, "<p>"+strings.Join(buf, "<br>")+"</p>")
	}
	if len(paras) == 0 {
		return "<p></p>"
	}
	return strings.Join(paras, "\n")
}

// EscapeHTML 最小 HTML 转义（& < >）。
func EscapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// MarkdownToHTML 把 Markdown 渲染为 HTML（含 GFM 任务清单直构，见 preprocessGFMTaskLists）。
// 输出**未经消毒**，调用方须再过 Content()。
func MarkdownToHTML(text string) string {
	// v21.111（挂账①）：gomarkdown 无 GFM task-list 扩展（v2 起既有缺口），`- [x]` 按普通
	// 列表文本导入。改为前置直构：任务清单行在进入 gomarkdown 前转成 tiptap 同构的
	// taskList HTML 块（v3 序列化形态，2.x 编辑器载入亦归一化兼容，I2 实证），其余文本
	// 照常解析——导出↔导入任务清单回环打通。
	text = preprocessGFMTaskLists(text)

	extensions := parser.CommonExtensions |
		parser.AutoHeadingIDs |
		parser.Strikethrough |
		parser.Tables |
		parser.FencedCode |
		parser.Autolink |
		parser.Footnotes

	p := parser.NewWithExtensions(extensions)

	opts := html.RendererOptions{
		Flags: html.CommonFlags | html.HrefTargetBlank,
	}
	renderer := html.NewRenderer(opts)

	doc := p.Parse([]byte(text))
	return string(markdown.Render(doc, renderer))
}

// taskLine 一条已识别的 GFM 任务清单行。
type taskLine struct {
	indent int    // 行首空白列数（决定嵌套层级）
	done   bool   // [x]/[X] 勾选态
	inline string // 剩余行内 markdown（未转义，交 gomarkdown 行内渲染）
}

var gfmTaskLineRe = regexp.MustCompile(`^([ \t]*)[-*+][ \t]+\[([xX ])\](?:[ \t]+(.*))?$`)

// preprocessGFMTaskLists 把连续的 GFM 任务清单行聚组直构为 taskList HTML 块。
// 规则：① 仅当组内行全部是任务行（普通列表行/空行/其他内容即刻断组回退 gomarkdown，
// 不做混合列表拆解——偏差已在交付说明记录）；② 围栏代码块（``` / ~~~）内不转换；
// ③ 缩进栈支持嵌套子清单；④ 行内格式（粗体/链接等）经 gomarkdown 行内渲染保留。
func preprocessGFMTaskLists(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines)+8)
	var group []taskLine
	inFence := false
	var fenceMark string

	flush := func() {
		if len(group) == 0 {
			return
		}
		// 空行分隔：防止 gomarkdown 把紧邻的 HTML 块并入其正打开的普通列表 li
		out = append(out, "")
		out = append(out, renderTaskListGroup(group))
		group = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		// 围栏状态机：``` / ~~~ 开关（GFM fenced code block 内不转换）
		st := strings.TrimLeft(trimmed, " \t")
		if len(st) >= 3 && (strings.HasPrefix(st, "```") || strings.HasPrefix(st, "~~~")) {
			mark := st[:3]
			if !inFence {
				inFence, fenceMark = true, mark
			} else if strings.HasPrefix(mark, fenceMark) {
				inFence = false
			}
			flush()
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}

		if m := gfmTaskLineRe.FindStringSubmatch(trimmed); m != nil {
			group = append(group, taskLine{
				indent: len(m[1]),
				done:   m[2] == "x" || m[2] == "X",
				inline: strings.TrimSpace(m[3]),
			})
			continue
		}
		// 非任务行：断组
		flush()
		out = append(out, line)
	}
	flush()
	return strings.Join(out, "\n")
}

// renderTaskListGroup 把一组任务行按缩进递归构建任务树并序列化为嵌套 taskList HTML。
func renderTaskListGroup(group []taskLine) string {
	var sb strings.Builder
	sb.WriteString("<ul data-type=\"taskList\">\n")
	sb.WriteString(renderTaskNodes(buildTaskTree(group)))
	sb.WriteString("</ul>\n")
	return sb.String()
}

// taskNode 任务树节点：缩进比父深的连续行聚合为其子级。
type taskNode struct {
	t     taskLine
	child []taskNode
}

// buildTaskTree 递归下降：base 为当前层级父缩进，子级 = 缩进严格大于父项的连续段。
func buildTaskTree(items []taskLine) []taskNode {
	var nodes []taskNode
	i := 0
	for i < len(items) {
		cur := items[i]
		node := taskNode{t: cur}
		i++
		if i < len(items) && items[i].indent > cur.indent {
			j := i
			for j < len(items) && items[j].indent > cur.indent {
				j++
			}
			node.child = buildTaskTree(items[i:j])
			i = j
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func renderTaskNodes(nodes []taskNode) string {
	var sb strings.Builder
	for _, n := range nodes {
		checked := "false"
		if n.t.done {
			checked = "true"
		}
		// v3 TaskItem parseHTML gate = li[data-type="taskItem"]（renderHTML 亦输出该属性），
		// 缺失则编辑器载入降级普通列表（UI 探针实证），必须与 v3 序列化对齐
		sb.WriteString(`<li data-type="taskItem" data-checked="` + checked + `"><div><p>`)
		sb.WriteString(renderInlineMD(n.t.inline))
		sb.WriteString("</p></div>")
		if len(n.child) > 0 {
			sb.WriteString("\n<ul data-type=\"taskList\">\n")
			sb.WriteString(renderTaskNodes(n.child))
			sb.WriteString("</ul>\n")
		}
		sb.WriteString("</li>\n")
	}
	return sb.String()
}

// renderInlineMD 单行行内 markdown → 行内 HTML（剥段落包装）。
// 任务项内容沿用 gomarkdown 行内语法（粗体/斜体/链接/删除线等），与正文其他部分一致。
func renderInlineMD(s string) string {
	if s == "" {
		return ""
	}
	extensions := parser.CommonExtensions | parser.Strikethrough | parser.Autolink
	p := parser.NewWithExtensions(extensions)
	renderer := html.NewRenderer(html.RendererOptions{Flags: html.CommonFlags})
	out := string(markdown.Render(p.Parse([]byte(s)), renderer))
	out = strings.TrimSpace(out)
	// 单行 → 必为 <p>…</p>；防御性处理：无 p 包装时原样返回
	if strings.HasPrefix(out, "<p>") && strings.HasSuffix(out, "</p>") {
		return out[3 : len(out)-4]
	}
	return out
}
