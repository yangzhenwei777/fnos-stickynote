package sync

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"stickynote/changelog"
	"stickynote/database"
	"stickynote/htmltext"
	"stickynote/model"
	"stickynote/syncspec"
)

// push.go —— POST /api/sync/push（§41~§44 + ADR-002 + ADR-004 R2）。
// 批 ≤50 op；一批一实体一 op（违例整批 400 零执行）；逐 op 独立事务：
// op_id 幂等 → 变异 CAS → seq+ChangeLog → 结果登记，四者同提交（§42）。

// 契约类型/错误码单一来源 = syncspec（本包仅别名，防双源漂移）。
type (
	PushOp      = syncspec.PushOp
	PushRequest = syncspec.PushRequest
	OpResult    = syncspec.OpResult
)

const (
	CodeAcked            = syncspec.CodeAcked
	CodeConflict         = syncspec.CodeConflict
	CodeEntityGone       = syncspec.CodeEntityGone
	CodeIdempotentReplay = syncspec.CodeIdempotentReplay
	CodeOpIDReuseError   = syncspec.CodeOpIDReuseError
	CodeIdentityConflict = syncspec.CodeIdentityConflict
	CodeBatchProtocol    = syncspec.CodeBatchProtocol
	CodeUnsupported      = syncspec.CodeUnsupported
	MaxOpsPerBatch       = syncspec.MaxOpsPerBatch
)

func opRequestHash(op PushOp) string {
	b, _ := json.Marshal(op)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func Push(c *gin.Context) {
	userID, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}
	uid := userID.(int64)
	var req PushRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid push request", "code": CodeBatchProtocol})
		return
	}
	if len(req.Ops) == 0 {
		c.JSON(http.StatusOK, gin.H{"results": []OpResult{}})
		return
	}
	if len(req.Ops) > MaxOpsPerBatch {
		c.JSON(http.StatusBadRequest, gin.H{"error": "batch too large", "code": CodeBatchProtocol})
		return
	}
	// ADR-002 R2：批级协议校验先于一切执行——同实体双 op 整批拒绝。
	seen := map[string]bool{}
	for _, op := range req.Ops {
		key := op.EntityType + "|" + op.EntitySyncID
		// ADR-002 R4：FOLDER_ORDER 空 entity_sync_id 视作单例键，同样参与批内判重。
		if op.EntitySyncID != "" || op.EntityType == syncspec.EntityTypeFolderOrder {
			if seen[key] {
				c.JSON(http.StatusBadRequest, gin.H{"error": "duplicate entity op in batch", "code": CodeBatchProtocol})
				return
			}
			seen[key] = true
		}
	}

	results := make([]OpResult, 0, len(req.Ops))
	for _, op := range req.Ops {
		results = append(results, applyOp(uid, req.DeviceID, op))
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

func applyOp(uid int64, deviceID string, op PushOp) OpResult {
	res := OpResult{OpID: op.OpID}
	if op.OpID == "" || op.EntityType == "" || op.Operation == "" {
		res.Code = CodeBatchProtocol
		res.Error = "op_id/entity_type/operation required"
		return res
	}
	// §8/§43 op_id 幂等双键。
	existing, err := lookupOperation(uid, op.OpID)
	if err != nil {
		res.Code = "INTERNAL"
		res.Error = err.Error()
		return res
	}
	reqHash := opRequestHash(op)
	if existing != nil {
		if existing.RequestSha256 == reqHash {
			res.Code = CodeIdempotentReplay
			res.ServerVersion = existing.ResultVersion
			return res
		}
		// E4-2（2026-09-23 真机实锤）：**未生效**（result_version==0：CONFLICT/GONE 等
		// 挂账结果）的 op_id 允许客户端 rebase 后以新内容重推**重新评估**——saveOperation
		// 为 UPSERT 会覆盖旧登记。只有**已生效**（result_version>0）的 op_id 才强制
		// 请求身份不变（first-write-wins + 防篡改）。此前未生效重推被 REUSE 拒收，
		// 客户端转 STALE → 用户被迫人工重投（「以本机为准重投」后仍可能再次 REUSE）。
		if existing.ResultVersion > 0 {
			res.Code = CodeOpIDReuseError
			res.Error = "same op_id with different payload"
			return res
		}
	}
	// ADR-004 R3：NOTE CREATE/UPDATE 的 attachment_refs 必须先经 /api/sync/upload 到达。
	// 缺失 -> ATTACHMENT_NOT_FOUND 且**不登记 op、零副作用**：客户端重传缺失文件后
	// 同一 op_id 同一 payload 原样重试不被幂等表挡（R3 自恢复路径的前提）。
	if op.EntityType == "NOTE" && (op.Operation == "CREATE" || op.Operation == "UPDATE") {
		var probe syncspec.NotePayload
		if err := json.Unmarshal(op.Payload, &probe); err == nil {
			if missing := checkAttachmentRefs(uid, probe.AttachmentRefs); len(missing) > 0 {
				res.Code = syncspec.CodeAttachmentNotFound
				res.Error = attachmentNotFound(missing)
				return res
			}
		}
	}
	switch op.EntityType {
	case "NOTE":
	case syncspec.EntityTypeFolder, syncspec.EntityTypeFolderOrder:
		// P1 批次2：文件夹实体/集合级 op 独立事务路径（同一提交纪律，push_folder.go）。
		return applyFolderTx(uid, deviceID, op, reqHash)
	default:
		res.Code = CodeUnsupported
		res.Error = "unsupported entity type"
		return res
	}

	tx, err := db().Begin()
	if err != nil {
		res.Code = "INTERNAL"
		res.Error = err.Error()
		return res
	}
	defer tx.Rollback()

	var noteID int64
	var serverVersion int64
	switch op.Operation {
	case "CREATE":
		noteID, serverVersion, res.Code, res.Error = opCreate(tx, uid, op)
	case "UPDATE":
		noteID, serverVersion, res.Code, res.Error = opCasUpdate(tx, uid, op)
	case "DELETE":
		noteID, serverVersion, res.Code, res.Error = opSoftDelete(tx, uid, op)
	case "RESTORE":
		noteID, serverVersion, res.Code, res.Error = opRestore(tx, uid, op)
	case syncspec.OperationPurge:
		// E8 双向闭环（2026-09-23 用户实测缺口）：桌面端彻底删/清空回收站推送 PURGE。
		noteID, serverVersion, res.Code, res.Error = opPurge(tx, uid, op)
	default:
		res.Code = CodeBatchProtocol
		res.Error = "unknown operation"
		return res
	}
	if res.Code != CodeAcked {
		res.RemoteVersion = serverVersion // CONFLICT/ENTITY_GONE 携带远端当前版本（§54 解决基点）
		// 冲突/不存在也登记（同 op_id 重放语义一致），但实体零副作用。
		_ = saveOperation(tx, uid, op.OpID, reqHash, res.Code, 0, "")
		if err := tx.Commit(); err != nil {
			res.Code = "INTERNAL"
			res.Error = err.Error()
		}
		return res
	}

	seq, err := nextChangeSeq(tx)
	if err != nil {
		res.Code = "INTERNAL"
		res.Error = err.Error()
		return res
	}
	// E8（2026-09-23）：PURGE 行已物理删，buildNotePayload 不可用——事件 payload 只需
	// sync_id（客户端按 ch.EntitySyncID + payload.sync_id 定位，不依赖行内容）。
	// §32 P1-b：NOTE 事件统一走 AppendNotePayload（≤4MB 内联 / >4MB blob_ref 自动分流）。
	if op.Operation == syncspec.OperationPurge {
		b, _ := json.Marshal(map[string]string{"sync_id": op.EntitySyncID})
		if err := appendChange(tx, seq, uid, "NOTE", &op.EntitySyncID, op.Operation, int(serverVersion), string(b), deviceID); err != nil {
			res.Code = "INTERNAL"
			res.Error = err.Error()
			return res
		}
	} else {
		p, err := buildNotePayload(tx, noteID)
		if err != nil {
			res.Code = "INTERNAL"
			res.Error = err.Error()
			return res
		}
		if err := changelog.AppendNotePayload(tx, seq, uid, &op.EntitySyncID, op.Operation, int(serverVersion), p, deviceID); err != nil {
			res.Code = "INTERNAL"
			res.Error = err.Error()
			return res
		}
	}
	res.ServerVersion = serverVersion
	res.ChangeSeq = seq
	if err := saveOperation(tx, uid, op.OpID, reqHash, CodeAcked, serverVersion, ""); err != nil {
		res.Code = "INTERNAL"
		res.Error = err.Error()
		return res
	}
	if err := tx.Commit(); err != nil {
		res.Code = "INTERNAL"
		res.Error = err.Error()
		return res
	}
	res.Code = CodeAcked
	return res
}

// classifyMiss §44/ADR-004 R2：CAS 0 行命中 → 区分 物理缺行(ENTITY_GONE) / 版本不符(CONFLICT)。
func classifyMiss(tx *sql.Tx, uid int64, syncID string, wantLive bool) (int64, string) {
	var v int64
	var live int
	err := tx.QueryRow(`SELECT version, CASE WHEN deleted_at IS NULL THEN 1 ELSE 0 END FROM notes WHERE user_id = ? AND sync_id = ?`, uid, syncID).Scan(&v, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, CodeEntityGone
	}
	if err != nil {
		return 0, "INTERNAL"
	}
	if wantLive && live == 0 {
		return v, CodeEntityGone // 服务端已删（墓碑在链上），本地仍在其后编辑 → 远端=永久删除语义
	}
	return v, CodeConflict
}

func opCreate(tx *sql.Tx, uid int64, op PushOp) (int64, int64, string, string) {
	var p NotePayload
	if err := json.Unmarshal(op.Payload, &p); err != nil || p.SyncID == "" {
		return 0, 0, CodeBatchProtocol, "payload invalid: sync_id required"
	}
	// §44 第二层幂等：sync_id 已存在 → 属主一致视为已创建，不一致身份冲突。
	var existID, existUser int64
	err := tx.QueryRow(`SELECT id, user_id FROM notes WHERE sync_id = ?`, p.SyncID).Scan(&existID, &existUser)
	if err == nil {
		if existUser == uid {
			return existID, 0, CodeIdempotentReplay, ""
		}
		return 0, 0, CodeIdentityConflict, ""
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, "INTERNAL", err.Error()
	}
	var folderID *int64
	if p.FolderSyncID != nil && *p.FolderSyncID != "" {
		var fid int64
		if err := tx.QueryRow(`SELECT id FROM note_folders WHERE user_id = ? AND sync_id = ?`, uid, *p.FolderSyncID).Scan(&fid); err != nil {
			return 0, 0, CodeBatchProtocol, "unknown folder_sync_id"
		}
		folderID = &fid
	}
	// v21.75 纪律：version 列 DEFAULT 1，与 Web 创建路径一致。
	res, err := tx.Exec(`INSERT INTO notes (user_id, title, content, content_text, tags, is_encrypted,
			encrypt_password_hash, encrypted_content, folder_id, sync_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, p.Title, p.Content, htmltext.ToText(p.Content), p.Tags, p.IsEncrypted,
		p.EncryptPasswordHash, p.EncryptedContent, folderID, p.SyncID)
	if err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	id, _ := res.LastInsertId()
	if err := model.SyncNoteTagsTx(tx, id, p.Tags); err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	if err := database.SyncNoteUploadsTx(tx, id, uid, p.Content); err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	return id, 1, CodeAcked, ""
}

func opCasUpdate(tx *sql.Tx, uid int64, op PushOp) (int64, int64, string, string) {
	var p NotePayload
	if err := json.Unmarshal(op.Payload, &p); err != nil || op.BaseVersion == nil {
		return 0, 0, CodeBatchProtocol, "payload/base_version required"
	}
	var noteID int64
	err := tx.QueryRow(`SELECT id FROM notes WHERE user_id = ? AND sync_id = ?`, uid, op.EntitySyncID).Scan(&noteID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, CodeEntityGone, ""
	}
	if err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	// v21.169（用户实机缺陷）：笔记「移动到其他文件夹」的同步落点。
	// 此前 opCreate 会写 folder_id、而 opCasUpdate 的 UPDATE 语句**完全没有 folder_id**
	// ⇒ 客户端把 op 推上来了、服务端也 ACK 了，但文件夹根本没变，双端均表现为「移动不同步」。
	// 此处按 opCreate（L259-266）同一语义补齐：folder_sync_id 为空/nil ⇒ 回默认文件夹（NULL）。
	var folderID *int64
	if p.FolderSyncID != nil && *p.FolderSyncID != "" {
		var fid int64
		if err := tx.QueryRow(`SELECT id FROM note_folders WHERE user_id = ? AND sync_id = ?`, uid, *p.FolderSyncID).Scan(&fid); err != nil {
			return 0, 0, CodeBatchProtocol, "unknown folder_sync_id"
		}
		folderID = &fid
	}
	// 附件关系以 refs 为权威（§29/§67）：先删旧关系再按 refs 重建（文件本体由上传通道先行保证）。
	res, err := tx.Exec(`UPDATE notes SET title = ?, content = ?, content_text = ?, tags = ?, folder_id = ?,
			version = version + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND version = ?`,
		p.Title, p.Content, htmltext.ToText(p.Content), p.Tags, folderID, noteID, uid, *op.BaseVersion)
	if err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	if n, _ := res.RowsAffected(); n == 0 {
		rv, code := classifyMiss(tx, uid, op.EntitySyncID, true)
		return 0, rv, code, "base_version mismatch"
	}
	if err := model.SyncNoteTagsTx(tx, noteID, p.Tags); err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	if err := database.SyncNoteUploadsTx(tx, noteID, uid, p.Content); err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	var v int64
	_ = tx.QueryRow(`SELECT version FROM notes WHERE id = ?`, noteID).Scan(&v)
	return noteID, v, CodeAcked, ""
}

// opPurge §59 永久删（E8 双向闭环，2026-09-23 用户实测缺口补全）：
// 桌面端"彻底删除/清空回收站"推送 PURGE 的**服务端应用路径**（此前只有服务端→客户端方向）。
// 语义：
//   - 仅允许删除**已软删**（回收站内）的行；活行收到 PURGE → CONFLICT 拒绝（防误删，
//     客户端应先软删再彻底删）；
//   - 行不存在（已删过/幂等重放）→ IDEMPOTENT_REPLAY（目标态"不存在"已达成）；
//   - 物理删后由 applyOp 尾段发 PURGE 事件（本端自回声由 device_id 跳过，他端据此清理）。
func opPurge(tx *sql.Tx, uid int64, op PushOp) (int64, int64, string, string) {
	var id, ver int64
	var deletedAt sql.NullString
	err := tx.QueryRow(`SELECT id, version, deleted_at FROM notes
		WHERE user_id = ? AND sync_id = ?`, uid, op.EntitySyncID).Scan(&id, &ver, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, CodeIdempotentReplay, ""
	}
	if err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	if !deletedAt.Valid {
		return id, ver, CodeConflict, "purge requires trashed note (soft-delete first)"
	}
	if _, err := tx.Exec(`DELETE FROM notes WHERE id = ? AND user_id = ?`, id, uid); err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	return id, ver, CodeAcked, ""
}

func opSoftDelete(tx *sql.Tx, uid int64, op PushOp) (int64, int64, string, string) {
	if op.BaseVersion == nil {
		return 0, 0, CodeBatchProtocol, "base_version required"
	}
	var noteID int64
	err := tx.QueryRow(`SELECT id FROM notes WHERE user_id = ? AND sync_id = ?`, uid, op.EntitySyncID).Scan(&noteID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, CodeEntityGone, ""
	}
	res, err := tx.Exec(`UPDATE notes SET deleted_at = CURRENT_TIMESTAMP, version = version + 1
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND version = ?`, noteID, uid, *op.BaseVersion)
	if err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	if n, _ := res.RowsAffected(); n == 0 {
		rv, code := classifyMiss(tx, uid, op.EntitySyncID, true)
		return 0, rv, code, "base_version mismatch"
	}
	var v int64
	_ = tx.QueryRow(`SELECT version FROM notes WHERE id = ?`, noteID).Scan(&v)
	return noteID, v, CodeAcked, ""
}

func opRestore(tx *sql.Tx, uid int64, op PushOp) (int64, int64, string, string) {
	if op.BaseVersion == nil {
		return 0, 0, CodeBatchProtocol, "base_version required"
	}
	var noteID int64
	err := tx.QueryRow(`SELECT id FROM notes WHERE user_id = ? AND sync_id = ?`, uid, op.EntitySyncID).Scan(&noteID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, CodeEntityGone, ""
	}
	res, err := tx.Exec(`UPDATE notes SET deleted_at = NULL, trash_upload_bytes = 0, version = version + 1
		WHERE id = ? AND user_id = ? AND deleted_at IS NOT NULL AND version = ?`, noteID, uid, *op.BaseVersion)
	if err != nil {
		return 0, 0, "INTERNAL", err.Error()
	}
	if n, _ := res.RowsAffected(); n == 0 {
		rv, code := classifyMiss(tx, uid, op.EntitySyncID, false)
		return 0, rv, code, "base_version mismatch"
	}
	var v int64
	_ = tx.QueryRow(`SELECT version FROM notes WHERE id = ?`, noteID).Scan(&v)
	return noteID, v, CodeAcked, ""
}
