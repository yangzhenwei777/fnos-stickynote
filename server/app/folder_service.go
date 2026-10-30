package app

import (
	"database/sql"
	"errors"
	"fmt"

	"stickynote/changelog"
	"stickynote/database"
	"stickynote/model"
	"stickynote/syncspec"
)

// FolderService 文件夹门面（P0-A 接线切片 → P1 批次2 全量）。语义与服务端 handler/folder.go 对齐：
// 默认"我的笔记"为虚拟项（folder_id IS NULL，ListFolders 首位），不落库、**永不进事件**（§55）；
// 删除受非空守卫约束（ErrFolderNotEmpty，SOURCE-FACT #15）。
//
// 双构造器语义同 NoteService（勿混用）：
//   - NewServerFolderService：Web 路径。每个 mutation 事务内 version++ + ChangeLog（§18/§56）；
//     排序移动产**单条 FOLDER_ORDER 集合级事件**（§9/§55：全序数组 + 逐 folder 权威版本，
//     一次移动 = 一个 seq，整批原子）；删除文件夹时回收站受影响笔记逐行 version++ + UPDATE 事件
//     （§56/§143——payload 携带 deleted_at，桌面应用端须保持墓碑态）。
//   - NewFolderService：桌面本地路径（P0-A 语义）：不产事件不 bump；本地 version = 最后已知
//     服务端版本（Outbox base 口径，synclocal §25 P0-1）。
type FolderService struct {
	serverEvents bool
}

func NewFolderService() *FolderService { return &FolderService{} }

// ErrFolderNotEmpty 别名转发（原 P0-A 定义位；ErrorCodeFor 共用）。
var ErrFolderNotEmpty = model.ErrFolderNotEmpty

// NewServerFolderService 服务端 Web 构造（P1 批次2：mutation + version++ + 事件同事务）。
func NewServerFolderService() *FolderService { return &FolderService{serverEvents: true} }

// bumpFolderVersionTx 服务端语义的版本推进：version++ 并返回新版本。
func bumpFolderVersionTx(tx *sql.Tx, id int64) (int64, error) {
	if _, err := tx.Exec(`UPDATE note_folders SET version = version + 1 WHERE id = ?`, id); err != nil {
		return 0, fmt.Errorf("bump folder %d version: %w", id, err)
	}
	var v int64
	if err := tx.QueryRow(`SELECT version FROM note_folders WHERE id = ?`, id).Scan(&v); err != nil {
		return 0, fmt.Errorf("read folder %d version: %w", id, err)
	}
	return v, nil
}

func folderSyncIDPtr(s string) *string { return &s }

// List 与 model.ListFolders 同源（虚拟默认项在首位，不落库不进事件）。
func (s *FolderService) List(userID int64, granted ...map[int64]struct{}) ([]model.Folder, error) {
	return model.ListFolders(userID, granted...)
}

// Create 新建自定义文件夹。server 模式：事务内 CREATE 事件（version=1）。
func (s *FolderService) Create(userID int64, name string) (*model.Folder, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	id, err := model.CreateFolderTx(tx, userID, name)
	if err != nil {
		return nil, err
	}
	if s.serverEvents {
		if _, err := changelog.AppendFolderChange(tx, userID, id, "CREATE", ""); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return model.GetFolder(id, userID)
}

// Rename 重命名。server 模式：事务内 version++ + UPDATE 事件。
func (s *FolderService) Rename(id, userID int64, name string) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := model.RenameFolderTx(tx, id, userID, name); err != nil {
		return err
	}
	if s.serverEvents {
		if _, err := bumpFolderVersionTx(tx, id); err != nil {
			return err
		}
		if _, err := changelog.AppendFolderChange(tx, userID, id, "UPDATE", ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Move 排序移动（up/down/top/bottom）。server 模式：变化行逐 folder version++，
// 整次移动产**单条 FOLDER_ORDER 事件**（§55 单一 seq，整批原子）；次序无变化 = 零事件（幂等）。
func (s *FolderService) Move(id, userID int64, position string) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	order, changed, err := model.MoveFolderComputeOrderTx(tx, id, userID, position)
	if err != nil {
		return err
	}
	if s.serverEvents && len(changed) > 0 {
		versions := make([]syncspec.FolderVersionRef, 0, len(order))
		for _, fid := range order {
			v, err := bumpFolderVersionTx(tx, fid)
			if err != nil {
				return err
			}
			syncID, err := model.FolderSyncIDTx(tx, fid)
			if err != nil {
				return err
			}
			versions = append(versions, syncspec.FolderVersionRef{SyncID: syncID, Version: v})
		}
		orderIDs := make([]string, 0, len(versions))
		for _, vr := range versions {
			orderIDs = append(orderIDs, vr.SyncID)
		}
		if _, err := changelog.AppendFolderOrderChange(tx, userID, orderIDs, versions, ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete 删除自定义文件夹（非空守卫）。server 模式：
//  1. FOLDER DELETE 墓碑事件（携带删除前 version，供远端冲突基点）；
//  2. 回收站悬空引用逐行 version++ + UPDATE 事件（§56/§143，payload 带 deleted_at 保墓碑态）。
func (s *FolderService) Delete(id, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	info, repointed, err := model.DeleteFolderTx(tx, id, userID)
	if err != nil {
		return err
	}
	if s.serverEvents {
		seq, err := changelog.NextChangeSeq(tx)
		if err != nil {
			return err
		}
		p := syncspec.FolderPayload{SyncID: info.SyncID, Name: info.Name, Version: info.Version}
		if err := changelog.AppendChange(tx, seq, userID, syncspec.EntityTypeFolder, folderSyncIDPtr(info.SyncID),
			"DELETE", int(info.Version), changelog.MarshalFolderPayload(p), ""); err != nil {
			return err
		}
		// §56：受影响回收站笔记逐行各自事件（不 bump 会让其他设备以旧 base 覆盖式重排归属）。
		for _, nid := range repointed {
			nv, err := bumpNoteVersionTx(tx, nid)
			if err != nil {
				return err
			}
			if err := emitNoteChange(tx, userID, nid, "UPDATE", nv); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ErrorCodeForFolder 文件夹域错误映射（跨端稳定码；binding/handler 共用，未知回落到 ErrorCodeFor）。
func ErrorCodeForFolder(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, model.ErrFolderNotFound):
		return "NOT_FOUND"
	case errors.Is(err, model.ErrFolderNotEmpty):
		return "FOLDER_NOT_EMPTY"
	case errors.Is(err, model.ErrFolderNameExists):
		return "FOLDER_NAME_EXISTS"
	case errors.Is(err, model.ErrFolderDefault):
		return "FOLDER_IMMUTABLE"
	case errors.Is(err, model.ErrFolderAlreadyEncrypted):
		return "FOLDER_ALREADY_ENCRYPTED"
	case errors.Is(err, model.ErrFolderNotEncrypted):
		return "FOLDER_NOT_ENCRYPTED"
	case errors.Is(err, model.ErrFolderLocked):
		return "FOLDER_LOCKED"
	case errors.Is(err, ErrWrongPassword):
		return "WRONG_PASSWORD"
	case errors.Is(err, ErrPasswordMismatch):
		return "PASSWORD_MISMATCH"
	case errors.Is(err, ErrPasswordTooLong), errors.Is(err, ErrPasswordInvalid):
		return "PASSWORD_INVALID"
	default:
		return ErrorCodeFor(err)
	}
}
