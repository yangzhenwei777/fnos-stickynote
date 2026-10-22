package synclocal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"stickynote/syncspec"
)

// conflicts_resolve.go —— 冲突**处置面**（v21.166 计划项3 · 自 conflicts.go 纯拆分）。
//
// 本文件自 conflicts.go 按职责切分而来，**零逻辑改动**（纯搬家）。
// 处置三种语义互不重叠，且只在服务端权威内容已知时才允许覆盖本机：
//   keep-local   以本机为准重投（脏链回 PENDING，CAS 基点重定到远端新版本）
//   keep-remote  以远端为准覆盖本机（必须 confirmed=true，红线 1/6）
//   drop-local   放弃本机这笔（只终止 + 版本对齐，不假装"应用远端"）
// 处置与 outbox 链流转同事务；不推进 cursor（cursor 早于冲突产生时同事务推进过）。

type ResolveResult struct {
	ConflictID    int64  `json:"conflict_id"`
	Kind          string `json:"kind"`
	EntityType    string `json:"entity_type"`
	EntitySyncID  string `json:"entity_sync_id"`
	Resolution    string `json:"resolution"`
	RequeuedOps   int    `json:"requeued_ops"`   // keep-local：回到 PENDING 的 op 数
	TerminatedOps int    `json:"terminated_ops"` // keep-remote/drop-local：被终止的 op 数
	AppliedRemote bool   `json:"applied_remote"` // 是否真的把远端内容落到本机
}

// ResolveConflict 用户显式处置一条冲突。confirmed 仅对 keep-remote 有意义（破坏性动作）。
func (w *Worker) ResolveConflict(conflictID int64, resolution string, confirmed bool) (*ResolveResult, error) {
	if w == nil || w.db == nil {
		return nil, errors.New("同步引擎未初始化")
	}
	var (
		c Conflict
	)
	err := w.db.QueryRow(`SELECT id, entity_sync_id, entity_type, operation, op_id, kind,
		local_payload, remote_payload, base_version, remote_version, status
		FROM sync_conflicts WHERE id = ?`, conflictID).
		Scan(&c.ID, &c.EntitySyncID, &c.EntityType, &c.Operation, &c.OpID, &c.Kind,
			&c.LocalPayload, &c.RemotePayload, &c.BaseVersion, &c.RemoteVersion, &c.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConflictNotFound
	}
	if err != nil {
		return nil, err
	}
	if c.Status != "open" {
		return nil, fmt.Errorf("%w（处置方式 %s，%s）", ErrConflictAlreadyResolved, c.Resolution, c.ResolvedAt)
	}
	if c.EntityType == "" {
		c.EntityType = InferEntityType(w.db, c.EntitySyncID)
	}
	switch resolution {
	case ResolveKeepLocal, ResolveKeepRemote, ResolveDropLocal, ResolveRecreate:
	default:
		return nil, fmt.Errorf("%w: %s", ErrResolveUnknown, resolution)
	}
	allowed := allowedResolutions(w.db, c.Kind, c.EntityType, c.EntitySyncID, c.Operation, c.RemotePayload)
	if !containsStr(allowed, resolution) {
		// 1.0.218：recreate 被拒时给**真因**（行不在 / 在回收站 / 类型不适用），
		// 而非笼统"不支持此处置"——用户需要知道下一步点哪个按钮。
		if resolution == ResolveRecreate {
			return nil, recreateRefusalReason(w.db, &c)
		}
		// 旧库遗留行缺操作/远端内容 → 矩阵本就不放行；单独给真因，避免"不支持此处置"误导用户
		// （DELETE 与 UPDATE 的应用分支不同，信息不全时猜语义就是数据损失）。
		if resolution == ResolveKeepRemote && (c.Operation == "" || c.RemotePayload == "") {
			return nil, fmt.Errorf("%w：%s 冲突记录缺远端内容或操作类型（旧版本产生），不能自动覆盖本机",
				ErrResolveUnsupported, c.EntityType)
		}
		return nil, fmt.Errorf("%w：%s 不适用于 %s/%s 类型冲突", ErrResolveUnsupported, resolution, c.EntityType, c.Kind)
	}
	if resolution == ResolveKeepRemote && !confirmed {
		return nil, ErrResolveRequiresConfirm
	}
	if resolution == ResolveKeepRemote && !localEntityExists(w.db, c.EntityType, c.EntitySyncID) {
		// applyNoteChange/applyFolderChange 对缺失行是"INSERT 补建"语义（pull 场景下正确，
		// 因为那行本该存在）。处置场景里本机已经没有这行（用户已彻底删除），照原路走会凭空
		// 造出一条笔记——fail-closed，改道"放弃这笔"。
		return nil, ErrConflictEntityMissing
	}
	// 1.0.218 recreate：**事务外**构建重建载荷（全部读到位再进事务——v21.177 教训：
	// 持连接期间的嵌套查询 = 死锁源，构建器一律不放进事务）。
	var recreate *recreatePlan
	if resolution == ResolveRecreate {
		p, err := buildRecreatePlan(w.db, &c)
		if err != nil {
			return nil, err
		}
		recreate = p
	}

	tx, err := w.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res := &ResolveResult{ConflictID: c.ID, Kind: c.Kind, EntityType: c.EntityType,
		EntitySyncID: c.EntitySyncID, Resolution: resolution}

	switch resolution {
	case ResolveKeepLocal:
		n, err := w.requeueChainTx(tx, &c)
		if err != nil {
			return nil, err
		}
		res.RequeuedOps = n
	case ResolveKeepRemote:
		// 复用 pull 的同一套应用函数（禁双源：另写一份覆盖逻辑必然与服务端语义漂移）。
		ch := syncspec.ChangeDTO{
			EntityType:    c.EntityType,
			EntitySyncID:  strPtr(c.EntitySyncID),
			Operation:     c.Operation,
			ServerVersion: c.RemoteVersion,
			Payload:       json.RawMessage(c.RemotePayload),
		}
		switch c.EntityType {
		case syncspec.EntityTypeFolder:
			if err := w.applyFolderChange(tx, ch); err != nil {
				return nil, fmt.Errorf("应用远端文件夹失败: %w", err)
			}
		default:
			if err := w.applyNoteChange(tx, ch); err != nil {
				return nil, fmt.Errorf("应用远端笔记失败: %w", err)
			}
		}
		res.AppliedRemote = true
		n, err := terminateChainTx(tx, c.EntityType, c.EntitySyncID, "superseded-by-remote")
		if err != nil {
			return nil, err
		}
		res.TerminatedOps = n
	case ResolveDropLocal:
		n, err := terminateChainTx(tx, c.EntityType, c.EntitySyncID, "dropped-by-user")
		if err != nil {
			return nil, err
		}
		res.TerminatedOps = n
		// 版本对齐：本机内容不动，但把行版本抬到已知的服务端版本，避免下次编辑必然撞 409。
		if c.RemoteVersion > 0 {
			if err := alignLocalVersionTx(tx, c.EntityType, c.EntitySyncID, c.RemoteVersion); err != nil {
				return nil, err
			}
		}
	case ResolveRecreate:
		// 1.0.218「以本机内容重建到服务器」：终止旧链（原 op 对不存在实体重投必再撞 GONE）→
		// 以本机**当前行内容**新排一笔 CREATE（randID 新 op_id；载荷已在事务外构建）。
		// 服务端 opCreate 幂等兜底：sync_id 不存在→INSERT（这就是"服务端本无此实体"的正常路径）；
		// 已存在同属主→IDEMPOTENT_REPLAY（极窄窗口：服务端行在回收站等，不再多猜）。
		n, err := terminateChainTx(tx, c.EntityType, c.EntitySyncID, "recreated-as-create")
		if err != nil {
			return nil, err
		}
		res.TerminatedOps = n
		if _, err := tx.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload)
			VALUES (?, ?, ?, 'CREATE', ?, ?)`,
			randID(), recreate.entityType, c.EntitySyncID, recreate.base, recreate.payload); err != nil {
			return nil, err
		}
		res.RequeuedOps = 1
	}

	if _, err := tx.Exec(`UPDATE sync_conflicts SET status='resolved', resolution=?,
		resolved_at=CURRENT_TIMESTAMP WHERE id = ? AND status='open'`, resolution, c.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

// requeueChainTx keep-local：脏链回 PENDING 且 CAS 基点重定到远端新版本（§50 同构——
// ACK 后重写后续 PENDING 的 base 是 worker 的既有纪律，人工处置必须走同一基点口径）。
func (w *Worker) requeueChainTx(tx *sql.Tx, c *Conflict) (int, error) {
	if c.EntityType == syncspec.EntityTypeFolderOrder {
		// 集合排序冲突：① 成员链按远端逐夹权威版本（payload.versions，缺失退到 bases）重定基后回
		// PENDING；② 本机的 order op 行（entity_sync_id 恒空串，§55）也回 PENDING，其 payload.bases
		// 交给既有 rebaseOrderPayloads 从本地权威版本重算（禁另写一份基线重写逻辑）。
		vers := folderOrderVersions(c.RemotePayload)
		total := 0
		for sid, v := range vers {
			n, err := requeueEntityTx(tx, sid, v)
			if err != nil {
				return 0, err
			}
			total += n
		}
		// E4-2：order op 回队也换新 op_id（base 重定后 hash 必变，见 requeueEntityTx 注释）。
		res, err := tx.Exec(`UPDATE sync_outbox SET status='PENDING', retry_count=0, last_error='',
			op_id = lower(hex(randomblob(12))),
			updated_at=CURRENT_TIMESTAMP WHERE entity_type='FOLDER_ORDER' AND status IN ('CONFLICT','FAILED')`)
		if err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			total += int(k)
		}
		if err := w.rebaseOrderPayloads(); err != nil {
			return 0, err
		}
		return total, nil
	}
	return requeueEntityTx(tx, c.EntitySyncID, c.RemoteVersion)
}

func requeueEntityTx(tx *sql.Tx, entitySyncID string, base int64) (int, error) {
	var v *int64
	if base > 0 {
		v = &base
	}
	var n int64
	// E4-2（2026-09-23 真机账本 seq1 实锤）：重投后 base 重定 → opRequestHash（含 base/payload）
	// 必变 → 同 op_id 重推会被服务端幂等表判 REUSE（若首推已登记过）→ 二次 STALE。
	// 不变量：**请求内容改写必须换请求身份**——重投一律换新 op_id。
	if v != nil {
		res, err := tx.Exec(`UPDATE sync_outbox SET status='PENDING', base_version=?, retry_count=0,
			last_error='', op_id = lower(hex(randomblob(12))), updated_at=CURRENT_TIMESTAMP
			WHERE entity_sync_id=? AND status IN ('CONFLICT','FAILED')`, *v, entitySyncID)
		if err != nil {
			return 0, err
		}
		n, _ = res.RowsAffected()
	} else {
		res, err := tx.Exec(`UPDATE sync_outbox SET status='PENDING', retry_count=0, last_error='',
			op_id = lower(hex(randomblob(12))), updated_at=CURRENT_TIMESTAMP
			WHERE entity_sync_id=? AND status IN ('CONFLICT','FAILED')`, entitySyncID)
		if err != nil {
			return 0, err
		}
		n, _ = res.RowsAffected()
	}
	return int(n), nil
}

// terminateChainTx 终止该实体的未推 op（keep-remote / drop-local 共用）。
func terminateChainTx(tx *sql.Tx, entityType, entitySyncID, reason string) (int, error) {
	var res sql.Result
	var err error
	if entityType == syncspec.EntityTypeFolderOrder || entitySyncID == "" {
		// FOLDER_ORDER 的 op 行 entity_sync_id 恒为空串（§55 集合级），只能按类型终止。
		res, err = tx.Exec(`UPDATE sync_outbox SET status='ACKED', last_error=?, updated_at=CURRENT_TIMESTAMP
			WHERE entity_type=? AND status IN ('PENDING','SENDING','FAILED','CONFLICT')`, reason, entityType)
	} else {
		res, err = tx.Exec(`UPDATE sync_outbox SET status='ACKED', last_error=?, updated_at=CURRENT_TIMESTAMP
			WHERE entity_sync_id=? AND status IN ('PENDING','SENDING','FAILED','CONFLICT')`, reason, entitySyncID)
	}
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// alignLocalVersionTx 把本机行版本抬到已知服务端版本（内容不动）。用 MAX 语义保证不倒退，
// 避免"放弃这笔"之后本机带着旧基点反复撞 409。
func alignLocalVersionTx(tx *sql.Tx, entityType, entitySyncID string, remoteVer int64) error {
	if entitySyncID == "" && entityType != syncspec.EntityTypeFolderOrder {
		return nil // 遗留行连实体都定位不到：宁可不抬版本，也不能猜一张表去写
	}
	switch entityType {
	case syncspec.EntityTypeFolder:
		_, err := tx.Exec(`UPDATE note_folders SET version = ? WHERE sync_id = ? AND version < ?`,
			remoteVer, entitySyncID, remoteVer)
		return err
	case syncspec.EntityTypeFolderOrder:
		return nil // 排序 op 无自身行版本，成员版本由 order 权威回写
	default:
		_, err := tx.Exec(`UPDATE notes SET version = ? WHERE sync_id = ? AND version < ?`,
			remoteVer, entitySyncID, remoteVer)
		return err
	}
}

// allowedResolutions 处置矩阵的唯一事实源（UI 与后端同源校验，避免前端按钮与服务端结论不一致）。
// 1.0.218：recreate 可用性要看本机行是否仍在（ENTITY_GONE × NOTE/FOLDER × 行在且未在回收站），
// 故签名收进 db 与 syncID；其余组合判定不变。
func allowedResolutions(db *sql.DB, kind, entityType, syncID, operation, remotePayload string) []string {
	switch kind {
	case KindConflictOnPull:
		if entityType == syncspec.EntityTypeFolderOrder {
			return []string{ResolveKeepLocal}
		}
		// keep-remote 只发给"实体类型明确 + 远端内容与操作类型齐备"的行：类型未知时 apply 分支
		// 会落到 notes（默认分支），等于拿文件夹冲突去写笔记表——必须挡在矩阵这一层。
		if entityType != syncspec.EntityTypeNote && entityType != syncspec.EntityTypeFolder {
			return []string{ResolveKeepLocal, ResolveDropLocal}
		}
		if operation == "" || remotePayload == "" {
			return []string{ResolveKeepLocal, ResolveDropLocal}
		}
		return []string{ResolveKeepLocal, ResolveKeepRemote, ResolveDropLocal}
	case KindPushConflict, KindRetryExhausted:
		return []string{ResolveKeepLocal, ResolveDropLocal}
	case KindAttachmentDiverged:
		// 同名附件内容分歧：重投必再撞（服务端那份哈希不动），故只允许放弃本机这笔；
		// 真要保本地内容请走"另存副本"（刀2）后由新建路径产生全新文件名。
		return []string{ResolveDropLocal}
	case KindEntityGone:
		// 1.0.218：实体在服务端不存在。若**本机行仍在**（未在回收站），允许「以本机内容重建到
		// 服务器」（recreate：换新 op_id 的 CREATE 重排队）；本机行不在则连重建的料都没有，
		// 只允许放弃（要保内容走另存副本——payload 里还留着快照）。
		if recreateEligible(db, entityType, syncID) {
			return []string{ResolveRecreate, ResolveDropLocal}
		}
		return []string{ResolveDropLocal}
	case KindStale:
		// 身份/请求身份层面已无望（OP_ID_REUSE / IDENTITY_CONFLICT）：原样重投必再撞，
		// 且身份冲突换 CREATE 同样撞——保持只允许放弃（recreate 不得向此 kind 扩散）。
		return []string{ResolveDropLocal}
	default:
		return []string{ResolveDropLocal}
	}
}

// recreateEligible 本机行仍在（笔记且未在回收站）⇒ 允许「以本机内容重建到服务器」。
// fail-closed 口径：判定不了（无 db / syncID 空 / 类型不认）一律不放行。
func recreateEligible(db *sql.DB, entityType, syncID string) bool {
	if db == nil || syncID == "" {
		return false
	}
	var one int
	switch entityType {
	case syncspec.EntityTypeFolder:
		return db.QueryRow(`SELECT 1 FROM note_folders WHERE sync_id = ? LIMIT 1`, syncID).Scan(&one) == nil
	case syncspec.EntityTypeNote:
		return db.QueryRow(`SELECT 1 FROM notes WHERE sync_id = ? AND deleted_at IS NULL LIMIT 1`, syncID).Scan(&one) == nil
	default:
		return false
	}
}

// recreateRefusalReason recreate 被拒时的真因（行不在 / 在回收站 / 类型或 kind 不适用），
// 让 UI 能直接上屏"下一步该点哪个按钮"。
func recreateRefusalReason(db *sql.DB, c *Conflict) error {
	if c.Kind != KindEntityGone {
		return fmt.Errorf("%w：「以本机内容重建到服务器」仅适用于「远端该实体已被删除」类冲突",
			ErrResolveUnsupported)
	}
	if c.EntityType != syncspec.EntityTypeNote && c.EntityType != syncspec.EntityTypeFolder {
		return fmt.Errorf("%w：「以本机内容重建到服务器」仅支持笔记与文件夹", ErrResolveUnsupported)
	}
	if c.EntityType == syncspec.EntityTypeNote && localNoteInTrash(db, c.EntitySyncID) {
		return ErrRecreateInTrash
	}
	return ErrRecreateEntityMissing
}

// localNoteInTrash 该 sync_id 的笔记行是否在回收站（distinguish"没有"与"在回收站"）。
func localNoteInTrash(db *sql.DB, syncID string) bool {
	if db == nil || syncID == "" {
		return false
	}
	var one int
	return db.QueryRow(`SELECT 1 FROM notes WHERE sync_id = ? AND deleted_at IS NOT NULL LIMIT 1`,
		syncID).Scan(&one) == nil
}

// recreatePlan「以本机内容重建到服务器」的事前构建产物（事务外读齐，事务内只写）。
type recreatePlan struct {
	entityType string
	payload    string
	base       int64
}

// buildRecreatePlan 事务外构建重建载荷：以本机**当前行**为准（服务端本无此实体，无覆盖风险），
// 与 EnqueueNoteOp/EnqueueFolderOp 同源（BuildNotePayload / FolderPayload），禁另写一份构造。
func buildRecreatePlan(db *sql.DB, c *Conflict) (*recreatePlan, error) {
	if c.EntityType == syncspec.EntityTypeFolder {
		var id int64
		err := db.QueryRow(`SELECT id FROM note_folders WHERE sync_id = ?`, c.EntitySyncID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRecreateEntityMissing
		}
		if err != nil {
			return nil, err
		}
		r, ok := GetFolderRef(db, id)
		if !ok {
			return nil, ErrRecreateEntityMissing
		}
		b, _ := json.Marshal(syncspec.FolderPayload{SyncID: r.SyncID, Name: r.Name, Version: r.Version})
		return &recreatePlan{entityType: syncspec.EntityTypeFolder, payload: string(b), base: r.Version}, nil
	}
	var (
		id      int64
		deleted sql.NullString
		version int64
	)
	err := db.QueryRow(`SELECT id, deleted_at, version FROM notes WHERE sync_id = ?`,
		c.EntitySyncID).Scan(&id, &deleted, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRecreateEntityMissing
	}
	if err != nil {
		return nil, err
	}
	if deleted.Valid {
		return nil, ErrRecreateInTrash
	}
	payload, err := BuildNotePayload(db, id)
	if err != nil {
		return nil, err
	}
	return &recreatePlan{entityType: syncspec.EntityTypeNote, payload: payload, base: version}, nil
}

// folderOrderVersions 从远端 FOLDER_ORDER payload 解出逐夹权威版本（缺失时退到 bases）。
func folderOrderVersions(raw string) map[string]int64 {
	out := map[string]int64{}
	if raw == "" {
		return out
	}
	var op syncspec.FolderOrderPayload
	if err := json.Unmarshal([]byte(raw), &op); err != nil {
		return out
	}
	for sid, v := range op.Bases {
		out[sid] = v
	}
	for _, vr := range op.Versions {
		out[vr.SyncID] = vr.Version
	}
	return out
}

// InferEntityType 按 sync_id 反查本地实体类型（旧库遗留行补型用；1.0.218 起导出，
// 供绑定层"另存副本"同口径补型——禁另写一份推断）。
func InferEntityType(db *sql.DB, syncID string) string {
	if syncID == "" {
		return syncspec.EntityTypeFolderOrder
	}
	var one int
	if err := db.QueryRow(`SELECT 1 FROM notes WHERE sync_id = ? LIMIT 1`, syncID).Scan(&one); err == nil {
		return syncspec.EntityTypeNote
	}
	if err := db.QueryRow(`SELECT 1 FROM note_folders WHERE sync_id = ? LIMIT 1`, syncID).Scan(&one); err == nil {
		return syncspec.EntityTypeFolder
	}
	return ""
}

func countOpenOps(db *sql.DB, entityType, syncID string) int {
	var n int
	if entityType == syncspec.EntityTypeFolderOrder || syncID == "" {
		_ = db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity_type='FOLDER_ORDER'
			AND status IN ('PENDING','SENDING','FAILED','CONFLICT')`).Scan(&n)
		return n
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity_sync_id=?
		AND status IN ('PENDING','SENDING','FAILED','CONFLICT')`, syncID).Scan(&n)
	return n
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func strPtr(s string) *string { return &s }

// localEntityExists 处置前置校验：keep-remote 只能覆盖"本机还在"的行。
func localEntityExists(db *sql.DB, entityType, syncID string) bool {
	table := "notes"
	if entityType == syncspec.EntityTypeFolder {
		table = "note_folders"
	}
	if syncID == "" || (entityType != syncspec.EntityTypeNote && entityType != syncspec.EntityTypeFolder) {
		return false
	}
	var one int
	err := db.QueryRow(`SELECT 1 FROM `+table+` WHERE sync_id = ? LIMIT 1`, syncID).Scan(&one)
	return err == nil
}

// sqlExecer 让冲突补记在 *sql.DB 与 *sql.Tx 两种执行面上共用同一实现（禁双源）。
type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// recordConflictFromOp 为"已被判 CONFLICT 但当初没记冲突资产"的 op 补记一条（幂等去重）。
// 刀1 闭合两个可见面黑洞：重试耗尽（reconcileOutboxRetries）与附件内容分歧（ensureRefUploads）。
func recordConflictFromOp(db sqlExecer, seq int64, kind string) error {
	var (
		opID, etype, syncID, oper, payload string
		base                               sql.NullInt64
	)
	err := db.QueryRow(`SELECT op_id, entity_type, entity_sync_id, operation, payload, base_version
		FROM sync_outbox WHERE seq = ?`, seq).Scan(&opID, &etype, &syncID, &oper, &payload, &base)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts WHERE op_id = ? AND kind = ?`, opID, kind).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil // 同一笔 op 的同类冲突只记一次（重复轮次不得刷屏）
	}
	_, err = db.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, op_id, kind,
		local_payload, remote_payload, base_version, remote_version)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, 0)`, syncID, etype, oper, opID, kind, payload, base.Int64)
	return err
}
