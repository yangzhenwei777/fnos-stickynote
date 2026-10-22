// Package app 是 Application Facade（离线方案 v20 §15/§16）：
//
//	Web(Gin Handler) / Desktop(Wails Binding) / Sync(Push/Pull) 三条入口共同下沉的业务层。
//	只处理业务规则、权限语义、事务、调用 model；不依赖 Gin/HTTP/Wails/Vue/Cookie。
//
// 迁移节奏（刻意渐进，不搞大爆炸）：
//   - P0-A：NoteService 薄封装现有 model 函数（事务当前仍在 model 内），先立骨架与错误码契约；
//   - P2 批次1（本轮）：笔记 CRUD 写路径改为"本包持有事务 + model *Tx 变体 + ChangeLog"
//     （§16 Model Tx 规范）：实体更新、seq 递增、事件写入同事务提交（§24/§42），
//     与 sync push 路径共用 changelog 包设施（防双源漂移）；handler 侧对应函数改为委托本包
//     （§129 门禁清单）。加密三件与回收站/批量随后续提交逐刀接入。
//
// 双构造器语义（关键，勿混用）：
//   - NewServerNoteService：服务端 Web 路径。写路径统一 version++（§56~§60）并在同事务
//     产 ChangeLog 事件——服务端 ChangeLog 是唯一权威事件流（桌面 pull 的数据源）。
//   - NewNoteService：桌面本地路径（兼容 P0-A 语义）。本地 version 恒为"最后已知服务端
//     版本"，本地编辑不自增（synclocal §25 P0-1，Outbox base_version 口径）；本地库不产
//     ChangeLog——桌面写路径经 Outbox push 到服务端后，由 push apply 统一产事件。
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

// ErrVersionConflict / ErrQuotaExceeded 直接转发 model 哨兵——入口层（handler/binding）
// 用 errors.Is 判类，禁止匹配错误文案（项目纪律）。
var (
	ErrVersionConflict = model.ErrVersionConflict
	ErrQuotaExceeded   = model.ErrQuotaExceeded
	ErrNotFound        = sql.ErrNoRows
	ErrInvalidFolder   = model.ErrInvalidFolder
)

// NoteService 笔记业务门面。MaxNotes 是"每用户笔记数上限"的运行参数
// （Web 侧来自 configs，每请求构造 service 注入；桌面侧本地默认，
// 同步冲突语义由服务端裁决）。
type NoteService struct {
	MaxNotes     int
	serverEvents bool // true=服务端 Web 路径（version++ + ChangeLog 同事务）；false=桌面本地路径
}

// NewNoteService 桌面/兼容构造（P0-A 语义：不产事件、Delete/Restore/Move 不动 version）。
func NewNoteService(maxNotes int) *NoteService { return &NoteService{MaxNotes: maxNotes} }

// NewServerNoteService 服务端 Web 构造（P2 批次1：写路径 version++ + ChangeLog 同事务）。
func NewServerNoteService(maxNotes int) *NoteService {
	return &NoteService{MaxNotes: maxNotes, serverEvents: true}
}

// bumpNoteVersionTx 服务端语义的版本推进（§56~§60）：version++ 并返回新版本。
// 仅 Delete/Restore/Move 需要（Update 走 CAS 自增；Create/Copy 由 DB DEFAULT 1 起步）。
func bumpNoteVersionTx(tx *sql.Tx, id int64) (int64, error) {
	if _, err := tx.Exec(`UPDATE notes SET version = version + 1 WHERE id = ?`, id); err != nil {
		return 0, fmt.Errorf("bump note %d version: %w", id, err)
	}
	var v int64
	if err := tx.QueryRow(`SELECT version FROM notes WHERE id = ?`, id).Scan(&v); err != nil {
		return 0, fmt.Errorf("read note %d version: %w", id, err)
	}
	return v, nil
}

// emitNoteChange 在调用方事务内产出 NOTE 实体事件（§23/§30）：
// payload 从权威行构造（服务端为准）→ seq 原子递增 → 事件落库。
// Web 路径 device_id 为空串（无设备身份，Pull 端自回声跳过只按 device_id==self 判定）。
// §32 P1-b：统一走 AppendNotePayload（≤4MB 内联 / >4MB blob_ref 自动分流）。
func emitNoteChange(tx *sql.Tx, userID, noteID int64, operation string, version int64) error {
	seq, err := changelog.NextChangeSeq(tx)
	if err != nil {
		return err
	}
	p, err := changelog.BuildNotePayload(tx, noteID)
	if err != nil {
		return err
	}
	syncID := p.SyncID
	return changelog.AppendNotePayload(tx, seq, userID, &syncID, operation, int(version), p, "")
}

// finishWriteTx server 模式的写路径收尾：version++（bump 版）+ 事件产出。
func (s *NoteService) finishWriteTx(tx *sql.Tx, userID, noteID int64, operation string, bump bool, version int64) error {
	if bump {
		v, err := bumpNoteVersionTx(tx, noteID)
		if err != nil {
			return err
		}
		version = v
	}
	return emitNoteChange(tx, userID, noteID, operation, version)
}

// CreateNote 新建（folderID=nil 落默认"我的笔记"）。事务内：配额+插入（含 sync_id）
// +标签+附件关系+CREATE 事件（server 模式），四写同提交。返回含 ID/Version 的完整行。
func (s *NoteService) CreateNote(userID int64, title, content, tags string, folderID *int64) (*model.Note, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	id, err := model.CreateNoteInFolderTx(tx, userID, title, content, tags, s.MaxNotes, folderID)
	if err != nil {
		return nil, err
	}
	if s.serverEvents {
		if err := emitNoteChange(tx, userID, id, "CREATE", 1); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return model.GetNote(id, userID)
}

// UpdateNote 普通（非加密）笔记更新：baseVersion 是客户端已知的服务端版本（CAS 基点，
// v20 §23/§47；桌面 Outbox 场景 = 本地缓存的 last-known server version，ADR §137 必测）。
// 事务内：CAS 更新（自增 version）+ UPDATE 事件（server 模式）同提交；
// 冲突返回 ErrVersionConflict；成功后返回服务端权威新行。
//
// E4-4（2026-09-23 真机账本铁证）：**桌面本地路径（serverEvents=false）不得 bump/CAS**。
// 不变量（P0-1）：本地 version 恒为"最后已知服务端版本"，bump 只由推送 ACK 回写。
// 此前桌面复用服务端 CAS+bump 写 → 本地 version 每次编辑自增（账本 base 2→3→4→5→6）
// → 服务端 version 纹丝不动 → 推送 CAS 永远失败 → 编辑多少次就有多少条 409 冲突。
// 冲突判定完全交给同步推送时的服务端 CAS；冲突处置走冲突中心（keep-local 重投）。
func (s *NoteService) UpdateNote(id, userID int64, title, content, tags string, baseVersion int64) (*model.Note, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if !s.serverEvents {
		// 桌面本地写：不 bump、不 CAS（updated_at 照写，version 保持 last-known server 值）
		if err := model.UpdateNoteLocalTx(tx, id, userID, title, content, tags); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return model.GetNote(id, userID)
	}
	newVersion, err := model.UpdateNoteTx(tx, id, userID, title, content, tags, baseVersion)
	if err != nil {
		return nil, err
	}
	if s.serverEvents {
		if err := emitNoteChange(tx, userID, id, "UPDATE", newVersion); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return model.GetNote(id, userID)
}

func (s *NoteService) GetNote(id, userID int64) (*model.Note, error) {
	return model.GetNote(id, userID)
}

func (s *NoteService) ListNotes(userID int64, page, pageSize int, search, tag, sortBy string, folderID *int64) (*model.NoteListResponse, error) {
	return model.ListNotes(userID, page, pageSize, search, tag, sortBy, folderID)
}

// DeleteNote 软删入回收站。server 模式：version++ + DELETE 墓碑事件（v20 §58 / SOURCE-FACT #3）
// 与实体更新同事务提交；桌面模式保持 P0-A 语义。
func (s *NoteService) DeleteNote(id, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := model.DeleteNoteTx(tx, id, userID); err != nil {
		return err
	}
	if s.serverEvents {
		if err := s.finishWriteTx(tx, userID, id, "DELETE", true, 0); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RestoreNote 从回收站恢复。server 模式：version++ + RESTORE 事件（§59 / SOURCE-FACT #4），
// 同事务提交；桌面模式保持 P0-A 语义。
func (s *NoteService) RestoreNote(id, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := model.RestoreNoteTx(tx, id, userID); err != nil {
		return err
	}
	if s.serverEvents {
		if err := s.finishWriteTx(tx, userID, id, "RESTORE", true, 0); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MoveNoteToFolder 移动（目标夹校验在其事务内，v21.66）。server 模式：version++（§57，
// updated_at 不动；对 v21.87 "移动不动 version" 的定向推翻仅限服务端语义——移动变更经
// UPDATE 事件传播，其他设备的下一保存按 409 冲突处理）+ UPDATE 事件同事务提交。
// 返回移动后的新版本（server 模式）供调用方同步本地乐观锁基点；桌面模式返回原版本。
func (s *NoteService) MoveNoteToFolder(id, userID int64, folderID *int64) (int64, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := model.MoveNoteToFolderTx(tx, id, userID, folderID); err != nil {
		return 0, err
	}
	newVersion := int64(0)
	if s.serverEvents {
		if err := s.finishWriteTx(tx, userID, id, "UPDATE", true, 0); err != nil {
			return 0, err
		}
		if err := tx.QueryRow(`SELECT version FROM notes WHERE id = ?`, id).Scan(&newVersion); err != nil {
			return 0, err
		}
	} else if err := tx.QueryRow(`SELECT version FROM notes WHERE id = ?`, id).Scan(&newVersion); err != nil {
		return 0, err
	}
	return newVersion, tx.Commit()
}

// CopyNoteToFolder 复制到目标文件夹（新副本带独立 sync_id）。server 模式：事务内配额+
// 复制关系行+CREATE 事件同提交（事件语义同 #1，payload 含 attachment_refs）。
func (s *NoteService) CopyNoteToFolder(id, userID int64, folderID *int64) (*model.Note, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	newID, err := model.CopyNoteToFolderTx(tx, id, userID, folderID, s.MaxNotes)
	if err != nil {
		return nil, err
	}
	if s.serverEvents {
		if err := emitNoteChange(tx, userID, newID, "CREATE", 1); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	note, err := model.GetNote(newID, userID)
	if err != nil {
		return nil, err
	}
	note.FolderID = folderID
	return note, nil
}

// ErrorCodeFor 把业务错误映射为跨端稳定错误码（binding 层 JSON code 字段用；
// 未知错误统一 INTERNAL，禁止把内部文案透传给 UI 逻辑判断）。
func ErrorCodeFor(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrVersionConflict):
		return "VERSION_CONFLICT"
	case errors.Is(err, ErrQuotaExceeded):
		return "QUOTA_EXCEEDED"
	case errors.Is(err, ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrInvalidFolder):
		return "INVALID_FOLDER"
	case errors.Is(err, ErrWrongPassword):
		return "WRONG_PASSWORD"
	case errors.Is(err, ErrAlreadyEncrypted):
		return "ALREADY_ENCRYPTED"
	case errors.Is(err, ErrNotEncrypted):
		return "NOT_ENCRYPTED"
	case errors.Is(err, ErrFolderNotEmpty):
		return "FOLDER_NOT_EMPTY"
	case errors.Is(err, ErrFileTypeRejected):
		return "FILE_TYPE_REJECTED"
	default:
		return "INTERNAL"
	}
}

// BatchDeleteNotes 批量软删（SOURCE-FACT #6）：单事务逐行 DeleteNoteTx +（server 模式）
// version++ + DELETE 墓碑事件——每行各自事件（op 独立性天然成立，ADR-002 批协议按实体去重），
// 对齐 EmptyTrash 的 v21.95 单事务模式（BEGIN IMMEDIATE，全成或全不删，消除逐行独立事务的
// 部分删除中间态）。不存在的 ID 跳过并按原语义计数（deleted 只计成功行）。
func (s *NoteService) BatchDeleteNotes(userID int64, ids []int64) (int, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	deleted := 0
	for _, id := range ids {
		if err := model.DeleteNoteTx(tx, id, userID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // 不存在/已删除：跳过（与原逐行循环语义一致）
			}
			return 0, err
		}
		if s.serverEvents {
			if err := s.finishWriteTx(tx, userID, id, "DELETE", true, 0); err != nil {
				return 0, err
			}
		}
		deleted++
	}
	return deleted, tx.Commit()
}

// EmptyTrash server 模式：清空回收站（**永久删**）。
// E7（2026-09-23 用户产品决策，推翻"purge 不发事件"原设计）：为每条被永久删的笔记
// **在删除前**产 PURGE 事件（BuildNotePayload 需要权威行在，行删后不可再构造），
// 客户端据此物理清理本地行并撤销挂账——否则客户端永远留僵尸行、恢复必撞 ENTITY_GONE
// （真机六轮实锤的回收站不同步根因）。DELETE（软删进回收站）语义不变。
// 返回 (删除条数, 附件清单, error)；附件清单在删除前收集（fail-closed）。
func (s *NoteService) EmptyTrash(userID int64) (int, []string, error) {
	rows, err := database.DB.Query(
		`SELECT id FROM notes WHERE user_id = ? AND deleted_at IS NOT NULL`, userID)
	if err != nil {
		return 0, nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	var uploadNames []string
	for _, id := range ids {
		names, uerr := database.ListNoteUploads(id)
		if uerr != nil {
			return 0, nil, fmt.Errorf("list note %d attachments: %w", id, uerr)
		}
		uploadNames = append(uploadNames, names...)
	}
	if len(ids) == 0 {
		return 0, nil, nil
	}
	tx, err := database.DB.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	if s.serverEvents {
		for _, id := range ids {
			var v int64
			if err := tx.QueryRow(`SELECT version FROM notes WHERE id = ?`, id).Scan(&v); err != nil {
				return 0, nil, err
			}
			if err := emitNoteChange(tx, userID, id, syncspec.OperationPurge, v); err != nil {
				return 0, nil, err
			}
		}
	}
	if _, err := tx.Exec(
		`DELETE FROM notes WHERE user_id = ? AND deleted_at IS NOT NULL`, userID); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return len(ids), uploadNames, nil
}

// PermanentDelete server 模式：彻底删除单条（回收站内），删除前产 PURGE 事件（同上）。
func (s *NoteService) PermanentDelete(id, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int64
	if err := tx.QueryRow(
		`SELECT version FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NOT NULL`,
		id, userID).Scan(&v); err != nil {
		return err // 不存在/不在回收站
	}
	if s.serverEvents {
		if err := emitNoteChange(tx, userID, id, syncspec.OperationPurge, v); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`DELETE FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NOT NULL`, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}
