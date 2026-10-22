// conflicts_detect.go —— 冲突/待推的**只读面**（v21.166 计划项3 · 自 conflicts.go 纯拆分）。
//
// 本文件自 conflicts.go 按职责切分而来，**零逻辑改动**。
// 切分依据：原文件同时承载「只读呈现（ListConflicts/ListOutbox）」与「处置执行
// （ResolveConflict 及链重排/终止/版本对齐）」，前者供冲突中心呈现、后者改本地库，
// 风险等级不同 —— 拆分后「读」与「写」分文件，评审与回归时边界一目了然。
package synclocal

import (
	"database/sql"
	"errors"
)

// 处置方式常量（前端与 portable 同用字面量，勿改拼写）。
const (
	ResolveKeepLocal  = "keep-local"
	ResolveKeepRemote = "keep-remote"
	ResolveDropLocal  = "drop-local"
	// ResolveRecreate 1.0.218（217 内测：离线新建链卡账出路）：「以本机内容重建到服务器」——
	// 仅对 ENTITY_GONE ×（NOTE/FOLDER）× 本机行仍在（未在回收站）开放。语义：服务端本无此实体，
	// 终止旧链后以本机**当前行内容**新排一笔 CREATE（换新 op_id）重推；服务端 opCreate 幂等兜底
	// （不存在→INSERT；已存在同属主→IDEMPOTENT_REPLAY）。≠ 原 op 重投（对不存在实体重投 UPDATE
	// 必再撞 GONE）；≠ 另存副本（不换身份、不产生重复笔记）。
	ResolveRecreate = "recreate"
)

// 冲突 kind（worker 各处产生；RETRY_EXHAUSTED / ATTACHMENT_DIVERGED 为刀1 补记的可见面）。
const (
	KindConflictOnPull     = "CONFLICT_ON_PULL"
	KindPushConflict       = "PUSH_CONFLICT"
	KindEntityGone         = "ENTITY_GONE"
	KindStale              = "STALE"
	KindRetryExhausted     = "RETRY_EXHAUSTED"
	KindAttachmentDiverged = "ATTACHMENT_DIVERGED"
)

var (
	ErrConflictNotFound        = errors.New("冲突记录不存在")
	ErrConflictAlreadyResolved = errors.New("该冲突已处置，无需重复操作")
	ErrResolveRequiresConfirm  = errors.New("覆盖本机未同步的修改需要明确确认")
	ErrResolveUnsupported      = errors.New("该冲突不支持此处置方式")
	ErrResolveUnknown          = errors.New("未知的处置方式")
	ErrConflictEntityMissing   = errors.New("本机已无该实体，无法用远端内容覆盖；请改选放弃这笔")
	// 1.0.218：recreate 的 fail-closed 拒绝因由（比笼统"不支持"多给一条可行动的出路指引）。
	ErrRecreateEntityMissing = errors.New("本机已无这条实体，无法重建到服务器；请改选「放弃本机这笔」")
	ErrRecreateInTrash       = errors.New("这条笔记在本机回收站中，无法重建；请先还原它或改选「放弃本机这笔」")
)

// Conflict 一条冲突资产（sync_conflicts 行的可序列化形状）。payload 原样回传（JSON 串），
// 由前端解析后并排呈现本机/远端内容——Go 侧不做内容级 diff，避免猜语义。
type Conflict struct {
	ID            int64  `json:"id"`
	EntitySyncID  string `json:"entity_sync_id"`
	EntityType    string `json:"entity_type"`
	Operation     string `json:"operation"`
	OpID          string `json:"op_id"`
	Kind          string `json:"kind"`
	LocalPayload  string `json:"local_payload"`
	RemotePayload string `json:"remote_payload"`
	BaseVersion   int64  `json:"base_version"`
	RemoteVersion int64  `json:"remote_version"`
	CreatedAt     string `json:"created_at"`
	Status        string `json:"status"`
	Resolution    string `json:"resolution"`
	ResolvedAt    string `json:"resolved_at"`
	// OpenOps 该实体仍挂在本机的 op 数（PENDING/SENDING/FAILED/CONFLICT 四态合计）。
	OpenOps int `json:"open_ops"`
	// AllowedResolutions 供 UI 只渲染合法按钮，避免把"点了必报错"的选项摊给用户。
	AllowedResolutions []string `json:"allowed_resolutions"`
}

// OutboxOp 本机待推/历史 op 的一行（断网期间产生的挂账可见化）。base_version 可为 NULL
// （规范 CREATE 与 FOLDER_ORDER 不带 base，§48）→ 必须走 NullInt64，批次6 的 NULL-base 事故同源。
type OutboxOp struct {
	Seq          int64  `json:"seq"`
	OpID         string `json:"op_id"`
	EntityType   string `json:"entity_type"`
	EntitySyncID string `json:"entity_sync_id"`
	Operation    string `json:"operation"`
	BaseVersion  *int64 `json:"base_version"`
	Status       string `json:"status"`
	RetryCount   int    `json:"retry_count"`
	LastError    string `json:"last_error"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	PayloadBytes int    `json:"payload_bytes"`
}

// ListConflicts 分页读冲突；onlyOpen=true 时只读未处置项。返回 (行, 同过滤条件下的总数)。
func ListConflicts(db *sql.DB, onlyOpen bool, limit, offset int) ([]Conflict, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200 // 与 buildBatch 的取样上限同量级，避免一次性把大 payload 拖过 IPC
	}
	if offset < 0 {
		offset = 0
	}
	where := ""
	if onlyOpen {
		where = ` WHERE status='open'`
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts` + where).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(`SELECT id, entity_sync_id, entity_type, operation, op_id, kind,
		local_payload, remote_payload, base_version, remote_version, created_at,
		status, resolution, resolved_at
		FROM sync_conflicts`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Conflict, 0, limit)
	for rows.Next() {
		var c Conflict
		if err := rows.Scan(&c.ID, &c.EntitySyncID, &c.EntityType, &c.Operation, &c.OpID, &c.Kind,
			&c.LocalPayload, &c.RemotePayload, &c.BaseVersion, &c.RemoteVersion,
			&c.CreatedAt, &c.Status, &c.Resolution, &c.ResolvedAt); err != nil {
			return nil, 0, err
		}
		// 旧库遗留行可能没有 entity_type 列值（空串）：按 sync_id 反查本地表补型，补不到留空
		// ——处置面仍会 fail-closed 拒绝，只是 UI 能显示"这条是笔记还是文件夹"。
		if c.EntityType == "" {
			c.EntityType = InferEntityType(db, c.EntitySyncID)
		}
		c.OpenOps = countOpenOps(db, c.EntityType, c.EntitySyncID)
		c.AllowedResolutions = allowedResolutions(db, c.Kind, c.EntityType, c.EntitySyncID, c.Operation, c.RemotePayload)
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// ListOutbox 分页读 outbox；statuses 为空表示不过滤。返回 (行, 按状态计数, 总数)。
func ListOutbox(db *sql.DB, statuses []string, limit, offset int) ([]OutboxOp, map[string]int, int, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	var args []any
	where := ""
	if len(statuses) > 0 {
		where = " WHERE status IN ("
		for i, s := range statuses {
			if i > 0 {
				where += ","
			}
			where += "?"
			args = append(args, s)
		}
		where += ")"
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_outbox`+where, args...).Scan(&total); err != nil {
		return nil, nil, 0, err
	}
	counts := map[string]int{}
	crows, err := db.Query(`SELECT status, COUNT(*) FROM sync_outbox GROUP BY status`)
	if err != nil {
		return nil, nil, 0, err
	}
	for crows.Next() {
		var st string
		var n int
		if err := crows.Scan(&st, &n); err == nil {
			counts[st] = n
		}
	}
	crows.Close()

	rows, err := db.Query(`SELECT seq, op_id, entity_type, entity_sync_id, operation, base_version,
		status, retry_count, last_error, created_at, updated_at, length(payload)
		FROM sync_outbox`+where+` ORDER BY seq DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	out := make([]OutboxOp, 0, limit)
	for rows.Next() {
		var (
			o     OutboxOp
			base  sql.NullInt64
			creat string
			upd   string
		)
		if err := rows.Scan(&o.Seq, &o.OpID, &o.EntityType, &o.EntitySyncID, &o.Operation, &base,
			&o.Status, &o.RetryCount, &o.LastError, &creat, &upd, &o.PayloadBytes); err != nil {
			return nil, nil, 0, err
		}
		if base.Valid {
			v := base.Int64
			o.BaseVersion = &v
		}
		o.CreatedAt, o.UpdatedAt = creat, upd
		out = append(out, o)
	}
	return out, counts, total, rows.Err()
}

// ResolveResult 处置结果（供 UI 回执与日志）。
