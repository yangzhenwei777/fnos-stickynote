package model

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"time"

	"stickynote/database"
)

// Upload 上传文件归属记录（v18.5）：删用户时按此表物理清理文件。
// user_id 外键 CASCADE——用户删除后记录自动清除，文件清理由 handler 按清单执行。
type Upload struct {
	ID        int64     `json:"id"`
	Filename  string    `json:"filename"`
	UserID    int64     `json:"user_id"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// RecordUpload 上传成功后落库一条归属记录（sha256=” 兼容旧调用——由回填/补算收敛）。
func RecordUpload(filename string, userID int64, size int64) error {
	return RecordUploadHashed(filename, userID, size, "")
}

// RecordUploadHashed 带服务端权威 sha256 的落库（P1 批次3 ADR-003 R2：新上传必带哈希；
// sha256 传空串仅兼容路径使用，由回填收敛）。
func RecordUploadHashed(filename string, userID int64, size int64, sha256 string) error {
	_, err := database.DB.Exec(
		"INSERT OR IGNORE INTO uploads (filename, user_id, size, sha256) VALUES (?, ?, ?, ?)",
		filename, userID, size, sha256,
	)
	return err
}

// GetUploadSHA256 读附件权威哈希（” = 未回填；行不存在返回 ”）。
func GetUploadSHA256(filename string) string {
	var s string
	_ = database.DB.QueryRow("SELECT sha256 FROM uploads WHERE filename = ?", filename).Scan(&s)
	return s
}

// SetUploadHash 回填/补算结果落库（仅命中空值行，幂等且不覆盖既有权威值）。
func SetUploadHash(filename, sha256 string) error {
	_, err := database.DB.Exec("UPDATE uploads SET sha256 = ? WHERE filename = ? AND (sha256 = '' OR sha256 IS NULL)", sha256, filename)
	return err
}

// ListUploadsMissingHash 低优先级回填任务取批（ADR-003 R3）：每批 limit 个 sha256=” 行。
func ListUploadsMissingHash(limit int) ([]string, error) {
	rows, err := database.DB.Query("SELECT filename FROM uploads WHERE sha256 = '' OR sha256 IS NULL ORDER BY id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CountUploadsMissingHash 回填进度可见性（管理端/日志）。
func CountUploadsMissingHash() (int, error) {
	var n int
	err := database.DB.QueryRow("SELECT COUNT(*) FROM uploads WHERE sha256 = '' OR sha256 IS NULL").Scan(&n)
	return n, err
}

// FileSHA256 计算磁盘文件 sha256（64 位小写 hex）。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeAndPersistUploadHash 即时补算单文件哈希并落库（ADR-003 R4 保证位：
// 事件/快照产出前 sha256=” 的行由此收敛，协议保证消费端收到的 ref 哈希非空）。
// 文件缺失/IO 失败返回错误，调用方决定降级策略（事件面：宁缺哈希不产错哈希）。
func ComputeAndPersistUploadHash(uploadDir, filename string) (string, error) {
	sha, err := FileSHA256(filepath.Join(uploadDir, filepath.Base(filename)))
	if err != nil {
		return "", err
	}
	if err := SetUploadHash(filename, sha); err != nil {
		return "", err
	}
	return sha, nil
}

// ListUploadsByUser 取用户全部上传文件名（删用户清理用，需在删除用户前调用——
// 用户删除后记录随 CASCADE 清除）。
func ListUploadsByUser(userID int64) ([]string, error) {
	rows, err := database.DB.Query("SELECT filename FROM uploads WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// CountUploadsByUser 统计用户上传记录数（冒烟断言用）。
func CountUploadsByUser(userID int64) (int, error) {
	var count int
	err := database.DB.QueryRow("SELECT COUNT(*) FROM uploads WHERE user_id = ?", userID).Scan(&count)
	return count, err
}

// SumUploadSizeByUser 汇总用户上传文件总字节数（v18.7 存储配额校验用）。
func SumUploadSizeByUser(userID int64) (int64, error) {
	var total sql.NullInt64
	err := database.DB.QueryRow("SELECT SUM(size) FROM uploads WHERE user_id = ?", userID).Scan(&total)
	return total.Int64, err
}

// GetUploadOwner 查询文件归属用户 ID（v20.8 审计修复），用于 ServeUpload 校验。
// 返回 0 表示文件无归属记录。
func GetUploadOwner(filename string) (int64, error) {
	var owner int64
	err := database.DB.QueryRow("SELECT user_id FROM uploads WHERE filename = ?", filename).Scan(&owner)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return owner, err
}

// QueueUploadCleanup 将删除失败的物理文件加入待清理队列（v21.67 P1-03）。
// 用于删除用户时物理文件删除失败——用户已删、uploads 记录已级联清除，
// 无法再靠 uploads.status 追踪，需独立队列兜底重试。
// v21.68 P2-03：记录 user_id 便于溯源。
func QueueUploadCleanup(filename string, userID int64) error {
	_, err := database.DB.Exec("INSERT OR IGNORE INTO pending_upload_cleanup (filename, user_id) VALUES (?, ?)", filename, userID)
	return err
}

// BackfillUploadHashesOnce 低优先级回填一轮（ADR-003 R3）：至多 batch 个 ” 行，
// 文件缺失的行跳过并保持 ”（下次重试；宁缺哈希不产错哈希）。返回（本轮完成数, 剩余数）。
func BackfillUploadHashesOnce(uploadDir string, batch int) (int, int, error) {
	names, err := ListUploadsMissingHash(batch)
	if err != nil {
		return 0, 0, err
	}
	done := 0
	for _, n := range names {
		if _, err := ComputeAndPersistUploadHash(uploadDir, n); err == nil {
			done++
		}
	}
	remaining, err := CountUploadsMissingHash()
	if err != nil {
		return done, -1, err
	}
	return done, remaining, nil
}

// GetUploadMeta 附件归属行元数据（sync 上传通道 R5 判定用）。
func GetUploadMeta(filename string) (size int64, sha string, owner int64, exists bool, err error) {
	err = database.DB.QueryRow("SELECT size, COALESCE(sha256, ''), user_id FROM uploads WHERE filename = ?", filename).Scan(&size, &sha, &owner)
	if err == sql.ErrNoRows {
		return 0, "", 0, false, nil
	}
	if err != nil {
		return 0, "", 0, false, err
	}
	return size, sha, owner, true, nil
}

// SetUploadHashForce 无条件写入权威哈希（仅 sync 通道 R5 竞态回填：同尺寸文件重传
// 且服务端实算为准的场景；一般回填仍用条件版 SetUploadHash 不覆盖既有值）。
func SetUploadHashForce(filename, sha256 string) error {
	_, err := database.DB.Exec("UPDATE uploads SET sha256 = ? WHERE filename = ?", sha256, filename)
	return err
}
