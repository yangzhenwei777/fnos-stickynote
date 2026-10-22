package backup

import "errors"

// 稳定错误码（API 层据此回结构化 {code,message}；前缀 BACKUP_/WEBDAV_/RESTORE_）。
// 命名遵循 v4.3-final 审核：用 BACKUP_* 而非 SYNC_*（不把"同步"写进稳定 API）。
var (
	ErrDatabaseCorrupted  = errors.New("BACKUP_DATABASE_CORRUPTED")
	ErrAttachmentMissing  = errors.New("BACKUP_ATTACHMENT_MISSING")
	ErrBackupTooLarge     = errors.New("BACKUP_TOO_LARGE") // 归档超大小帽
	ErrArchiveCapExceeded = errors.New("BACKUP_TOO_LARGE") // 同义，seal 前置检查
	ErrDiskInsufficient   = errors.New("BACKUP_DISK_INSUFFICIENT")
	ErrDecryptFailed      = errors.New("BACKUP_DECRYPT_FAILED")  // GCM 认证失败（密钥错/损坏/AAD 篡改）
	ErrAlreadyRunning     = errors.New("BACKUP_ALREADY_RUNNING") // 四操作互斥
	ErrConfigInvalid      = errors.New("WEBDAV_CONFIG_INVALID")
	ErrWebDAVAuth         = errors.New("WEBDAV_AUTH_FAILED")
	ErrSnapshot           = errors.New("BACKUP_SNAPSHOT_FAILED")

	ErrRestoreIdentityMismatch = errors.New("RESTORE_IDENTITY_MISMATCH")
	ErrRefuseFullRestore       = errors.New("REFUSE_FULL_RESTORE") // JWT_SECRET env 存在等
)
