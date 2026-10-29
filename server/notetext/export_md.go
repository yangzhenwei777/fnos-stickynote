package notetext

import (
	"strings"

	"stickynote/htmltext"
)

// export_md.go —— 单篇笔记 → Markdown 的统一出口（v2.2.2 R2 自 handler/buildNoteMD
// 下沉）。服务端单篇/全库/批量导出与桌面导出绑定共用本实现，防止「标题/标签头 +
// 消毒 + 转换」三步在双宿主间漂移（下沉纪律同 notetext 包头注）。
//
// 消毒在此统一收口的原因：v2.2.2 R2 起，带列宽表格在 MD 导出中以**原生 HTML 块**
// 转储（GFM pipe 无列宽语法），而同步 push 链路的入库内容不经 Web 消毒——若导出前
// 不消毒，被推入的恶意 HTML 会随 .md 文件带出到外部渲染器执行（fail-closed）。
// 已消毒内容再次消毒幂等，干净内容零变化。

// NoteToMarkdown 生成单篇笔记的 Markdown：标题 + 标签元信息 + 正文。
// 不加 YAML frontmatter（用户确认），纯 MD 正文互操作最佳。
// resolveImage 传 nil 时图片保持原样引用（bench/测试口径）。
func NoteToMarkdown(title, tags, content string, resolveImage htmltext.ImageResolver) string {
	var sb strings.Builder
	sb.WriteString("# " + title + "\n\n")
	if strings.TrimSpace(tags) != "" {
		sb.WriteString("**标签：** " + tags + "\n\n")
	} else {
		sb.WriteString("\n")
	}
	sb.WriteString(htmltext.ToMarkdownWithImages(Content(content), resolveImage))
	sb.WriteString("\n")
	return sb.String()
}
