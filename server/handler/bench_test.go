package handler

import (
	"stickynote/model"
	"stickynote/notetext"
	"strings"
	"testing"
)

func BenchmarkBuildNoteMD(b *testing.B) {
	n := &model.Note{
		ID:    1,
		Title: "性能基准测试笔记",
		Content: `<h1>大笔记性能测试</h1><p>` +
			strings.Repeat("正文内容正文内容正文内容。", 1000) +
			`</p><ul>` + strings.Repeat(`<li>项目</li>`, 200) + `</ul>`,
		Tags: "基准,性能,markdown",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildNoteMD(n, nil) // v2.2.2：nil resolver 保持 bench 原口径（不触发图片读盘）
	}
}

func BenchmarkSanitizeFilename(b *testing.B) {
	names := []string{
		"正常文件名.md",
		"包含/斜杠:冒号*星号?问号的笔记.md",
		strings.Repeat("超长文件名", 30) + ".md",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = notetext.SanitizeExportFilename(names[i%len(names)])
	}
}
