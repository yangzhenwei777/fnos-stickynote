package model

import (
	"database/sql"
	"errors"
	"fmt"

	"stickynote/changelog"
	"stickynote/database"
)

// folder_tx.go —— 文件夹写路径 *Tx 变体（P1 批次2，§16 Model Tx 规范 / §18 / §55~§56）。
//
// 与 note_tx.go 同一纪律：事务由调用方（app FolderService）持有；本层**不 bump version、
// 不产事件**——桌面 LocalService 复用本层操作本地库，本地 version 恒为
// "最后已知服务端版本"（synclocal §25 P0-1）。服务端语义（version++ / ChangeLog /
// FOLDER_ORDER 单事件，§56 受影响回收站笔记逐行事件）由 app.NewServerFolderService
// 在同一事务内补齐。

// GetFolderTx 事务内取文件夹（存在性+归属）。未命中返回 sql.ErrNoRows。
func GetFolderTx(tx *sql.Tx, id, userID int64) (name string, sortOrder int, version int64, err error) {
	err = tx.QueryRow("SELECT name, sort_order, version FROM note_folders WHERE id = ? AND user_id = ?", id, userID).
		Scan(&name, &sortOrder, &version)
	return
}

// CreateFolderTx 事务内创建（上限/重名校验同事务，sync_id 插入即生成）。返回新行 ID。
func CreateFolderTx(tx *sql.Tx, userID int64, name string) (int64, error) {
	name, err := ValidateFolderName(name)
	if err != nil {
		return 0, err
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM note_folders WHERE user_id = ?", userID).Scan(&count); err != nil {
		return 0, err
	}
	if count >= MaxFoldersPerUser {
		return 0, fmt.Errorf("自定义文件夹最多 %d 个", MaxFoldersPerUser)
	}
	var dup int
	if err := tx.QueryRow("SELECT COUNT(*) FROM note_folders WHERE user_id = ? AND name = ?", userID, name).Scan(&dup); err != nil {
		return 0, err
	}
	if dup > 0 {
		return 0, ErrFolderNameExists
	}
	var maxOrder int
	if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), 0) FROM note_folders WHERE user_id = ?", userID).Scan(&maxOrder); err != nil {
		return 0, err
	}
	result, err := tx.Exec(
		// 插入即生成 sync_id（批次1 口径；§18 身份列），Ensure 幂等兜底。
		"INSERT INTO note_folders (user_id, name, sort_order, sync_id) VALUES (?, ?, ?, ?)",
		userID, name, maxOrder+1, changelog.NewSyncID(),
	)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get inserted folder id: %w", err)
	}
	return id, nil
}

// RenameFolderTx 事务内重命名（存在性/重名校验同事务）。
func RenameFolderTx(tx *sql.Tx, id, userID int64, name string) error {
	name, err := ValidateFolderName(name)
	if err != nil {
		return err
	}
	if _, _, _, err := GetFolderTx(tx, id, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFolderNotFound
		}
		return err
	}
	var dup int
	if err := tx.QueryRow("SELECT COUNT(*) FROM note_folders WHERE user_id = ? AND name = ? AND id != ?",
		userID, name, id).Scan(&dup); err != nil {
		return err
	}
	if dup > 0 {
		return ErrFolderNameExists
	}
	_, err = tx.Exec("UPDATE note_folders SET name = ? WHERE id = ? AND user_id = ?", name, id, userID)
	return err
}

// ListFolderIDsTx 当前次序下的自定义文件夹 ID（sort_order、id 升序）。
func ListFolderIDsTx(tx *sql.Tx, userID int64) ([]int64, error) {
	rows, err := tx.Query("SELECT id FROM note_folders WHERE user_id = ? ORDER BY sort_order ASC, id ASC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ApplyFolderOrderTx 全量重写 sort_order 为 1..n（ids 给定期次）。
// 返回实际发生变化的 ID 集合（供服务端 bump + FOLDER_ORDER 事件判定；次序未变 = 零写入，幂等）。
// 历史重复 sort_order 的自愈重写同样计入 changed（v21.101 NormalizeFolderSortOrder 的 Tx 化替代）。
func ApplyFolderOrderTx(tx *sql.Tx, userID int64, ids []int64) ([]int64, error) {
	current, err := tx.Query("SELECT id, sort_order FROM note_folders WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	sortByID := map[int64]int{}
	for current.Next() {
		var id int64
		var so int
		if err := current.Scan(&id, &so); err != nil {
			current.Close()
			return nil, err
		}
		sortByID[id] = so
	}
	current.Close()
	if err := current.Err(); err != nil {
		return nil, err
	}

	var changed []int64
	for i, id := range ids {
		if sortByID[id] == i+1 {
			continue
		}
		if _, err := tx.Exec("UPDATE note_folders SET sort_order = ? WHERE id = ? AND user_id = ?", i+1, id, userID); err != nil {
			return nil, err
		}
		changed = append(changed, id)
	}
	return changed, nil
}

// DeleteFolderTx 事务内删除：非空守卫（未删笔记）→ 收集回收站悬空引用 → 物理删 → 置 NULL。
// 返回被改指向（folder_id→NULL）的回收站笔记 ID（服务端对每行补 version++ + UPDATE 事件，
// §56/§143"每条受影响 notes 各自事件"；桌面模式仅改指不 bump）。
func DeleteFolderTx(tx *sql.Tx, id, userID int64) (info FolderDeleteInfo, repointedNoteIDs []int64, err error) {
	fName, _, fVer, err := GetFolderTx(tx, id, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return info, nil, ErrFolderNotFound
		}
		return info, nil, err
	}
	fSync, err := changelog.EnsureFolderSyncID(tx, id)
	if err != nil {
		return info, nil, err
	}
	info = FolderDeleteInfo{ID: id, SyncID: fSync, Name: fName, Version: fVer}
	var active int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM notes WHERE user_id = ? AND folder_id = ? AND deleted_at IS NULL",
		userID, id,
	).Scan(&active); err != nil {
		return info, nil, err
	}
	if active > 0 {
		return info, nil, ErrFolderNotEmpty
	}

	rows, err := tx.Query("SELECT id FROM notes WHERE user_id = ? AND folder_id = ?", userID, id)
	if err != nil {
		return info, nil, err
	}
	for rows.Next() {
		var nid int64
		if err := rows.Scan(&nid); err != nil {
			rows.Close()
			return info, nil, err
		}
		repointedNoteIDs = append(repointedNoteIDs, nid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return info, nil, err
	}

	if _, err := tx.Exec("DELETE FROM note_folders WHERE id = ? AND user_id = ?", id, userID); err != nil {
		return info, nil, err
	}
	if _, err := tx.Exec("UPDATE notes SET folder_id = NULL WHERE user_id = ? AND folder_id = ?", userID, id); err != nil {
		return info, nil, err
	}
	return info, repointedNoteIDs, nil
}

// FolderDeleteInfo 被删文件夹行的删除前快照（DELETE 墓碑事件的 entity_sync_id/version/载荷素材）。
type FolderDeleteInfo struct {
	ID      int64
	SyncID  string
	Name    string
	Version int64
}

// FolderSyncIDTx 事务内取文件夹身份（NULL 存量行自动补齐后返回；同 notes 防御口径）。
func FolderSyncIDTx(tx *sql.Tx, id int64) (string, error) {
	return changelog.EnsureFolderSyncID(tx, id)
}

// ---- 兼容包装（自持事务；语义与 P0-A 一致：不 bump 不产事件。
// ---- 服务端 Web 路径请走 app.NewServerFolderService；桌面 binding 沿用本组函数）----

// CreateFolder 自持事务创建（实现迁至 CreateFolderTx，P1 批次2）。
func CreateFolder(userID int64, name string) (*Folder, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := CreateFolderTx(tx, userID, name)
	if err != nil {
		return nil, err
	}
	var so int
	if err := tx.QueryRow("SELECT sort_order FROM note_folders WHERE id = ?", id).Scan(&so); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	fid := id
	return &Folder{ID: &fid, UserID: userID, Name: name, SortOrder: so, NoteCount: 0}, nil
}

// RenameFolder 自持事务重命名（实现迁至 RenameFolderTx，P1 批次2）。
func RenameFolder(id, userID int64, name string) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := RenameFolderTx(tx, id, userID, name); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveFolder 自持事务排序移动（实现迁至 List/ApplyFolderOrderTx，P1 批次2）。
// position: up / down / top / bottom；次序未变 = 幂等零写。
func MoveFolder(id, userID int64, position string) error {
	switch position {
	case "up", "down", "top", "bottom":
	default:
		return fmt.Errorf("无效的移动方向")
	}
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids, err := ListFolderIDsTx(tx, userID)
	if err != nil {
		return err
	}
	idx := -1
	for i, fid := range ids {
		if fid == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrFolderNotFound
	}
	if _, err := ApplyFolderOrderTx(tx, userID, reorderIDs(ids, idx, position)); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveFolderComputeOrderTx 排序移动的事务版：校验方向/存在性 → 计算新序 → 写入 →
// 返回（全序 ID 数组, 实际变化 ID 集合）。changed 为空 = 幂等零写（服务端据此不产事件）。
func MoveFolderComputeOrderTx(tx *sql.Tx, id, userID int64, position string) (order []int64, changed []int64, err error) {
	switch position {
	case "up", "down", "top", "bottom":
	default:
		return nil, nil, fmt.Errorf("无效的移动方向")
	}
	ids, err := ListFolderIDsTx(tx, userID)
	if err != nil {
		return nil, nil, err
	}
	idx := -1
	for i, fid := range ids {
		if fid == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, nil, ErrFolderNotFound
	}
	order = reorderIDs(ids, idx, position)
	changed, err = ApplyFolderOrderTx(tx, userID, order)
	return order, changed, err
}

// DeleteFolder 自持事务删除（实现迁至 DeleteFolderTx，P1 批次2）。
func DeleteFolder(id, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err := DeleteFolderTx(tx, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}
