package htmltext

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestToMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string // 必须包含的内容
		not  []string // 不应包含的内容
	}{
		{
			"空输入",
			"",
			[]string{},
			nil,
		},
		{
			"标题层级",
			"<h1>一级</h1><h2>二级</h2><h3>三级</h3>",
			[]string{"# 一级", "## 二级", "### 三级"},
			[]string{"h1", "h2", "h3"},
		},
		{
			"段落",
			"<p>一段文字</p><p>二段文字</p>",
			[]string{"一段文字", "二段文字"},
			[]string{"<p>", "</p>"},
		},
		{
			"加粗与斜体",
			"<p><strong>粗</strong><em>斜</em><b>粗2</b><i>斜2</i></p>",
			[]string{"**粗**", "*斜*", "**粗2**", "*斜2*"},
			[]string{"<strong>", "<em>"},
		},
		{
			"删除线与代码",
			"<p><s>删</s><code>var x=1</code></p>",
			[]string{"~~删~~", "`var x=1`"},
			nil,
		},
		{
			"链接与图片",
			`<p><a href="https://example.com">链接</a><img src="/uploads/a.png" alt="图" /></p>`,
			[]string{"[链接](https://example.com)", `![图](/uploads/a.png)`},
			[]string{"<a ", "<img "},
		},
		{
			"无序列表",
			"<ul><li>甲</li><li>乙</li></ul>",
			[]string{"- 甲", "- 乙"},
			[]string{"<ul>", "<li>"},
		},
		{
			"有序列表",
			"<ol><li>第一步</li><li>第二步</li></ol>",
			[]string{"- 第一步", "- 第二步"},
			[]string{"<ol>"},
		},
		{
			"代码块",
			"<pre><code>func main() {\n  fmt.Println(1)\n}</code></pre>",
			[]string{"```", "func main", "fmt.Println"},
			[]string{"<pre>", "<code>"},
		},
		{
			"引用块",
			"<blockquote><p>引用文</p></blockquote>",
			[]string{"> 引用文"},
			[]string{"<blockquote>"},
		},
		{
			"分隔线",
			"<hr>",
			[]string{"---"},
			nil,
		},
		{
			"脚本被剥离",
			"<script>alert(1)</script><p>安全</p>",
			[]string{"安全"},
			[]string{"alert", "script"},
		},
		{
			"span 直通行内文字",
			"<p><span style=\"color:red\">红字</span></p>",
			[]string{"红字"},
			[]string{"style", "span", "color"},
		},
		{
			"嵌套内联",
			"<p><strong><em>粗斜</em></strong></p>",
			[]string{"***粗斜***"},
			nil,
		},
		{
			"任务清单导出 GFM task list",
			`<ul data-type="taskList"><li data-type="taskItem" data-checked="true"><label><input type="checkbox" checked></label><div><p>已完成</p></div></li><li data-type="taskItem" data-checked="false"><label><input type="checkbox"></label><div><p>未完成</p></div></li></ul>`,
			[]string{"- [x] 已完成", "- [ ] 未完成"},
			[]string{"data-type", "checkbox"},
		},
		{
			// v21.110 tiptap 3.x 序列化：li 无 data-type=taskItem，靠父 ul taskList + data-checked；
			// label 内 a11y span（"Task item checkbox for …"）必须被跳过，不得混入导出正文。
			"任务清单导出 GFM(v3 序列化兼容)",
			`<ul data-type="taskList"><li data-checked="true"><label contenteditable="false"><input type="checkbox" checked><span style="position: absolute; width: 1px; clip: rect(0px, 0px, 0px, 0px);">Task item checkbox for 已完成</span></label><div><p>已完成</p></div></li><li data-checked="false"><label contenteditable="false"><input type="checkbox"><span style="position: absolute;">Task item checkbox for 未完成</span></label><div><p>未完成</p></div></li></ul>`,
			[]string{"- [x] 已完成", "- [ ] 未完成"},
			[]string{"Task item checkbox", "checkbox"},
		},
		{
			// 真实入库形态：bluemonday unwrap label 后 input/span 升为 li 直接子元素
			"任务清单导出 GFM(v3 入库 unwrap 形态)",
			`<ul data-type="taskList"><li data-checked="true"><input type="checkbox" checked><span style="position: absolute; clip: rect(0px, 0px, 0px, 0px);">Task item checkbox for 已完成</span><div><p>已完成</p></div></li><li data-checked="false"><input type="checkbox"><span style="position: absolute;">Task item checkbox for 未完成</span><div><p><strong>带粗体</strong>的未完成项</p></div></li></ul>`,
			[]string{"- [x] 已完成", "- [ ] **带粗体**的未完成项"},
			[]string{"Task item checkbox"},
		},
		{
			// v21.110：表格 GFM pipe 导出（补 v19.0 起缺口）。
			// v2.2.2 R2：无显式列宽仍走 pipe（colgroup 仅有 min-width 不算显式宽）。
			"表格导出 GFM pipe(含表头)",
			`<table><colgroup><col style="min-width: 25px;"><col style="min-width: 25px;"></colgroup><thead><tr><th><p>A</p></th><th><p>B</p></th></tr></thead><tbody><tr><td><p>1</p></td><td><p><strong>粗</strong></p></td></tr></tbody></table>`,
			[]string{"| A | B |", "| --- | --- |", "| 1 | **粗** |"},
			[]string{"<td"},
		},
		{
			// v2.2.2 R2（用户实测：拖宽下载 md 丢列宽）：带显式列宽的表格整表转储为
			// 原生 HTML 块（GFM pipe 无列宽语法）。
			// v2.2.2 R4（方案 A）：列宽投影为**百分比**（合计 100，未拖列按已拖列均值补齐），
			// 表格 fixed + 100% 宽 → md 任意查看器/窗口下与预览同比例。
			"表格导出列宽→原生HTML块(col style 形态)",
			`<table><colgroup><col style="width: 180px;"><col style="min-width: 25px;"></colgroup><thead><tr><th colwidth="180"><p>A</p></th><th><p>B</p></th></tr></thead><tbody><tr><td colwidth="180"><p>1</p></td><td><p><strong>粗</strong></p></td></tr></tbody></table>`,
			[]string{`<table style="table-layout: fixed; width: 100%">`, "<colgroup>", `<col style="width: 50%"/>`, `<th colwidth="180" style="width: 50%">`, `<td colwidth="180" style="width: 50%"><p>1</p>`, "<strong>粗</strong>"},
			[]string{"| A |", "width: 180px"},
		},
		{
			// v2.2.2 R2：col width 属性形态（sanitize 放行的回退通道）同样触发 HTML 块
			"表格导出列宽→原生HTML块(col width 属性形态)",
			`<table><colgroup><col width="180"><col></colgroup><tbody><tr><td><p>x</p></td><td><p>y</p></td></tr></tbody></table>`,
			[]string{`<table style="table-layout: fixed; width: 100%">`, `<col style="width: 50%"/>`},
			nil,
		},
		{
			// v2.2.2 R3（用户实测：编辑里缩放过图片，下载 md 未同步）：
			// 纯 Markdown 图片语法没有宽度位 → 带显式宽度的图输出原生 HTML img（width 属性）
			"图片宽度→原生HTML img",
			`<p>x</p><img src="/uploads/a.png" alt="图" width="240">`,
			[]string{`<img src="/uploads/a.png" alt="图" width="240">`},
			[]string{"![图]("},
		},
		{
			// 无宽度图片保持 Markdown 语法（不无谓改动存量导出形态）
			"无宽度图片保持Markdown语法",
			`<p>x</p><img src="/uploads/a.png" alt="图">`,
			[]string{"![图](/uploads/a.png)"},
			[]string{"<img"},
		},
		{
			// v2.2.2 R4：三列（300px / width 属性 240 / 仅 min-width）→ 未拖列按均价补齐，
			// 百分比 37.1 / 29.6 / 33.3；单元格 style 与 colgroup 双写（部分渲染器忽略 colgroup）
			"表格列宽→百分比(未拖列按均值补齐)",
			`<table><colgroup><col style="width: 300px;"><col width="240"><col style="min-width: 25px;"></colgroup><thead><tr><th><p>A</p></th><th><p>B</p></th><th><p>C</p></th></tr></thead><tbody><tr><td><p>1</p></td><td><p>2</p></td><td><p>3</p></td></tr></tbody></table>`,
			[]string{`<col style="width: 37.1%"/>`, `<col style="width: 29.6%"/>`, `<col style="width: 33.3%"/>`, `<th style="width: 37.1%">`, `<td style="width: 29.6%">`},
			[]string{"| A |"},
		},
		{
			// tiptap 默认首行为 th；用户删表头后无 th——补空表头行保证 GFM 合法
			"表格导出无表头补空表头",
			`<table><tbody><tr><td colrowspan="1,1"><p>x</p></td><td><p>y</p></td></tr></tbody></table>`,
			[]string{"|  |  |", "| --- | --- |", "| x | y |"},
			nil,
		},
		{
			// GFM 不支持合并单元格：colrowspan="3,1" 展开为 1 内容格 + 2 空格
			"表格导出合并列展开",
			`<table><tbody><tr><th colrowspan="3,1"><p>跨三列</p></th></tr><tr><td colrowspan="1,1"><p>a</p></td><td><p>b</p></td><td><p>c</p></td></tr></tbody></table>`,
			[]string{"| 跨三列 |  |  |", "| a | b | c |"},
			nil,
		},
		{
			"表格单元格竖线转义",
			`<table><tbody><tr><th><p>H</p></th></tr><tr><td><p>a|b</p></td></tr></tbody></table>`,
			[]string{`a\|b`},
			nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := ToMarkdown(tc.in)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("缺期望内容 %q\n输出: %s", w, out)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(out, n) {
					t.Errorf("不应包含 %q\n输出: %s", n, out)
				}
			}
		})
	}
}

// v2.2.2：MD 导出图片 base64 嵌入（ToMarkdownWithImages）——
// /uploads 相对引用脱离应用不可达（且带鉴权），嵌入后单文件自包含

func TestToMarkdownWithImages(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G'}
	resolver := func(src string) (string, []byte, bool) {
		switch src {
		case "/uploads/a.png":
			return "image/png", pngBytes, true
		default:
			return "", nil, false
		}
	}

	t.Run("命中 resolver 输出 base64 data URI", func(t *testing.T) {
		out := ToMarkdownWithImages(`<p><img src="/uploads/a.png" alt="图"></p>`, resolver)
		want := "![图](data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes) + ")"
		if !strings.Contains(out, want) {
			t.Errorf("缺 base64 嵌入 %q\n输出: %s", want, out)
		}
		if strings.Contains(out, "/uploads/a.png") {
			t.Errorf("不应保留原引用\n输出: %s", out)
		}
	})

	t.Run("未命中保持原样引用（不阻断导出）", func(t *testing.T) {
		out := ToMarkdownWithImages(`<p><img src="/uploads/missing.png" alt="缺"></p>`, resolver)
		if !strings.Contains(out, `![缺](/uploads/missing.png)`) {
			t.Errorf("缺原样引用\n输出: %s", out)
		}
		if strings.Contains(out, "data:") {
			t.Errorf("不应出现 data URI\n输出: %s", out)
		}
	})

	t.Run("nil resolver 退化为 ToMarkdown", func(t *testing.T) {
		out := ToMarkdownWithImages(`<p><img src="/uploads/a.png" alt="图"></p>`, nil)
		if !strings.Contains(out, `![图](/uploads/a.png)`) {
			t.Errorf("nil resolver 应保持原引用\n输出: %s", out)
		}
	})

	t.Run("alt 文本随嵌入保留", func(t *testing.T) {
		out := ToMarkdownWithImages(`<p><img src="/uploads/a.png" alt="架构图"></p>`, resolver)
		if !strings.Contains(out, "![架构图](data:image/png;base64,") {
			t.Errorf("alt 应保留\n输出: %s", out)
		}
	})
}
