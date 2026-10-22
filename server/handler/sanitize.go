package handler

import (
	"github.com/microcosm-cc/bluemonday"

	"stickynote/notetext"
)

// 本文件原为笔记内容/标题消毒策略的**唯一实现**。v21.140 起实现下沉到零 gin 依赖的
// `stickynote/notetext` 包——因为同一套规则要被两个宿主复用：
//   - 服务端在线导入：handler.ImportFile
//   - 桌面离线导入：desktop.LocalService.ImportNote
// 若继续留在 handler，桌面会为了两个纯函数拖入 gin/HTTP 栈；各自复制一份则必然漂移。
//
// 这里保留**同名薄封装**：既有调用点、单测断言与语义逐字节不变（搬移保真由既有测试把关）。

// buildNoteContentPolicy 保留原签名供既有单测与调用点使用；实现见 notetext.BuildContentPolicy。
//   - allowExternalImg=false（在役）：img src 仅 /uploads/
//   - allowExternalImg=true（仅测试口径）：img src 不限
func buildNoteContentPolicy(allowExternalImg bool) *bluemonday.Policy {
	return notetext.BuildContentPolicy(allowExternalImg)
}

// sanitizeNoteContent 按白名单消毒笔记 HTML 内容，入库前调用。
func sanitizeNoteContent(content string) string {
	return notetext.Content(content)
}

// sanitizeNoteTitle 消毒笔记标题（v21.105 口径：仅剥离控制字符，保持原文，不做实体转义）。
func sanitizeNoteTitle(title string) string {
	return notetext.Title(title)
}
