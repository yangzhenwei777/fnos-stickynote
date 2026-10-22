package main

// import_bindings.go —— 桌面离线「导入笔记」（.md/.txt/.html/.htm）。
//
// 背景（缺陷修复）：Web 端经 `POST /api/notes/import` 导入，但桌面 LocalTransport 的
// 路由表里**从未接线**该端点，且未接线路由当时返回的是「成功形状的 501」，
// 于是前端把错误对象当笔记插入列表并弹「导入成功」——用户看到一条空笔记、随后消失。
//
// 本文件补齐 Go 侧能力，与在线导入**共用同一套转换与消毒实现**（`stickynote/notetext`），
// 保证在线/离线产出逐字节一致：
//
//	multipart 解析 / 大小闸门  →  handler.ImportFile（在线）
//	Wails Binding 字节入参     →  本文件（离线）
//	标题提取·消毒·md/txt/html 转换·GFM 任务清单·内容白名单消毒  →  notetext（两端共用）
//	配额·落库·事件             →  各自宿主（在线走 app.NewServerNoteService；离线走本地 NoteService）
//
// 桌面落库语义（与 §25 一致）：本地 NoteService 属桌面模式——**零 ChangeLog、零 version++**；
// CreateNote 内部已按 §25 入 Outbox，未登录时零副作用（离线单机行为不变）。

import (
	"errors"
	"fmt"

	"stickynote/model"
	"stickynote/notetext"
)

// ErrImportEmpty 导入内容为空（0 字节文件）。
var ErrImportEmpty = errors.New("导入文件为空")

// desktopImportSizeLimit 桌面侧导入硬上限：与在线同一配置键与同一换算规则
// （notetext.SizeLimitForConfig），避免"同一份文件在线能导、离线报错"。
func desktopImportSizeLimit() int64 {
	kb := 0
	if v, err := model.GetConfig("max_note_content_kb"); err == nil {
		if n, ok := atoiAllow0(v); ok {
			kb = n
		}
	}
	return notetext.SizeLimitForConfig(kb)
}

// ImportNote 把文件字节导入为一条本地笔记，返回新建的笔记（形状与 Web 端
// `POST /api/notes/import` 的 201 响应一致，前端可无差别渲染）。
//
// filename 仅用于①扩展名白名单校验 ②无 Markdown 标题时回退为文件名。
// 错误串面向用户，可直接上屏（经 LocalTransport 的 ERROR_RULES 映射为 400/403 等状态码）。
//
// ⭐ v21.213（211b 实机·导入笔记落到「我的笔记」而非当前文件夹）：补folderID 透传。
// 定罪：原实现末尾硬编码 `l.CreateNote(title, content, "", nil)` —— **folderID 写死 nil**，
// 而 `CreateNote` 本身就支持 `folderID *int64`（main.go:156）。前端 LocalTransport 的
// `POST /notes` 也确实传了 `i(a.folder_id)`，**只有导入路径把它丢了**。
// 后果：无论用户在哪个文件夹点导入，笔记一律落到「我的笔记」（folder_id=NULL）。
// 修法：签名加 folderID 并透传；nil 仍表示"我的笔记"，与 CreateNote 语义一致。
// 旧调用点（测试/无文件夹导入）传 nil 即可，**零破坏**。
func (l *LocalService) ImportNote(filename string, data []byte, folderID *int64) (*model.Note, error) {
	if len(data) == 0 {
		return nil, ErrImportEmpty
	}
	if !notetext.AllowedExt(notetext.ExtOf(filename)) {
		return nil, errors.New("仅支持 .md / .txt / .html / .htm 文件")
	}
	limit := desktopImportSizeLimit()
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("导入文件过大，上限 %dMB", limit/(1<<20))
	}

	// 转换 + 消毒（与在线导入同一实现）
	title, content, err := notetext.Convert(filename, data)
	if err != nil {
		return nil, err
	}

	// 配额（与 GetQuota / 在线导入同口径：max_notes_per_user，0 = 不限）
	limitNotes := 1000
	if v, err := model.GetConfig("max_notes_per_user"); err == nil {
		if n, ok := atoiAllow0(v); ok {
			limitNotes = n
		}
	}
	if limitNotes > 0 {
		cnt, cerr := model.CountActiveNotes(l.userID)
		if cerr == nil && cnt >= limitNotes {
			return nil, fmt.Errorf("已达到笔记数量上限（%d 篇），请先清理不需要的笔记", limitNotes)
		}
	}

	// 落库：复用既有绑定（内置 §25 Outbox 入队；未登录零副作用）
	// ⭐ v21.213：透传 folderID（导入应落在「用户当前所在文件夹」，见函数头定罪）。
	return l.CreateNote(title, content, "", folderID)
}
