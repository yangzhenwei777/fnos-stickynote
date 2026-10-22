package model

import (
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"
)

// v21.87 笔记移动/复制（用户需求：笔记菜单新增【移动到】【复制到】）。
// 独立文件而非塞进 note.go（719 行已超纪律 5.1 软上限 700）。

// ErrInvalidFolder 目标文件夹不存在或不属于当前用户。
var ErrInvalidFolder = errors.New("invalid folder")

// validateFolderTx 在事务内校验文件夹归属（存在且属于 userID）。
// folderID == nil 表示默认"我的笔记"（虚拟项，folder_id IS NULL），无需校验。
func validateFolderTx(tx *sql.Tx, folderID *int64, userID int64) error {
	if folderID == nil {
		return nil
	}
	var fid int64
	if err := tx.QueryRow(
		"SELECT id FROM note_folders WHERE id = ? AND user_id = ?", *folderID, userID,
	).Scan(&fid); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFolder, err)
	}
	return nil
}

// MoveNoteToFolder / CopyNoteToFolder 实现已迁至 note_tx.go（P2 批次1：
// *Tx 变体 + 自持事务包装，validateFolderTx/copyTitle 留守本文件供其复用）。

// copyTitle 副本标题：源标题 + " (副本)"；超 200 字符（createNoteRequest max=200）
// 时按 rune 截断（防超长标题破坏后续编辑保存的 binding 校验）。
func copyTitle(src string) string {
	t := src
	if t == "" {
		t = "无标题"
	}
	t += " (副本)"
	if utf8.RuneCountInString(t) > 200 {
		t = string([]rune(t)[:200])
	}
	return t
}

// CopyNoteToFolder 复制笔记到目标文件夹（实现见 note_tx.go）。
