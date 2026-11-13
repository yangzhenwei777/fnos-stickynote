package app

import "database/sql"

// LocalSyncHooks 由桌面端注入。所有 hook 都在本地业务事务内执行：
// 返回错误即回滚业务写，禁止出现业务已提交但 Outbox 缺失的中间态。
// Web 服务端路径不设置 hooks，行为保持不变。
type LocalSyncHooks struct {
	Note         func(tx *sql.Tx, noteID int64, operation string) error
	Folder       func(tx *sql.Tx, folderID int64, operation string) error
	FolderDelete func(tx *sql.Tx, syncID, name string, version int64) error
	FolderOrder  func(tx *sql.Tx) error
}

func (s *NoteService) outboxNote(tx *sql.Tx, id int64, operation string) error {
	if s.serverEvents || s.LocalHooks == nil || s.LocalHooks.Note == nil {
		return nil
	}
	return s.LocalHooks.Note(tx, id, operation)
}

func (s *FolderService) outboxFolder(tx *sql.Tx, id int64, operation string) error {
	if s.serverEvents || s.LocalHooks == nil || s.LocalHooks.Folder == nil {
		return nil
	}
	return s.LocalHooks.Folder(tx, id, operation)
}

func (s *FolderService) outboxFolderDelete(tx *sql.Tx, syncID, name string, version int64) error {
	if s.serverEvents || s.LocalHooks == nil || s.LocalHooks.FolderDelete == nil {
		return nil
	}
	return s.LocalHooks.FolderDelete(tx, syncID, name, version)
}

func (s *FolderService) outboxNote(tx *sql.Tx, id int64, operation string) error {
	if s.serverEvents || s.LocalHooks == nil || s.LocalHooks.Note == nil {
		return nil
	}
	return s.LocalHooks.Note(tx, id, operation)
}

func (s *FolderService) outboxFolderOrder(tx *sql.Tx) error {
	if s.serverEvents || s.LocalHooks == nil || s.LocalHooks.FolderOrder == nil {
		return nil
	}
	return s.LocalHooks.FolderOrder(tx)
}
