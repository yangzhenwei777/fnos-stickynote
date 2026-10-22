package htmltext

import (
	"strings"
	"testing"
)

func BenchmarkToMarkdownSmall(b *testing.B) {
	html := `<p>这是一段简单的文本。</p><ul><li>项目一</li><li>项目二</li></ul>`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ToMarkdown(html)
	}
}

func BenchmarkToMarkdownLarge(b *testing.B) {
	// 模拟一篇 50KB 的大笔记（富文本 + 表格 + 代码块）
	p := `<p>` + strings.Repeat("这是一段长文本内容，用于测试 HTML 到 Markdown 转换的性能。", 200) + `</p>`
	table := `<table><tr><th>列A</th><th>列B</th><th>列C</th></tr>` +
		strings.Repeat(`<tr><td>数据1</td><td>数据2</td><td>数据3</td></tr>`, 100) + `</table>`
	code := `<pre><code class="language-go">` +
		strings.Repeat(`func main() { fmt.Println("hello world") }`+"\n", 100) + `</code></pre>`
	html := p + table + code
	b.SetBytes(int64(len(html)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ToMarkdown(html)
	}
}

func BenchmarkToText(b *testing.B) {
	html := `<h1>标题</h1><p>` + strings.Repeat("正文内容。", 500) + `</p>`
	b.SetBytes(int64(len(html)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ToText(html)
	}
}
