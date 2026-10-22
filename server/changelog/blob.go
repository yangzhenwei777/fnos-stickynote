// blob.go —— §32~36 大载荷通道（P1-b，2026-10-15）。
//
// 背景：NOTE payload 内联进 sync_changes.payload_json。服务端内容硬上限 100MB
// （加密笔记密文另有 base64 膨胀），超大行会撑爆 DB 行/复制成本/快照与 pull 分发效率。
// 本文件按 v20-Project §32~36 落地 blob_ref 分流通道：
//
//	§32 INLINE_PAYLOAD_LIMIT=4MB：≤4MB 走 payload_json 内联；>4MB 走
//	    payload_kind='blob_ref' + payload_ref=<sha256> + payload_size。
//	§33 禁止二次大内存复制：json.Encoder 流式写临时文件 → MultiWriter 同步喂
//	    sha256 → 同目录 atomic rename（写盘路径零 payload 级内存缓冲）。
//	§34 Blob 存 DATA_DIR/sync-payloads/<sha256>，权限 0600；禁止静态 HTTP，
//	    读取只经 GET /api/sync/payload/:sha256（sync/payload_endpoint.go，JWT+§35）。
//	§35 授权 = 该 sha256 仍被当前 user 的有效 sync_changes 引用，否则 404
//	    （不能只因为知道 hash 就读取）。V1 Blob 仅用于 ChangeLog（Bootstrap 从
//	    冻结快照读实体状态，不引用 Change payload），故授权面只查 sync_changes。
//	§36 GC 只删「无任何有效 sync_changes 引用」的 blob；事务失败孤儿由
//	    INSERT 失败即时清理 + GCOrphanPayloads 兜底。V1 服务端 sync_changes 无
//	    保留期清理机制（事件流只增不减），故 GC 仅作为函数与兜底存在，不自动挂载；
//	    服务端将来引入 ChangeLog 保留策略时一并接线。
//
// 兼容性：payloadsDir 未配置（单测/未接线部署）时全部走内联（旧行为），功能关闭语义。
package changelog

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"stickynote/syncspec"
)

// InlinePayloadLimit 内联载荷上限（§32 字面量）。
const InlinePayloadLimit = 4 << 20 // 4MB

// notePayloadOverhead 预判余量：content/encrypted 之外的 JSON 结构开销
// （title/tags/attachment_refs/时间戳等，含 JSON 转义放大），超限即走 blob 通道。
const notePayloadOverhead = 64 * 1024

var payloadDir atomic.Value // string；空 = 未配置（全部内联）

// ConfigurePayloads 注入 blob 存储根（cmd/server.go 启动接线：DATA_DIR/sync-payloads）。
func ConfigurePayloads(dir string) { payloadDir.Store(dir) }

// PayloadsDir 读取当前注入的 blob 存储根（空 = 未配置；sync 包 GC 与端点共用）。
func PayloadsDir() string {
	if v, ok := payloadDir.Load().(string); ok {
		return v
	}
	return ""
}

func payloadsDir() string { return PayloadsDir() }

// ShouldInlineNote 预判 NOTE payload 是否内联（§32：体量预判是估算，"可能大"即走
// blob——宁可 blob 也不把 100MB 行塞进 DB；误差由 Overhead 余量吸收）。
func ShouldInlineNote(p syncspec.NotePayload) bool {
	return len(p.Content)+len(p.EncryptedContent)+notePayloadOverhead <= InlinePayloadLimit
}

// AppendNotePayload NOTE 事件统一落库入口（§32 分流判定 + 写入）。
// 三处 NOTE 生产者共用（app emitNoteChange / sync push 主路径 / push_folder 级联），
// 防止"只改一处、另两处仍内联"的双源漂移。FOLDER/FOLDER_ORDER 载荷极小，不接此通道。
func AppendNotePayload(tx *sql.Tx, seq, userID int64, entitySyncID *string, operation string, serverVersion int, p syncspec.NotePayload, deviceID string) error {
	if payloadsDir() == "" || ShouldInlineNote(p) {
		return AppendChange(tx, seq, userID, syncspec.EntityTypeNote, entitySyncID, operation, serverVersion, MarshalPayload(p), deviceID)
	}
	return appendChangeBlob(tx, seq, userID, entitySyncID, operation, serverVersion, p, deviceID)
}

// appendChangeBlob §32 blob_ref 行落库。INSERT 失败即时清理刚写的 blob
// （把孤儿窗口压到最小；INSERT 成功但事务最终回滚的残余孤儿由 GCOrphanPayloads 兜底）。
func appendChangeBlob(tx *sql.Tx, seq, userID int64, entitySyncID *string, operation string, serverVersion int, p syncspec.NotePayload, deviceID string) error {
	ref, size, err := writePayloadBlob(p)
	if err != nil {
		return fmt.Errorf("append blob change %d: %w", seq, err)
	}
	_, err = tx.Exec(`INSERT INTO sync_changes
		(change_seq, user_id, entity_type, entity_sync_id, operation, server_version, payload_kind, payload_json, payload_ref, payload_size, device_id)
		VALUES (?, ?, ?, ?, ?, ?, 'blob_ref', '', ?, ?, ?)`,
		seq, userID, syncspec.EntityTypeNote, entitySyncID, operation, serverVersion, ref, size, deviceID)
	if err != nil {
		_ = os.Remove(PayloadPath(ref))
		return fmt.Errorf("append blob change %d: %w", seq, err)
	}
	return nil
}

// writePayloadBlob §33 零大内存复制路径：Encoder 直写临时文件并经 MultiWriter 同步
// 喂 sha256（无整载荷内存缓冲、无回读）→ 同目录 atomic rename → 0600（§34）。
// 返回 ref（hex sha256）与字节数（含 Encoder 尾部换行，客户端解析不受影响）。
func writePayloadBlob(p any) (ref string, size int64, err error) {
	dir := payloadsDir()
	if dir == "" {
		return "", 0, errors.New("changelog: payload dir not configured")
	}
	if merr := os.MkdirAll(dir, 0700); merr != nil {
		return "", 0, fmt.Errorf("payload dir: %w", merr)
	}
	tmp, err := os.CreateTemp(dir, ".payload-*")
	if err != nil {
		return "", 0, fmt.Errorf("payload temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		_ = os.Remove(tmpName)
	}
	h := sha256.New()
	enc := json.NewEncoder(io.MultiWriter(tmp, h))
	if err = enc.Encode(p); err != nil {
		cleanup()
		return "", 0, fmt.Errorf("payload encode: %w", err)
	}
	if size, err = tmp.Seek(0, io.SeekCurrent); err != nil {
		cleanup()
		return "", 0, fmt.Errorf("payload size: %w", err)
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", 0, fmt.Errorf("payload close: %w", err)
	}
	ref = hex.EncodeToString(h.Sum(nil))
	final := PayloadPath(ref)
	// 同目录 rename 原子；目标已存在（同内容重复事件）时覆盖——sha256 同 = 内容同，幂等无害。
	if err = os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return "", 0, fmt.Errorf("payload rename: %w", err)
	}
	if err = os.Chmod(final, 0600); err != nil { // §34 权限
		return "", 0, fmt.Errorf("payload chmod: %w", err)
	}
	return ref, size, nil
}

// PayloadPath blob 最终路径（§34：DATA_DIR/sync-payloads/<sha256>）。
// 调用方必须先做 hex64 校验（端点侧），本函数不做二次校验以防误用为路径拼接面。
func PayloadPath(ref string) string {
	return filepath.Join(payloadsDir(), ref)
}

// ValidPayloadRef 端点入参校验：64 位**小写** hex（sha256 形状），天然拒绝路径穿越。
// 小写强约束：Windows 大小写不敏感文件系统上，大写 ref 会与本写法落盘的小写路径
// 指向同一文件——引用判定（DB 内恒小写）必须与路径判定同口径。
func ValidPayloadRef(ref string) bool {
	if len(ref) != 64 || ref != strings.ToLower(ref) {
		return false
	}
	_, err := hex.DecodeString(ref)
	return err == nil
}

// OpenPayload 打开 blob 文件（端点流式回传用）。调用方负责关闭。
func OpenPayload(ref string) (*os.File, int64, error) {
	f, err := os.Open(PayloadPath(ref))
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// PayloadReferenced §35 授权查询：该 sha256 是否仍被该用户的有效 sync_changes 引用。
// V1 ChangeLog 无保留期清理（事件流只增不减），全部行即"有效"；将来引入保留策略时
// 在此追加保留期条件（与 sync 包 GCOrphanPayloads 的引用集合判定保持同一口径）。
func PayloadReferenced(db *sql.DB, userID int64, ref string) (bool, error) {
	var one int
	err := db.QueryRow(`SELECT 1 FROM sync_changes
		WHERE user_id = ? AND payload_kind = 'blob_ref' AND payload_ref = ? LIMIT 1`,
		userID, ref).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ListPayloadRefs 返回全部 blob 引用（§36 GC 的引用集合来源；DB 句柄由调用方注入
// ——本包零 DB 依赖纪律，不持全局句柄）。
func ListPayloadRefs(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT DISTINCT payload_ref FROM sync_changes WHERE payload_kind = 'blob_ref'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string]bool{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		refs[r] = true
	}
	return refs, rows.Err()
}
