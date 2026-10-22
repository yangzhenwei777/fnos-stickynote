package model

import "database/sql"

// SyncNoteTagsTx 导出事务内标签重建（P0-D：sync push 变异与 Web 端共用同一 tags 语义，
// 内部直接委托既有 syncNoteTagsTx，零行为变化）。
func SyncNoteTagsTx(tx *sql.Tx, noteID int64, tags string) error {
	return syncNoteTagsTx(tx, noteID, tags)
}
