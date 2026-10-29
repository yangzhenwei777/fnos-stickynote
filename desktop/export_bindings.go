package main

// export_bindings.go — 桌面端笔记导出绑定（v2.2.2 R2 桌面同步补齐）。
//
// 背景（用户实测桌面端缺陷）：Web 端「下载」走 GET/POST /notes/:id/export（HTTP 通道，
// axios blob 落盘）；桌面壳内 /api 全部经 LocalTransport 走 Wails 绑定，且该端点从未
// 接线（local_extras.go 头注挂账「export blob 501」）——非加密笔记的前端旧实现还是
// window.open(相对URL)，而 Wails v2 不处理 WebView2 NewWindowRequested，只会弹出
// 一个指向 wails.localhost 的空壳窗口。故桌面端「下载」功能实际不可用。
//
// 本绑定补齐导出面：转换器/图片内联/消毒与 Web 端**同一实现**
// （notetext.NoteToMarkdown + htmltext.NewFileImageResolver），零双源漂移；
// 密码仅加密笔记需要，随调用栈内存传递（不落盘不进日志）。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"stickynote/crypto"
	"stickynote/htmltext"
	"stickynote/model"
	"stickynote/notetext"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// encBlob 加密笔记口令哈希+密文（dbReadGuard 泛型封装用）。
type encBlob struct{ hash, ct string }

// ExportNoteMD 导出一篇笔记为 Markdown 字符串（前端 blob 触发保存）。
// 与 handler.ExportNote 同语义：加密笔记先 bcrypt 验口令再解密（只读，不改加密态）；
// 非加密笔记直接取存储态内容。错误文案面向用户（中文，与 Web 端提示一致）。
func (l *LocalService) ExportNoteMD(id int64, password string) (string, error) {
	note, err := dbReadGuard(func() (*model.Note, error) { return model.GetNote(id, l.userID) })
	if err != nil {
		return "", fmt.Errorf("笔记不存在")
	}

	content := note.Content
	if note.IsEncrypted {
		if strings.TrimSpace(password) == "" {
			return "", fmt.Errorf("加密笔记导出需要密码")
		}
		blob, gerr := dbReadGuard(func() (encBlob, error) {
			h, c, e := model.GetEncryptedContent(id, l.userID)
			return encBlob{hash: h, ct: c}, e
		})
		if gerr != nil {
			return "", fmt.Errorf("获取加密信息失败")
		}
		if bcrypt.CompareHashAndPassword([]byte(blob.hash), []byte(password)) != nil {
			return "", fmt.Errorf("密码错误")
		}
		plaintext, derr := crypto.Decrypt(blob.ct, password)
		if derr != nil {
			return "", fmt.Errorf("解密失败")
		}
		content = plaintext
	}

	// 转换 + 消毒统一走下沉层（notetext.NoteToMarkdown 内含 Content 消毒——
	// 列宽表格原生 HTML 块导出的安全前提，见该文件头注）
	return notetext.NoteToMarkdown(note.Title, note.Tags, content,
		htmltext.NewFileImageResolver(l.uploadDir)), nil
}

// saveFileDialog 可注入：生产 = Wails 原生另存为；测试替换为固定路径，避免弹窗。
var saveFileDialog = wailsRuntime.SaveFileDialog

// ExportNotesZip 桌面端「导出全部 / 批量导出」：弹原生另存为，直接把 zip 落盘。
//
// 为什么不像 Web 端回传 blob：导出会把图片内联为 base64，全库 zip 可达数百 MB；
// 经 Wails 桥 base64 回传会内存翻倍并可能撞 WebView2 消息上限。原生落盘与
// PortableExport 同款，且失败时不会留下半截目标文件（先写同目录临时文件再 rename）。
//
// all=true 导出全部未加密笔记；否则按 ids 导出。返回：
//
//	{cancelled:true} 用户取消
//	{path, exported, skipped, bytes} 成功
//
// 加密笔记一律跳过（与 Web 端批量/全部导出同语义），skipped 供 UI 如实提示。
func (l *LocalService) ExportNotesZip(ids []int64, all bool) (map[string]any, error) {
	if l.ctx == nil {
		return nil, errors.New("DESKTOP_RUNTIME_NOT_READY")
	}
	notes, err := dbReadGuard(func() ([]model.Note, error) {
		if all {
			return model.ListAllNotesForExport(l.userID)
		}
		return model.ListNotesByIDs(ids, l.userID)
	})
	if err != nil {
		return nil, err
	}
	if len(notes) == 0 {
		return nil, errors.New("没有找到可导出的笔记")
	}

	// 全库导出查询本身已排除加密笔记，跳过数单独统计（与 Web 端 X-Encrypted-Skipped 同口径）
	skipped, _ := dbReadGuard(func() (int, error) {
		var n int
		if all {
			model.CountEncryptedNotes(l.userID, &n)
			return n, nil
		}
		for i := range notes {
			if notes[i].IsEncrypted {
				n++
			}
		}
		return n, nil
	})

	prefix := "便签导出-"
	if !all {
		prefix = "便签批量导出-"
	}
	savePath, derr := saveFileDialog(l.ctx, wailsRuntime.SaveDialogOptions{
		Title:           "导出笔记（.zip）",
		DefaultFilename: prefix + time.Now().Format("20060102-150405") + ".zip",
		Filters:         []wailsRuntime.FileFilter{{DisplayName: "便签导出 (*.zip)", Pattern: "*.zip"}},
	})
	if derr != nil {
		return nil, derr
	}
	if savePath == "" {
		return map[string]any{"cancelled": true}, nil
	}

	// 临时文件与目标同目录：rename 才是同卷原子替换，跨卷会退化为复制。
	tmp, cerr := os.CreateTemp(filepath.Dir(savePath), ".stickynote-export-*.zip")
	if cerr != nil {
		return nil, fmt.Errorf("无法写入所选位置：%w", cerr)
	}
	tmpPath := tmp.Name()
	written, zipSkipped, werr := notetext.WriteNotesZIP(tmp, notes, htmltext.NewFileImageResolver(l.uploadDir), true)
	if werr == nil {
		werr = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if werr != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("导出失败：%w", werr)
	}
	if err := os.Rename(tmpPath, savePath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("保存失败：%w", err)
	}
	if !all {
		skipped = zipSkipped
	}
	var size int64
	if fi, serr := os.Stat(savePath); serr == nil {
		size = fi.Size()
	}
	return map[string]any{
		"path": savePath, "exported": written, "skipped": skipped, "bytes": size,
	}, nil
}
