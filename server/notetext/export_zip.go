package notetext

// export_zip.go — 笔记导出 zip 的统一构造（v2.2.2 R6 下沉）。
//
// 背景：Web 端「导出全部 / 批量导出」在 handler 内流式写 zip；桌面端同一功能此前
// 完全未接线（LocalTransport 501）。R6 起桌面绑定直接落盘，两宿主必须共用同一份
// 文件命名/去重/转换实现，否则标题清洗与重名规则会出现双源漂移。
// 本文件零 gin 依赖，仅做「笔记切片 → zip 条目」。

import (
	"archive/zip"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"stickynote/htmltext"
	"stickynote/model"
)

// SanitizeExportFilename 清洗文件名：替换 Windows/zip 非法字符，去首尾空格和点，
// 空名回退「未命名」，最长 80 字（为扩展名留余量）。
func SanitizeExportFilename(title string) string {
	name := strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 32 {
			return -1
		}
		return r
	}, title)
	name = strings.TrimSpace(name)
	name = strings.TrimRight(name, ". ")
	if name == "" {
		name = "未命名"
	}
	if utf8.RuneCountInString(name) > 80 {
		name = string([]rune(name)[:80])
	}
	return name
}

// DedupeExportName zip 内重名加序号：标题.md、标题(2).md、标题(3).md……
// 大小写不敏感（Windows 解压目标文件系统不区分大小写）。
func DedupeExportName(used map[string]bool, base string) string {
	name := base
	for i := 2; ; i++ {
		key := strings.ToLower(name)
		if !used[key] {
			used[key] = true
			return name
		}
		name = base + "(" + strconv.Itoa(i) + ")"
	}
}

// WriteNotesZIP 把笔记逐篇转成 .md 写入 zip。
// skipEncrypted=true 时跳过加密笔记（批量导出语义；Web 全库导出查询本身已排除加密笔记，
// 这里再挡一次属于 fail-closed）。resolveImage 传 nil 时图片保持原样引用。
// 返回实际写入篇数与跳过篇数。
func WriteNotesZIP(w io.Writer, notes []model.Note, resolveImage htmltext.ImageResolver, skipEncrypted bool) (written, skipped int, err error) {
	zw := zip.NewWriter(w)
	used := make(map[string]bool)
	for i := range notes {
		n := &notes[i]
		if n.IsEncrypted && skipEncrypted {
			skipped++
			continue
		}
		entry := DedupeExportName(used, SanitizeExportFilename(n.Title)) + ".md"
		f, cerr := zw.Create(entry)
		if cerr != nil {
			_ = zw.Close()
			return written, skipped, cerr
		}
		if _, werr := f.Write([]byte(NoteToMarkdown(n.Title, n.Tags, n.Content, resolveImage))); werr != nil {
			_ = zw.Close()
			return written, skipped, werr
		}
		written++
	}
	if cerr := zw.Close(); cerr != nil {
		return written, skipped, cerr
	}
	return written, skipped, nil
}
