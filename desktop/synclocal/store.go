// Package synclocal —— 桌面同步执行器（离线方案 v20-Project P0-D，§45~§54 + ADR-001/002）。
//
// 组成：sync_state（cursor/epoch 锚）、sync_outbox（本地未同步操作，§45）、
// sync_conflicts（ADR-001 冲突恢复资产）。Worker.RunOnce = state 校验 → pull 应用
// （脏保护 + cursor 同事务，§34/ADR-001）→ push（批构造 ADR-002 + ACK 后 rebase §50）。
//
// 复用服务端 sync 包的 payload/op 类型（同模块 replace，零复制 §121）。
package synclocal

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"stickynote/syncspec"
)

// clientTables 桌面本地同步元表（notes 等业务表由服务端同款迁移在 production 建立；
// 集成测试自建最小 notes DDL）。§98：本地 users 行仅满足 FK，非认证权威。
const clientTables = `
CREATE TABLE IF NOT EXISTS sync_state (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sync_outbox (
	seq           INTEGER PRIMARY KEY AUTOINCREMENT,
	op_id         TEXT NOT NULL UNIQUE,
	entity_type   TEXT NOT NULL,
	entity_sync_id TEXT NOT NULL,
	operation     TEXT NOT NULL,
	base_version  INTEGER,
	payload       TEXT NOT NULL,
	status        TEXT NOT NULL DEFAULT 'PENDING', -- PENDING|SENDING|FAILED|CONFLICT|ACKED
	retry_count   INTEGER NOT NULL DEFAULT 0,
	last_error    TEXT NOT NULL DEFAULT '',
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_outbox_entity ON sync_outbox(entity_type, entity_sync_id, status);
CREATE TABLE IF NOT EXISTS sync_conflicts (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	entity_sync_id TEXT NOT NULL,
	kind           TEXT NOT NULL, -- CONFLICT_ON_PULL | PUSH_CONFLICT | ENTITY_GONE | OP_ID_REUSE | STALE
	local_payload  TEXT NOT NULL DEFAULT '',
	remote_payload TEXT NOT NULL DEFAULT '',
	base_version   INTEGER NOT NULL DEFAULT 0,
	remote_version INTEGER NOT NULL DEFAULT 0,
	created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	entity_type    TEXT NOT NULL DEFAULT '', -- NOTE|FOLDER|FOLDER_ORDER（批次8-Alt 刀1 补；旧行为空 = 不可自动处置）
	operation      TEXT NOT NULL DEFAULT '', -- 远端那侧的操作类型；applyNote/applyFolderChange 按它分支，缺它不能猜
	op_id          TEXT NOT NULL DEFAULT '', -- 涉事 outbox op（重试耗尽/附件分歧类冲突补记时用于幂等去重）
	status         TEXT NOT NULL DEFAULT 'open', -- open|resolved
	resolution     TEXT NOT NULL DEFAULT '',     -- keep-local|keep-remote
	resolved_at    TEXT NOT NULL DEFAULT ''
);`

// ensureConflictColumns 给旧库补 sync_conflicts 的处置列（幂等，PRAGMA 探测后再 ALTER）。
// 批次8-Alt 刀1：冲突处置面需要实体类型/操作类型/处置态三组信息；1.0.178 及更早的档案库
// 只有前 7 列，故启动建表入口（EnsureTables）里做有界列补齐——SQLite 的 ADD COLUMN 带
// NOT NULL DEFAULT 是常数默认，不重写既有行，安全。
func ensureConflictColumns(db *sql.DB) error {
	want := []struct{ name, ddl string }{
		{"entity_type", "ALTER TABLE sync_conflicts ADD COLUMN entity_type TEXT NOT NULL DEFAULT ''"},
		{"operation", "ALTER TABLE sync_conflicts ADD COLUMN operation TEXT NOT NULL DEFAULT ''"},
		{"op_id", "ALTER TABLE sync_conflicts ADD COLUMN op_id TEXT NOT NULL DEFAULT ''"},
		{"status", "ALTER TABLE sync_conflicts ADD COLUMN status TEXT NOT NULL DEFAULT 'open'"},
		{"resolution", "ALTER TABLE sync_conflicts ADD COLUMN resolution TEXT NOT NULL DEFAULT ''"},
		{"resolved_at", "ALTER TABLE sync_conflicts ADD COLUMN resolved_at TEXT NOT NULL DEFAULT ''"},
	}
	have, err := conflictColumns(db)
	if err != nil {
		return err
	}
	for _, c := range want {
		if _, ok := have[c.name]; ok {
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil {
			return fmt.Errorf("sync_conflicts 补列 %s 失败: %w", c.name, err)
		}
	}
	return nil
}

func conflictColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(sync_conflicts)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// EnsureTables 建同步元表（幂等）。附件账本/下载队列表随 P1 批次3 并入同一入口；
// 冲突处置列随批次8-Alt 刀1 在同一入口补齐（断网冷启动建表纪律同批次4：不能等首次冲突才建）。
func EnsureTables(db *sql.DB) error {
	if err := execMulti(db, clientTables); err != nil {
		return err
	}
	if err := ensureConflictColumns(db); err != nil {
		return err
	}
	return execMulti(db, attachmentTables)
}

func execMulti(db *sql.DB, s string) error {
	_, err := db.Exec(s)
	return err
}

func getState(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// StateValue 读锚点的导出面（v21.201 A2：接管后锚点自检需要跨包比对
// epoch / instance / cursor 三个键，禁在 main 包另写一份 SQL）。
func StateValue(db *sql.DB, key string) (string, error) { return getState(db, key) }

func setState(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO sync_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func randID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ---- 载荷构造（本地权威行 → canonical payload，字段与 sync.NotePayload 对齐） ----

// BuildNotePayload 从本地 notes 行读业务状态（含软删行；RESTORE/DELETE 需要墓碑态）。
func BuildNotePayload(db *sql.DB, noteID int64) (string, error) {
	var (
		p struct {
			SyncID       string     `json:"sync_id"`
			Title        string     `json:"title"`
			Content      string     `json:"content"`
			Tags         string     `json:"tags"`
			IsEncrypted  bool       `json:"is_encrypted"`
			Hash         string     `json:"encrypt_password_hash"`
			Encrypted    string     `json:"encrypted_content"`
			CreatedAt    time.Time  `json:"created_at"`
			UpdatedAt    time.Time  `json:"updated_at"`
			DeletedAt    *time.Time `json:"deleted_at"`
			Version      int64      `json:"version"`
			FolderSyncID *string    `json:"folder_sync_id"`
			Refs         []refRow   `json:"-"`
		}
		syncID, folderSync sql.NullString
		deleted            sql.NullTime
	)
	err := db.QueryRow(`SELECT n.sync_id, n.title, n.content, n.tags, n.is_encrypted, n.encrypt_password_hash,
			n.encrypted_content, n.created_at, n.updated_at, n.deleted_at, n.version, f.sync_id
		FROM notes n LEFT JOIN note_folders f ON f.id = n.folder_id AND f.user_id = n.user_id
		WHERE n.id = ?`, noteID).
		Scan(&syncID, &p.Title, &p.Content, &p.Tags, &p.IsEncrypted, &p.Hash, &p.Encrypted,
			&p.CreatedAt, &p.UpdatedAt, &deleted, &p.Version, &folderSync)
	if err != nil {
		return "", fmt.Errorf("build local payload note %d: %w", noteID, err)
	}
	if !syncID.Valid || syncID.String == "" {
		return "", fmt.Errorf("note %d missing sync_id (client schema not migrated?)", noteID)
	}
	p.SyncID = syncID.String
	if deleted.Valid {
		d := deleted.Time
		p.DeletedAt = &d
	}
	if folderSync.Valid {
		f := folderSync.String
		p.FolderSyncID = &f
	}
	rows, err := db.Query(`SELECT u.filename, COALESCE(up.size, 0) FROM note_uploads u
		LEFT JOIN uploads up ON up.filename = u.filename WHERE u.note_id = ?`, noteID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var r refRow
			if rows.Scan(&r.Filename, &r.Size) == nil {
				p.Refs = append(p.Refs, r)
			}
		}
	}
	type refOut struct {
		Filename string `json:"filename"`
		Sha256   string `json:"sha256"`
		Size     int64  `json:"size"`
	}
	out := struct {
		SyncID              string     `json:"sync_id"`
		Title               string     `json:"title"`
		Content             string     `json:"content"`
		Tags                string     `json:"tags"`
		IsEncrypted         bool       `json:"is_encrypted"`
		EncryptPasswordHash string     `json:"encrypt_password_hash"`
		EncryptedContent    string     `json:"encrypted_content"`
		CreatedAt           time.Time  `json:"created_at"`
		UpdatedAt           time.Time  `json:"updated_at"`
		DeletedAt           *time.Time `json:"deleted_at"`
		Version             int64      `json:"version"`
		FolderSyncID        *string    `json:"folder_sync_id"`
		AttachmentRefs      []refOut   `json:"attachment_refs"`
	}{p.SyncID, p.Title, p.Content, p.Tags, p.IsEncrypted, p.Hash, p.Encrypted,
		p.CreatedAt, p.UpdatedAt, p.DeletedAt, p.Version, p.FolderSyncID, []refOut{}}
	for _, r := range p.Refs {
		out.AttachmentRefs = append(out.AttachmentRefs, refOut{Filename: r.Filename, Size: r.Size})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

type refRow struct {
	Filename string
	Size     int64
}

// EnqueueNoteOp 本地变异后入队（§25 Dirty 规则：与业务写同一调用栈内完成入队；
// 严格同事务在 P2 Facade 收口时并入，P0-D 以"写库成功后立即入队 + 失败上抛"过渡）。
// 同实体连续 PENDING 由批构造合并（§49），此处不判重。
func EnqueueNoteOp(db *sql.DB, noteID int64, operation string) error {
	payload, err := BuildNotePayload(db, noteID)
	if err != nil {
		return err
	}
	var syncID string
	var version int64
	if err := db.QueryRow(`SELECT sync_id, version FROM notes WHERE id = ?`, noteID).Scan(&syncID, &version); err != nil {
		return err
	}
	base := version // 本地 version 恒为"最后已知服务端版本"（P0-1：本地编辑不自增）
	_, err = db.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload)
		VALUES (?, 'NOTE', ?, ?, ?, ?)`, randID(), syncID, operation, base, payload)
	return err
}

// IsDirty §20：是否有未同步操作。
func IsDirty(db *sql.DB, entitySyncID string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sync_outbox
		WHERE entity_sync_id = ? AND status IN ('PENDING','SENDING','FAILED')`, entitySyncID).Scan(&n)
	return n > 0, err
}

// ---- 文件夹入队原语（P1 批次2：FOLDER 逐实体 op + FOLDER_ORDER 集合级 op，§18/§55）----

// FolderRef 删除前捕获的行引用（DELETE 入队时行已不存在）。
type FolderRef struct {
	SyncID      string
	Name        string
	Version     int64
	IsEncrypted bool
	Hash        string
}

// GetFolderRef 读本地文件夹引用（无 sync_id 视为未初始化，ok=false）。
func GetFolderRef(db *sql.DB, folderID int64) (FolderRef, bool) {
	var r FolderRef
	var syncID sql.NullString
	var encrypted int
	err := db.QueryRow(`SELECT sync_id, name, version, is_encrypted, encrypt_password_hash FROM note_folders WHERE id = ?`, folderID).
		Scan(&syncID, &r.Name, &r.Version, &encrypted, &r.Hash)
	if err != nil || !syncID.Valid || syncID.String == "" {
		return r, false
	}
	r.SyncID = syncID.String
	r.IsEncrypted = encrypted != 0
	return r, true
}

// EnqueueFolderOp CREATE/UPDATE 入队：base = 本地 version（最后已知服务端版本，§25 P0-1）。
func EnqueueFolderOp(db *sql.DB, folderID int64, operation string) error {
	r, ok := GetFolderRef(db, folderID)
	if !ok {
		return fmt.Errorf("folder %d missing sync_id (client schema not migrated?)", folderID)
	}
	enc := r.IsEncrypted
	hash := r.Hash
	payload, _ := json.Marshal(syncspec.FolderPayload{
		SyncID: r.SyncID, Name: r.Name, Version: r.Version,
		IsEncrypted: &enc, EncryptPasswordHash: &hash,
	})
	_, err := db.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload)
		VALUES (?, 'FOLDER', ?, ?, ?, ?)`, randID(), r.SyncID, operation, r.Version, string(payload))
	return err
}

// EnqueueFolderDeleteOp DELETE 入队（调用方在物理删前用 GetFolderRef 捕获引用）。
func EnqueueFolderDeleteOp(db *sql.DB, r FolderRef) error {
	payload, _ := json.Marshal(syncspec.FolderPayload{SyncID: r.SyncID, Name: r.Name, Version: r.Version})
	_, err := db.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload)
		VALUES (?, 'FOLDER', ?, 'DELETE', ?, ?)`, randID(), r.SyncID, r.Version, string(payload))
	return err
}

// EnqueueFolderOrderOp 本地重排后入队集合级 op：全序数组 + 逐 folder base（§55）。
// entity_sync_id 为空串（服务端按单例键判重，ADR-002 R4）。
func EnqueueFolderOrderOp(db *sql.DB) error {
	rows, err := db.Query(`SELECT sync_id, version FROM note_folders WHERE sync_id IS NOT NULL AND sync_id != '' ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var order []string
	bases := map[string]int64{}
	for rows.Next() {
		var sid string
		var v int64
		if err := rows.Scan(&sid, &v); err != nil {
			return err
		}
		order = append(order, sid)
		bases[sid] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}
	payload, _ := json.Marshal(syncspec.FolderOrderPayload{Order: order, Bases: bases})
	_, err = db.Exec(`INSERT INTO sync_outbox (op_id, entity_type, entity_sync_id, operation, base_version, payload)
		VALUES (?, 'FOLDER_ORDER', '', 'UPDATE', NULL, ?)`, randID(), string(payload))
	return err
}

// addConflict 记一条冲突。entityType/operation 是处置面的前提（applyNoteChange/applyFolderChange
// 按 operation 分支，缺它就只能拒绝自动处置），批次8-Alt 刀1 起必传。
func addConflict(db *sql.Tx, entitySyncID, entityType, operation, kind, localP, remoteP string, base, remoteVer int64) error {
	_, err := db.Exec(`INSERT INTO sync_conflicts (entity_sync_id, entity_type, operation, kind,
			local_payload, remote_payload, base_version, remote_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, entitySyncID, entityType, operation, kind, localP, remoteP, base, remoteVer)
	return err
}

// Status 数据中心可见摘要（§82/§117 子集）。
func Status(db *sql.DB) (map[string]any, error) {
	out := map[string]any{}
	for _, k := range []string{"cursor", "server_epoch", "server_instance_id", "last_sync_at"} {
		v, _ := getState(db, k)
		out[k] = v
	}
	var pending, open, total int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE status IN ('PENDING','SENDING','FAILED')`).Scan(&pending)
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts WHERE status='open'`).Scan(&open)
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts`).Scan(&total)
	out["pending_ops"] = pending
	// conflict_count 语义（刀1 起）= **待处置**条数；历史总量另给 conflict_total，
	// 避免已处置冲突继续在前端亮红。
	out["conflict_count"] = open
	out["conflict_total"] = total
	return out, nil
}

// RandID 对外导出的稳定随机短 ID（设备 ID / op_id 生成用）。
func RandID() string { return randID() }
