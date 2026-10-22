package htmltext

import (
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
			// v21.110：表格 GFM pipe 导出（补 v19.0 起缺口）
			"表格导出 GFM pipe(含表头)",
			`<table><thead><tr><th colwidth="120"><p>A</p></th><th><p>B</p></th></tr></thead><tbody><tr><td><p>1</p></td><td><p><strong>粗</strong></p></td></tr></tbody></table>`,
			[]string{"| A | B |", "| --- | --- |", "| 1 | **粗** |"},
			[]string{"colwidth", "<td"},
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
