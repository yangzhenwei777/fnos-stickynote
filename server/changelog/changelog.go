// Package changelog —— ChangeLog 事件写入共享设施（P2 批次1 下沉，v20 §23/§24/§30）。
//
// P0-D 时 nextChangeSeq/appendChange/buildNotePayload 私有在 sync 包内（只有 push 一条
// 生产者）；P2 起 Web 写路径（app Facade）成为第二生产者，为防双源漂移（两处各写各的
// payload/seq 逻辑），设施下沉为本包——零 gin/DB 依赖（仅 database/sql + syncspec），
// sync 与 app 各自 import，事务由调用方持有（§16 Model Tx 规范：实体更新、seq 递增、
// 事件写入同事务提交，§24/§42）。
package changelog

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"stickynote/syncspec"
)

// NewSyncID 生成实体跨设备身份（§17）。与迁移回填口径一致（32 位小写 hex，
// upgrade_sync.go 同格式），插入时由写路径生成，不依赖重启回填兜底。
func NewSyncID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败极罕见（系统熵源不可用）——fail-closed 上抛，调用方放弃本次写入
		panic(fmt.Sprintf("changelog.NewSyncID: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// EnsureNoteSyncID 事务内为存量 NULL sync_id 行补齐身份并返回（幂等）。
// 防御场景：v1.0.175 升级后、批次1 部署前经 Web 创建的行（INSERT 未带 sync_id，
// 靠重启回填）在首个变更事件前必须先有身份，否则 ChangeLog 拿不到 entity_sync_id。
func EnsureNoteSyncID(tx *sql.Tx, noteID int64) (string, error) {
	var syncID sql.NullString
	if err := tx.QueryRow(`SELECT sync_id FROM notes WHERE id = ?`, noteID).Scan(&syncID); err != nil {
		return "", fmt.Errorf("ensure sync_id read note %d: %w", noteID, err)
	}
	if syncID.Valid && syncID.String != "" {
		return syncID.String, nil
	}
	id := NewSyncID()
	if _, err := tx.Exec(`UPDATE notes SET sync_id = ? WHERE id = ? AND (sync_id IS NULL OR sync_id = '')`, id, noteID); err != nil {
		return "", fmt.Errorf("ensure sync_id write note %d: %w", noteID, err)
	}
	return id, nil
}

// NextChangeSeq 在调用方事务内原子递增并返回全局 change_seq（§24）。
// BEGIN IMMEDIATE（DSN _txlock）保证同事务内 UPDATE→SELECT 无竞态；
// 禁止以 MAX(change_seq) 作权威序列（§24）。
func NextChangeSeq(tx *sql.Tx) (int64, error) {
	if _, err := tx.Exec(`UPDATE sync_meta SET current_change_seq = current_change_seq + 1, updated_at = CURRENT_TIMESTAMP WHERE id = 1`); err != nil {
		return 0, fmt.Errorf("bump change_seq: %w", err)
	}
	var seq int64
	if err := tx.QueryRow(`SELECT current_change_seq FROM sync_meta WHERE id = 1`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("read change_seq: %w", err)
	}
	return seq, nil
}

// AppendChange 在调用方事务内写不可变事件（§23/§30：与实体更新、seq 递增同事务）。
func AppendChange(tx *sql.Tx, seq, userID int64, entityType string, entitySyncID *string, operation string, serverVersion int, payloadJSON, deviceID string) error {
	_, err := tx.Exec(`INSERT INTO sync_changes
		(change_seq, user_id, entity_type, entity_sync_id, operation, server_version, payload_kind, payload_json, device_id)
		VALUES (?, ?, ?, ?, ?, ?, 'json', ?, ?)`,
		seq, userID, entityType, entitySyncID, operation, serverVersion, payloadJSON, deviceID)
	if err != nil {
		return fmt.Errorf("append change %d: %w", seq, err)
	}
	return nil
}

// HashProvider 附件即时补算钩子（ADR-003 R4 保证位；cmd/server 启动时接线为
// model.FileSHA256 的**只读**计算）。注意：本包函数常在调用方写事务内执行，
// provider 绝不可再开独立连接写库（BEGIN IMMEDIATE 下必锁超时——批次3 实测）；
// 权威值落库由回填任务（BackfillUploadHashesOnce）事务外收敛。未接线时事件照常
// 产出但哈希留空由回填兜底。
var HashProvider func(filename string) (string, error)

// BuildNotePayload 从权威行构造事件载荷（§28/§29 服务端为准，不信任客户端回传形状）。
// 只含业务同步状态；content_text/note_tags/FTS/highlight/trash_upload_bytes 等
// 派生数据不进 payload，Pull 端本地事务重建（§30/§31）。
func BuildNotePayload(tx *sql.Tx, noteID int64) (syncspec.NotePayload, error) {
	var p syncspec.NotePayload
	var syncID, folderSync sql.NullString
	err := tx.QueryRow(`SELECT n.sync_id, n.title, n.content, n.tags, n.is_encrypted, n.encrypt_password_hash,
			n.encrypted_content, n.created_at, n.updated_at, n.deleted_at, n.version, f.sync_id
		FROM notes n LEFT JOIN note_folders f ON f.id = n.folder_id AND f.user_id = n.user_id
		WHERE n.id = ?`, noteID).
		Scan(&syncID, &p.Title, &p.Content, &p.Tags, &p.IsEncrypted, &p.EncryptPasswordHash,
			&p.EncryptedContent, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.Version, &folderSync)
	if err != nil {
		return p, fmt.Errorf("build payload note %d: %w", noteID, err)
	}
	if syncID.Valid {
		p.SyncID = syncID.String
	}
	if folderSync.Valid {
		v := folderSync.String
		p.FolderSyncID = &v
	}
	rows, err := tx.Query(`SELECT u.filename, COALESCE(up.size, 0), COALESCE(up.sha256, '')
		FROM note_uploads u LEFT JOIN uploads up ON up.filename = u.filename WHERE u.note_id = ?`, noteID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	type pending struct {
		idx  int
		name string
	}
	var needFill []pending
	for rows.Next() {
		var ref syncspec.AttachmentRef
		var sha string
		if err := rows.Scan(&ref.Filename, &ref.Size, &sha); err != nil {
			return p, err
		}
		ref.Sha256 = sha
		idx := len(p.AttachmentRefs)
		p.AttachmentRefs = append(p.AttachmentRefs, ref)
		if sha == "" {
			needFill = append(needFill, pending{idx, ref.Filename})
		}
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	// R4 保证位：桌面收到的任何 attachment_ref sha256 非空 —— '' 行即时**只读**补算
	// （文件在变更时刻必在，上传先于引用是 §65 通道顺序）。补算失败（文件缺失等）保持 ''
	// 并继续产事件，回填任务兜底；落库不在本路径发生（避免事务内开连接死锁）。
	if len(needFill) > 0 && HashProvider != nil {
		for i := range needFill {
			if sha, herr := HashProvider(needFill[i].name); herr == nil && sha != "" {
				p.AttachmentRefs[needFill[i].idx].Sha256 = sha
			}
		}
	}
	return p, nil
}

// MarshalPayload 序列化事件载荷（失败降级 "{}"——载荷不可序列化时事件仍必须落库，
// 空 payload 由 Pull 端按全量对账语义处理，优于丢事件）。
func MarshalPayload(p syncspec.NotePayload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ---- FOLDER / FOLDER_ORDER 事件设施（P1 批次2，§18/§55）----

// EnsureFolderSyncID 事务内为存量 NULL sync_id 文件夹行补齐身份并返回（幂等，同 notes 口径）。
func EnsureFolderSyncID(tx *sql.Tx, folderID int64) (string, error) {
	var syncID sql.NullString
	if err := tx.QueryRow(`SELECT sync_id FROM note_folders WHERE id = ?`, folderID).Scan(&syncID); err != nil {
		return "", fmt.Errorf("ensure sync_id read folder %d: %w", folderID, err)
	}
	if syncID.Valid && syncID.String != "" {
		return syncID.String, nil
	}
	id := NewSyncID()
	if _, err := tx.Exec(`UPDATE note_folders SET sync_id = ? WHERE id = ? AND (sync_id IS NULL OR sync_id = '')`, id, folderID); err != nil {
		return "", fmt.Errorf("ensure sync_id write folder %d: %w", folderID, err)
	}
	return id, nil
}

// BuildFolderPayload 从权威行构造文件夹事件载荷（服务端为准）。
func BuildFolderPayload(tx *sql.Tx, folderID int64) (syncspec.FolderPayload, error) {
	var p syncspec.FolderPayload
	var syncID sql.NullString
	var isEncrypted int
	var hash string
	err := tx.QueryRow(`SELECT sync_id, name, sort_order, version, created_at, is_encrypted, encrypt_password_hash FROM note_folders WHERE id = ?`, folderID).
		Scan(&syncID, &p.Name, &p.SortOrder, &p.Version, &p.CreatedAt, &isEncrypted, &hash)
	if err != nil {
		return p, fmt.Errorf("build payload folder %d: %w", folderID, err)
	}
	if syncID.Valid {
		p.SyncID = syncID.String
	}
	enc := isEncrypted != 0
	p.IsEncrypted = &enc
	p.EncryptPasswordHash = &hash
	return p, nil
}

// MarshalFolderPayload 序列化文件夹载荷（降级语义与 MarshalPayload 一致）。
func MarshalFolderPayload(p syncspec.FolderPayload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// AppendFolderChange 事务内产 FOLDER 实体事件（operation: CREATE|UPDATE|DELETE）。
func AppendFolderChange(tx *sql.Tx, userID int64, folderID int64, operation, deviceID string) (int64, error) {
	seq, err := NextChangeSeq(tx)
	if err != nil {
		return 0, err
	}
	p, err := BuildFolderPayload(tx, folderID)
	if err != nil {
		return 0, err
	}
	syncID := p.SyncID
	if err := AppendChange(tx, seq, userID, syncspec.EntityTypeFolder, &syncID, operation, int(p.Version), MarshalFolderPayload(p), deviceID); err != nil {
		return 0, err
	}
	return seq, nil
}

// AppendFolderOrderChange 事务内产 FOLDER_ORDER 集合级事件（§55：整批提交单一 seq；
// entity_sync_id=NULL 单例键）。order=生效后的全序数组，versions=逐 folder 权威版本。
func AppendFolderOrderChange(tx *sql.Tx, userID int64, order []string, versions []syncspec.FolderVersionRef, deviceID string) (int64, error) {
	seq, err := NextChangeSeq(tx)
	if err != nil {
		return 0, err
	}
	p := syncspec.FolderOrderPayload{Order: order, Versions: versions}
	b, err := json.Marshal(p)
	if err != nil {
		b = []byte("{}")
	}
	if err := AppendChange(tx, seq, userID, syncspec.EntityTypeFolderOrder, nil, "UPDATE", 0, string(b), deviceID); err != nil {
		return 0, err
	}
	return seq, nil
}

// BumpNoteVersionTx 服务端语义版本推进（实体写后由事件产出方调用；单一实现供 app/sync 共用）。
func BumpNoteVersionTx(tx *sql.Tx, noteID int64) (int64, error) {
	if _, err := tx.Exec("UPDATE notes SET version = version + 1 WHERE id = ?", noteID); err != nil {
		return 0, fmt.Errorf("bump note %d version: %w", noteID, err)
	}
	var v int64
	if err := tx.QueryRow("SELECT version FROM notes WHERE id = ?", noteID).Scan(&v); err != nil {
		return 0, fmt.Errorf("read note %d version: %w", noteID, err)
	}
	return v, nil
}

// BumpFolderVersionTx 同上（note_folders 表）。
func BumpFolderVersionTx(tx *sql.Tx, folderID int64) (int64, error) {
	if _, err := tx.Exec("UPDATE note_folders SET version = version + 1 WHERE id = ?", folderID); err != nil {
		return 0, fmt.Errorf("bump folder %d version: %w", folderID, err)
	}
	var v int64
	if err := tx.QueryRow("SELECT version FROM note_folders WHERE id = ?", folderID).Scan(&v); err != nil {
		return 0, fmt.Errorf("read folder %d version: %w", folderID, err)
	}
	return v, nil
}
