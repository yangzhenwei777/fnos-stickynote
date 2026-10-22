// Package syncspec —— 同步协议线上契约（类型 + 稳定错误码），零依赖（不引 gin/DB），
// 供服务端 sync 处理器与桌面 synclocal 执行器共用同一份 wire 形状（§155 协议面）。
// 破坏性变更须 SYNC_PROTOCOL_VERSION++（§156），不是 App 版本。
package syncspec

import (
	"encoding/json"
	"time"
)

const ProtocolVersion = 1

// 跨端稳定错误码（响应 JSON code 字段；文案可改码不改）。
const (
	CodeAcked            = "ACKED"
	CodeConflict         = "CONFLICT"
	CodeEntityGone       = "ENTITY_GONE"
	CodeIdempotentReplay = "IDEMPOTENT_REPLAY"
	CodeOpIDReuseError   = "OP_ID_REUSE_ERROR"
	CodeIdentityConflict = "ENTITY_IDENTITY_CONFLICT"
	CodeBatchProtocol    = "BATCH_PROTOCOL_ERROR"
	CodeUnsupported      = "UNSUPPORTED_ENTITY"
	// P1 批次3（ADR-003 R2/R5 + ADR-004 R3）：附件通道错误码。
	CodeUploadHashMismatch     = "UPLOAD_HASH_MISMATCH"     // 申报 != 服务端实算（拒收，删临时件）
	CodeUploadIdentityConflict = "UPLOAD_IDENTITY_CONFLICT" // 同名异内容/异尺寸/归属他人（既有件不动）
	CodeAttachmentNotFound     = "ATTACHMENT_NOT_FOUND"     // push refs 缺失（不登记 op，重传后可原样重试）
)

const MaxOpsPerBatch = 50

// 同步操作码（PushOp.Operation 与 ChangeLog operation 共用，§46）。
// E7（2026-09-23 用户产品决策）：新增 PURGE——推翻"永久删不发事件"的原设计
// （不发事件 → 客户端永远留僵尸行、恢复必撞 ENTITY_GONE，真机六轮实锤）。
const (
	OperationCreate  = "CREATE"
	OperationUpdate  = "UPDATE"
	OperationDelete  = "DELETE"  // 软删：进回收站（客户端标 deleted_at）
	OperationRestore = "RESTORE" // 从回收站恢复
	OperationPurge   = "PURGE"   // 永久删（彻底删除/清空回收站）：客户端物理清理本地行
)

// 实体类型（§9；FOLDER/FOLDER_ORDER 为 P1 批次2 接入）。
// 协议兼容：新实体类型对旧客户端走"仅推进 cursor"安全路径（worker applyChange 既有分支），
// 非破坏性扩展，ProtocolVersion 保持 1（§156）。
const (
	EntityTypeNote        = "NOTE"
	EntityTypeFolder      = "FOLDER"
	EntityTypeFolderOrder = "FOLDER_ORDER" // 集合级 op：entity_sync_id=NULL 视作单例键（ADR-002 R4）
)

// ---- push ----

type PushOp struct {
	OpID         string          `json:"op_id"`
	EntityType   string          `json:"entity_type"` // NOTE | FOLDER | FOLDER_ORDER
	EntitySyncID string          `json:"entity_sync_id"`
	Operation    string          `json:"operation"`    // CREATE|UPDATE|DELETE|RESTORE（FOLDER_ORDER 固定 UPDATE）
	BaseVersion  *int64          `json:"base_version"` // FOLDER_ORDER 不用（逐 folder base 在 payload 内，§55）
	Payload      json.RawMessage `json:"payload"`
}

type PushRequest struct {
	DeviceID string   `json:"device_id"`
	Ops      []PushOp `json:"ops"`
}

type OpResult struct {
	OpID          string `json:"op_id"`
	Code          string `json:"code"`
	ServerVersion int64  `json:"server_version,omitempty"`
	RemoteVersion int64  `json:"remote_version,omitempty"`
	ChangeSeq     int64  `json:"change_seq,omitempty"`
	Error         string `json:"error,omitempty"`
	// OrderVersions 仅 FOLDER_ORDER ACK 携带（批次2）：逐 folder 生效后的权威版本，
	// 供客户端把队列中后续 folder op 的 base_version 重定基（§50 同构，避免串行冲突风暴）。
	// 增量字段（omitempty），旧客户端忽略不受影响，ProtocolVersion 不变。
	OrderVersions []FolderVersionRef `json:"order_versions,omitempty"`
}

// ---- pull / state ----

type ChangeDTO struct {
	ChangeSeq     int64           `json:"change_seq"`
	EntityType    string          `json:"entity_type"`
	EntitySyncID  *string         `json:"entity_sync_id"`
	Operation     string          `json:"operation"`
	ServerVersion int64           `json:"server_version"`
	PayloadKind   string          `json:"payload_kind"` // json | blob_ref（§32 P1-b）
	Payload       json.RawMessage `json:"payload"`
	// blob_ref 通道增量字段（§32，omitempty 向后兼容：旧客户端忽略，ProtocolVersion 不变）。
	// PayloadKind='blob_ref' 时 Payload 为 null，内容经 GET /api/sync/payload/:sha256
	// 下载（§34/§35 授权）。
	PayloadRef  string `json:"payload_ref,omitempty"`
	PayloadSize int64  `json:"payload_size,omitempty"`
	DeviceID    string `json:"device_id"`
	CreatedAt   string `json:"created_at"`
}

// ---- canonical note payload（§28/§29）----

type AttachmentRef struct {
	Filename string `json:"filename"`
	Sha256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

type NotePayload struct {
	SyncID              string          `json:"sync_id"`
	Title               string          `json:"title"`
	Content             string          `json:"content"`
	Tags                string          `json:"tags"`
	IsEncrypted         bool            `json:"is_encrypted"`
	EncryptPasswordHash string          `json:"encrypt_password_hash"`
	EncryptedContent    string          `json:"encrypted_content"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	DeletedAt           *time.Time      `json:"deleted_at"`
	Version             int64           `json:"version"`
	FolderSyncID        *string         `json:"folder_sync_id"`
	AttachmentRefs      []AttachmentRef `json:"attachment_refs"`
}

// ---- canonical folder payload（批次2 / §18）----

type FolderPayload struct {
	SyncID    string    `json:"sync_id"`
	Name      string    `json:"name"`
	SortOrder int       `json:"sort_order"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

// FolderVersionRef FOLDER_ORDER 应用后的逐 folder 权威版本（桌面侧回写 + 后续 op 重定基，§50 同构）。
type FolderVersionRef struct {
	SyncID  string `json:"sync_id"`
	Version int64  `json:"version"`
}

// FolderOrderPayload 集合级重排（§9/§55）：
//   - Order：全序数组 = 服务端当前全部自定义文件夹 sync_id 的完整排列（缺失/多余即协议错）；
//   - Bases：逐 folder base_version CAS 基点（任一不符 → 整 op 409 且零提交，§142）；
//   - Versions：仅 pull 侧携带（本 op 生效后权威版本）；push 侧服务端自行计算，客户端不回传。
type FolderOrderPayload struct {
	Order    []string           `json:"order"`
	Bases    map[string]int64   `json:"bases,omitempty"`
	Versions []FolderVersionRef `json:"versions,omitempty"`
}
