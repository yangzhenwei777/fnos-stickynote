// payload.go —— Canonical Sync Payload（v20 §28/§29/§63）。
// 类型别名自 syncspec（单一契约源，桌面 synclocal 共用同一形状）。
// 构造逻辑已下沉 changelog 包（P2 批次1，Web 写路径共用同一构造器防双源漂移）。
package sync

import (
	"database/sql"

	"stickynote/changelog"
	"stickynote/syncspec"
)

type (
	AttachmentRef = syncspec.AttachmentRef
	NotePayload   = syncspec.NotePayload
)

// buildNotePayload 从权威行构造事件载荷（服务端为准，不信任客户端回传形状）。
func buildNotePayload(tx *sql.Tx, noteID int64) (NotePayload, error) {
	return changelog.BuildNotePayload(tx, noteID)
}

func marshalPayload(p NotePayload) string {
	return changelog.MarshalPayload(p)
}
