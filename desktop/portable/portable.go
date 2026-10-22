// Package portable —— 便携备份 .snbak PORTABLE（离线方案 v20-Project P6，§106~§111 +
// 终审补充裁决"剔除 totp_secret 与一切凭据"；必测 §148；红线 8/25/26/27）。
//
// 规格锚点（v20 原文）：
//   - §106 同一 SNBK Envelope（format=1/enc=1），内部 manifest 增 backup_type=PORTABLE，
//     不新增 Envelope 版本 → 复用 server/backup 的 EnvelopeSeal/Open/BuildArchive/ExtractArchive，
//     master 由调用方注入 = PBKDF2(备份口令, 随机 salt, 600k, 32B)（终审裁决：派生可行）。
//   - §107 内容 = 当前用户 sealed db + uploads + sync_state/outbox/conflicts + manifest；
//     不含 JWT Secret / 其他用户 / Admin / WebDAV 凭据 / Server Backup Target。
//   - §109 >256MB 明确拒绝（V1；enc=2 分块二期）。
//   - §111 manifest 增补 backup_set_id / server_epoch / sync_protocol_version。
//
// 容器格式（薄封装，信封本体零改动）：
//
//	"SNBP"[4] | ver[1]=1 | kdf_iter[4 BE] | salt_len[1]=16 | salt[16] | <SNBK envelope bytes>
//
// AAD/master 绑定全部沿用信封层（backup_id/pt_size/kid 进 AAD）。salt 明文在头
// （PBKDF2 salt 非机密）；口令错误 → GCM 认证失败（ErrDecryptFailed），无预言机。
package portable

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	// 容器魔数与版本（区别于裸 SNBK：便携包多一个 KDF 头。信封本体仍是 SNBK/enc=1）。
	containerMagic = "SNBP"
	containerVer   = 1

	pbkdfSaltLen = 16
	pbkdfIter    = 600_000 // 与 NPD1 现行参数对齐（P0-C 冻结口径）
	pbkdfMaxIter = 2_000_000
	masterKeyLen = 32

	// MinPasswordLen 与既定决策"口令 min=6"对齐（备份口令面）。
	MinPasswordLen = 6
)

var (
	// ErrPasswordWeak 备份口令过短（导出侧拒绝；导入侧统一走 ErrDecryptFailed，不做口令预言机）。
	ErrPasswordWeak = errors.New("portable: password too short (min 6)")
	// ErrBadContainer .snbak 便携容器头非法/截断。
	ErrBadContainer = errors.New("portable: bad SNBP container")
	// ErrNotPortable 包内 manifest 不是 PORTABLE（服务端包或伪造）。
	ErrNotPortable = errors.New("portable: archive is not a PORTABLE backup")
	// ErrAttachmentsMissing 引用集附件缺失（导出 fail-closed；红线 17/25）。
	ErrAttachmentsMissing = errors.New("portable: referenced attachments missing on disk")
	// ErrArchiveMismatch 解包文件集与 manifest 不吻合（缺失/多余/哈希不符；红线 25）。
	ErrArchiveMismatch = errors.New("portable: archive contents do not match manifest")
	// ErrEmptyProfile 无可激活的本地代（红线 26 前置：必须有活动 generation 可导）。
	ErrEmptyProfile = errors.New("portable: no active generation to export")
)

// deriveMaster 备份口令 → 32B 信封 master（PBKDF2-HMAC-SHA256，参数在容器头自描述）。
func deriveMaster(password string, salt []byte, iter int) []byte {
	return pbkdf2.Key([]byte(password), salt, iter, masterKeyLen, sha256.New)
}

// randRead 全熵填充（salt 生成收口，便于测试注桩）。
func randRead(b []byte) (int, error) { return rand.Read(b) }

// marshalManifest 内部 manifest 序列化（缩进 JSON，服务端归档同格式族）。
func marshalManifest(m any) ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("portable: marshal manifest: %w", err)
	}
	return b, nil
}

// uuid4 生成 36 字符 UUIDv4（EnvelopeSeal 要求 backup_id 为 36 字符）。
func uuid4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("portable: uuid rand: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// writeContainer 拼 SNBP 头 + 信封字节并原子落盘（.part → fsync → rename）。
func writeContainer(path string, salt []byte, iter int, envelope []byte) error {
	if len(salt) == 0 || len(salt) > 64 {
		return errors.New("portable: bad salt length")
	}
	var head bytes.Buffer
	head.WriteString(containerMagic)
	head.WriteByte(containerVer)
	var it [4]byte
	binary.BigEndian.PutUint32(it[:], uint32(iter))
	head.Write(it[:])
	head.WriteByte(byte(len(salt)))
	head.Write(salt)
	head.Write(envelope)

	dir := filepath.Dir(path)
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("portable: open part: %w", err)
	}
	if _, err := f.Write(head.Bytes()); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("portable: rename final: %w", err)
	}
	return nil
}

// readContainer 解析头部并返回 (salt, iter, 信封字节)。不整读超大文件前先做长度体检。
func readContainer(path string, maxFileBytes int64) (salt []byte, iter int, envelope []byte, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("portable: stat: %w", err)
	}
	if maxFileBytes > 0 && st.Size() > maxFileBytes {
		return nil, 0, nil, fmt.Errorf("%w: file %d bytes > limit %d", ErrArchiveMismatch, st.Size(), maxFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, nil, err
	}
	// magic4+ver1+iter4+saltlen1 = 10 起
	if len(raw) < 10 || string(raw[:4]) != containerMagic {
		return nil, 0, nil, ErrBadContainer
	}
	if raw[4] != containerVer {
		return nil, 0, nil, fmt.Errorf("%w: unsupported container ver %d", ErrBadContainer, raw[4])
	}
	iter = int(binary.BigEndian.Uint32(raw[5:9]))
	saltLen := int(raw[9])
	if saltLen == 0 || saltLen > 64 || len(raw) < 10+saltLen {
		return nil, 0, nil, ErrBadContainer
	}
	if iter < 1024 || iter > pbkdfMaxIter {
		return nil, 0, nil, fmt.Errorf("%w: kdf iter %d out of range", ErrBadContainer, iter)
	}
	salt = raw[10 : 10+saltLen]
	envelope = raw[10+saltLen:]
	if len(envelope) < 7+36+8+8+12+16 {
		return nil, 0, nil, fmt.Errorf("%w: envelope truncated", ErrBadContainer)
	}
	return salt, iter, envelope, nil
}

// ---- 快照脱敏（§107 + 终审补充裁决：剔除 totp_secret 与一切凭据/服务器面表） ----

// sanitizeSnapshot 在**导出的快照副本**上执行（绝不触碰 active 库）。幂等、单事务：
//   - client_profile：DPAPI 封存件（refresh/device_secret）+ PIN verifier + device_id 全删
//     （换机后无意义且属凭据面；导入端由目标设备自己的档案重植）。
//   - users 行保留（§98 FK 需要）但凭据列全部清空：password_hash/security_question/
//     security_answer_hash/totp_secret/refresh_jti，totp_enabled=0。
//   - totp_recovery / refresh_sessions / login_fails：认证与会话状态表清空。
//   - backup_* / sync_snapshots* / restore_finalizations：§107 "Server Backup Target" 面清空（纵深）。
//   - admin_logs：若存在则清（历史缺表容忍）。
func sanitizeSnapshot(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, t := range []string{
		"client_profile", "totp_recovery", "refresh_sessions", "login_fails",
		"backup_targets", "backup_jobs", "backup_snapshots", "backup_placements",
		"sync_snapshots", "sync_snapshots_hold", "restore_finalizations", "admin_logs",
	} {
		ex, err := tableExistsTx(tx, t)
		if err != nil {
			tx.Rollback()
		}
		if !ex {
			continue
		}
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			tx.Rollback()
			return fmt.Errorf("portable: sanitize delete %s: %w", t, err)
		}
	}
	// users 凭据列：逐列存在性守卫（老快照容忍），一条 UPDATE 收口。
	cols := map[string]string{
		"password_hash": "''", "security_question": "''", "security_answer_hash": "''",
		"totp_secret": "''", "totp_enabled": "0", "refresh_jti": "''",
	}
	var sets []string
	for c, v := range cols {
		ex, err := columnExistsTx(tx, "users", c)
		if err != nil {
			tx.Rollback()
			return err
		}
		if ex {
			sets = append(sets, c+"="+v)
		}
	}
	if len(sets) > 0 {
		if _, err := tx.Exec("UPDATE users SET " + strings.Join(sets, ", ")); err != nil {
			tx.Rollback()
			return fmt.Errorf("portable: sanitize users creds: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// 脱敏后再 VACUUM 一次：DELETE/UPDATE 只释放页不擦内容，重建库把旧凭据页物理丢弃
	// （终审"剔除一切凭据"的纵深；测试侧以 sentinel 字节扫描钉死）。
	if _, err := db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("portable: sanitize vacuum: %w", err)
	}
	return nil
}

func tableExistsTx(tx *sql.Tx, name string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0, err
}

func columnExistsTx(tx *sql.Tx, table, col string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, col).Scan(&n)
	return n > 0, err
}

// ---- 小工具 ----

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// isBasenames 附件/文件名受控校验（UUID 名的纵深防御，与服务端同口径）。
func isSafeBasename(n string) bool {
	return n != "" && filepath.Base(n) == n && !strings.ContainsAny(n, `/\`) && n != "." && n != ".."
}
