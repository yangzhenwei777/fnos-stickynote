package notetext

import (
	"errors"
	"strings"
)

// ErrUnsupportedExt 扩展名不在导入白名单（.md/.txt/.html/.htm）内。
var ErrUnsupportedExt = errors.New("notetext: unsupported file extension")

// MaxTitleRunes 标题最大字符数（与 POST/PUT /notes 的 sanitizeNoteTitle 截断口径一致）。
const MaxTitleRunes = 200

// FallbackTitle 标题为空时的兜底标题。
const FallbackTitle = "未命名"

// SizeLimitForConfig 按【单篇笔记上限】配置换算导入文件硬上限（字节）。
//
// v21.55 口径（在线导入原实现）：跟随 max_note_content_kb（0=不限）。
// 导入文件内容 ≈ 笔记内容大小（无 JSON 转义余量），故 X>0 直接用 X 字节；
// X=0 → 100MB 兜底（与业务硬上限一致）。范围收敛到 [16MB, 128MB]（v21.90 N-04）。
//
// 下沉理由：服务端与桌面离线导入必须用同一个上限，否则同一份文件在线能导、离线报错。
func SizeLimitForConfig(maxNoteContentKB int) int64 {
	const (
		defaultLimit = 100 << 20
		minLimit     = 16 << 20
		maxLimit     = 128 << 20
	)
	if maxNoteContentKB <= 0 {
		return defaultLimit
	}
	limit := int64(maxNoteContentKB) * 1024
	if limit < minLimit {
		return minLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// Convert 把「文件名 + 原始字节」转成可入库的 (标题, 已消毒 HTML 内容)。
//
// 这是**唯一**的导入转换入口：服务端 handler.ImportFile 与桌面
// LocalService.ImportNote 都走它，保证在线/离线两条路径产出完全一致。
// 配额判定、落库、事件产生（ChangeLog）由各自宿主负责，不在本函数内。
//
// 返回标题已做 sanitize（剥离控制字符）并按 MaxTitleRunes 截断。
func Convert(filename string, raw []byte) (title string, htmlContent string, err error) {
	ext := ExtOf(filename)
	if !AllowedExt(ext) {
		return "", "", ErrUnsupportedExt
	}

	text := string(raw)

	// v21.90 P0（M-02 审计核对）：导入标题与创建路径（note.go sanitizeNoteTitle）对齐——
	// 此前 Markdown 首个 # 标题 / 原始文件名未经消毒直接入库，绕过 v21.79 标题 XSS 修复；
	// 且标题不受 max=200 约束。现补消毒 + 200 字符截断。
	title = Title(ExtractTitle(text, filename))
	if strings.TrimSpace(title) == "" {
		title = Title(FallbackTitle)
	}
	if runes := []rune(title); len(runes) > MaxTitleRunes {
		title = string(runes[:MaxTitleRunes])
	}

	switch ext {
	case ".md":
		htmlContent = Content(MarkdownToHTML(text))
	case ".txt":
		htmlContent = TextToHTML(text) // EscapeHTML 已转义，安全
	default: // .html / .htm
		htmlContent = Content(text) // 复用站内统一策略
	}
	return title, htmlContent, nil
}
