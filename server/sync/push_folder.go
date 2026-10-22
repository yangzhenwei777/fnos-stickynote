package sync

import (
	"database/sql"
	"encoding/json"
	"errors"

	"stickynote/changelog"
	"stickynote/model"
	"stickynote/syncspec"
)

// push_folder.go —— FOLDER / FOLDER_ORDER push op 应用（P1 批次2，§18/§55/§56 + ADR-002 R4 + §142）。
//
// 与 NOTE 分支同一事务纪律：实体变更 + seq + ChangeLog 同提交。FOLDER_ORDER 为集合级 op：
// 逐 folder base_version CAS，任一不符 → 整 op CONFLICT 且**零提交**（§142 必测：
// "任一 base 不符整 op 409 且 A 也不提交"）；成功则整批单一 seq 产一条 FOLDER_ORDER 事件。

type folderRow struct {
	id      int64
	syncID  string
	version int64
}

func listUserFolders(tx *sql.Tx, uid int64) ([]folderRow, error) {
	rows, err := tx.Query("SELECT id, sync_id, version FROM note_folders WHERE user_id = ? ORDER BY sort_order ASC, id ASC", uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []folderRow
	for rows.Next() {
		var f folderRow
		var sid sql.NullString
		if err := rows.Scan(&f.id, &sid, &f.version); err != nil {
			return nil, err
		}
		if sid.Valid {
			f.syncID = sid.String
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// applyFolderOp FOLDER CREATE/UPDATE/DELETE 分派。返回（结果码, serverVersion, remoteVersion, 错误文案, 事件 seq）。
func applyFolderOp(tx *sql.Tx, uid int64, deviceID string, op PushOp) (string, int64, int64, string, int64) {
	switch op.Operation {
	case "CREATE":
		return folderCreate(tx, uid, deviceID, op)
	case "UPDATE":
		return folderUpdate(tx, uid, deviceID, op)
	case "DELETE":
		return folderDelete(tx, uid, deviceID, op)
	default:
		return CodeBatchProtocol, 0, 0, "folder: unknown operation", 0
	}
}

func folderCreate(tx *sql.Tx, uid int64, deviceID string, op PushOp) (string, int64, int64, string, int64) {
	var p syncspec.FolderPayload
	if err := json.Unmarshal(op.Payload, &p); err != nil || p.SyncID == "" {
		return CodeBatchProtocol, 0, 0, "payload invalid: sync_id required", 0
	}
	// §44 二层幂等（同 notes）：sync_id 已存在 → 属主一致视为已创建。
	var existID, existUser int64
	err := tx.QueryRow("SELECT id, user_id FROM note_folders WHERE sync_id = ?", p.SyncID).Scan(&existID, &existUser)
	if err == nil {
		if existUser == uid {
			var v int64
			_ = tx.QueryRow("SELECT version FROM note_folders WHERE id = ?", existID).Scan(&v)
			return CodeIdempotentReplay, v, 0, "", 0
		}
		return CodeIdentityConflict, 0, 0, "", 0
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	name, verr := model.ValidateFolderName(p.Name)
	if verr != nil {
		return CodeBatchProtocol, 0, 0, "folder name invalid", 0
	}
	var dup, count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM note_folders WHERE user_id = ? AND name = ?", uid, name).Scan(&dup); err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	if dup > 0 {
		return CodeConflict, 0, 0, "FOLDER_NAME_EXISTS", 0
	}
	if err := tx.QueryRow("SELECT COUNT(*) FROM note_folders WHERE user_id = ?", uid).Scan(&count); err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	if count >= model.MaxFoldersPerUser {
		return CodeConflict, 0, 0, "FOLDER_QUOTA_EXCEEDED", 0
	}
	var maxOrder int
	if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order),0) FROM note_folders WHERE user_id = ?", uid).Scan(&maxOrder); err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	res, err := tx.Exec("INSERT INTO note_folders (user_id, name, sort_order, sync_id, version) VALUES (?, ?, ?, ?, 1)",
		uid, name, maxOrder+1, p.SyncID)
	if err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	id, _ := res.LastInsertId()
	seq, err := changelog.AppendFolderChange(tx, uid, id, "CREATE", deviceID)
	if err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	return CodeAcked, 1, 0, "", seq
}

func folderUpdate(tx *sql.Tx, uid int64, deviceID string, op PushOp) (string, int64, int64, string, int64) {
	var p syncspec.FolderPayload
	if err := json.Unmarshal(op.Payload, &p); err != nil {
		return CodeBatchProtocol, 0, 0, "payload invalid", 0
	}
	if op.BaseVersion == nil {
		return CodeBatchProtocol, 0, 0, "base_version required", 0
	}
	name, verr := model.ValidateFolderName(p.Name)
	if verr != nil {
		return CodeBatchProtocol, 0, 0, "folder name invalid", 0
	}
	f, code, rv := resolveFolder(tx, uid, op.EntitySyncID)
	if code != "" {
		return code, 0, rv, "", 0
	}
	var dup int
	if err := tx.QueryRow("SELECT COUNT(*) FROM note_folders WHERE user_id = ? AND name = ? AND id != ?", uid, name, f.id).Scan(&dup); err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	if dup > 0 {
		return CodeConflict, 0, f.version, "FOLDER_NAME_EXISTS", 0
	}
	res, err := tx.Exec("UPDATE note_folders SET name = ?, version = version + 1 WHERE id = ? AND version = ?",
		name, f.id, *op.BaseVersion)
	if err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var cv int64
		_ = tx.QueryRow("SELECT version FROM note_folders WHERE id = ?", f.id).Scan(&cv)
		return CodeConflict, 0, cv, "base_version mismatch", 0
	}
	seq, err := changelog.AppendFolderChange(tx, uid, f.id, "UPDATE", deviceID)
	if err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	return CodeAcked, f.version + 1, 0, "", seq
}

func folderDelete(tx *sql.Tx, uid int64, deviceID string, op PushOp) (string, int64, int64, string, int64) {
	if op.BaseVersion == nil {
		return CodeBatchProtocol, 0, 0, "base_version required", 0
	}
	f, code, rv := resolveFolder(tx, uid, op.EntitySyncID)
	if code != "" {
		return code, 0, rv, "", 0
	}
	if *op.BaseVersion != f.version {
		return CodeConflict, 0, f.version, "base_version mismatch", 0
	}
	info, repointed, err := model.DeleteFolderTx(tx, f.id, uid)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrFolderNotEmpty):
			return CodeConflict, 0, f.version, "FOLDER_NOT_EMPTY", 0
		case errors.Is(err, model.ErrFolderNotFound):
			return CodeEntityGone, 0, 0, "", 0
		default:
			return "INTERNAL", 0, 0, err.Error(), 0
		}
	}
	// 墓碑事件（删除前 version 供远端冲突基点）+ §56 受影响回收站笔记逐行 UPDATE 事件。
	seq, err := changelog.NextChangeSeq(tx)
	if err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	p := syncspec.FolderPayload{SyncID: info.SyncID, Name: info.Name, Version: info.Version}
	sid := info.SyncID
	if err := changelog.AppendChange(tx, seq, uid, syncspec.EntityTypeFolder, &sid, "DELETE", int(info.Version), changelog.MarshalFolderPayload(p), deviceID); err != nil {
		return "INTERNAL", 0, 0, err.Error(), 0
	}
	for _, nid := range repointed {
		nv, err := changelog.BumpNoteVersionTx(tx, nid)
		if err != nil {
			return "INTERNAL", 0, 0, err.Error(), 0
		}
		ns, err := changelog.EnsureNoteSyncID(tx, nid)
		if err != nil {
			return "INTERNAL", 0, 0, err.Error(), 0
		}
		nseq, err := changelog.NextChangeSeq(tx)
		if err != nil {
			return "INTERNAL", 0, 0, err.Error(), 0
		}
		np, err := changelog.BuildNotePayload(tx, nid)
		if err != nil {
			return "INTERNAL", 0, 0, err.Error(), 0
		}
		if err := changelog.AppendNotePayload(tx, nseq, uid, &ns, "UPDATE", int(nv), np, deviceID); err != nil {
			return "INTERNAL", 0, 0, err.Error(), 0
		}
	}
	return CodeAcked, info.Version, 0, "", seq
}

// applyFolderOrder FOLDER_ORDER 集合级 op（§55/§142）：全序校验 → 逐 folder base CAS
// （任一不符整 op CONFLICT 零提交）→ 全量重写 sort + 逐 folder version++ → 单一 seq 事件。
// 返回 orderVersions 供客户端把后续 folder op 的 base_version 重定基（§50 同构）。
func applyFolderOrder(tx *sql.Tx, uid int64, deviceID string, op PushOp) (string, []syncspec.FolderVersionRef, int64, string, int64) {
	var p syncspec.FolderOrderPayload
	if err := json.Unmarshal(op.Payload, &p); err != nil {
		return CodeBatchProtocol, nil, 0, "payload invalid", 0
	}
	current, err := listUserFolders(tx, uid)
	if err != nil {
		return "INTERNAL", nil, 0, err.Error(), 0
	}
	// 空集幂等：无文件夹且 order 为空 → ACK 零事件。
	if len(current) == 0 && len(p.Order) == 0 {
		return CodeAcked, nil, 0, "", 0
	}
	if len(p.Order) != len(current) {
		return CodeBatchProtocol, nil, 0, "order must cover all folders exactly", 0
	}
	bySync := map[string]folderRow{}
	for _, f := range current {
		bySync[f.syncID] = f
	}
	seen := map[string]bool{}
	for _, sid := range p.Order {
		f, ok := bySync[sid]
		if !ok || seen[sid] {
			return CodeBatchProtocol, nil, 0, "order contains unknown/duplicate sync_id", 0
		}
		seen[sid] = true
		base, ok := p.Bases[sid]
		if !ok {
			return CodeBatchProtocol, nil, 0, "bases missing entry for folder", 0
		}
		// §142：任一 base 不符 → 整 op CONFLICT 且零提交（含 order 中其他 folder 也不提交）。
		if base != f.version {
			return CodeConflict, nil, f.version, "base_version mismatch on folder", 0
		}
	}
	versions := make([]syncspec.FolderVersionRef, 0, len(p.Order))
	for i, sid := range p.Order {
		f := bySync[sid]
		if _, err := tx.Exec("UPDATE note_folders SET sort_order = ? WHERE id = ?", i+1, f.id); err != nil {
			return "INTERNAL", nil, 0, err.Error(), 0
		}
		v, err := changelog.BumpFolderVersionTx(tx, f.id)
		if err != nil {
			return "INTERNAL", nil, 0, err.Error(), 0
		}
		versions = append(versions, syncspec.FolderVersionRef{SyncID: sid, Version: v})
	}
	seq, err := changelog.AppendFolderOrderChange(tx, uid, p.Order, versions, deviceID)
	if err != nil {
		return "INTERNAL", nil, 0, err.Error(), 0
	}
	return CodeAcked, versions, 0, "", seq
}

func resolveFolder(tx *sql.Tx, uid int64, syncID string) (folderRow, string, int64) {
	var f folderRow
	err := tx.QueryRow("SELECT id, version FROM note_folders WHERE user_id = ? AND sync_id = ?", uid, syncID).Scan(&f.id, &f.version)
	if errors.Is(err, sql.ErrNoRows) {
		return f, CodeEntityGone, 0
	}
	if err != nil {
		return f, "INTERNAL", 0
	}
	f.syncID = syncID
	return f, "", 0
}

// applyFolderTx FOLDER / FOLDER_ORDER op 的事务封装（与 NOTE 分支同一提交纪律：
// 实体变更 + 事件 + op 登记同提交；冲突/不存在也登记，保证 op_id 重放语义一致）。
func applyFolderTx(uid int64, deviceID string, op PushOp, reqHash string) OpResult {
	res := OpResult{OpID: op.OpID}
	tx, err := db().Begin()
	if err != nil {
		res.Code = "INTERNAL"
		res.Error = err.Error()
		return res
	}
	defer tx.Rollback()

	var seq int64
	if op.EntityType == syncspec.EntityTypeFolderOrder {
		var ovs []syncspec.FolderVersionRef
		var rv int64
		var msg string
		res.Code, ovs, rv, msg, seq = applyFolderOrder(tx, uid, deviceID, op)
		res.Error = msg
		if res.Code == CodeConflict {
			res.RemoteVersion = rv
		}
		if res.Code == CodeAcked {
			res.OrderVersions = ovs
			res.ChangeSeq = seq
		}
	} else {
		var sv, rv int64
		var msg string
		res.Code, sv, rv, msg, seq = applyFolderOp(tx, uid, deviceID, op)
		res.Error = msg
		if res.Code == CodeAcked || res.Code == CodeIdempotentReplay {
			res.ServerVersion = sv
			res.ChangeSeq = seq
		} else {
			res.RemoteVersion = rv
		}
	}
	if res.Code != CodeAcked && res.Code != CodeIdempotentReplay {
		_ = saveOperation(tx, uid, op.OpID, reqHash, res.Code, 0, "")
		if err := tx.Commit(); err != nil {
			res.Code = "INTERNAL"
			res.Error = err.Error()
		}
		return res
	}
	if err := saveOperation(tx, uid, op.OpID, reqHash, res.Code, res.ServerVersion, ""); err != nil {
		res.Code = "INTERNAL"
		res.Error = err.Error()
		return res
	}
	if err := tx.Commit(); err != nil {
		res.Code = "INTERNAL"
		res.Error = err.Error()
		return res
	}
	return res
}
