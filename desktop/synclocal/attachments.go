package synclocal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"stickynote/syncspec"
)

// attachments.go —— 桌面附件双向通道（P1 批次3，ADR-003 R2/R5/R6 + ADR-004 R3）。
//
// 上传侧（journal）：本地上传的文件登记 (filename, sha, size, LOCAL)；push 前确保已到达
// 服务端（UPLOADED）；收到 ATTACHMENT_NOT_FOUND 把涉事文件重置 LOCAL 强制重传后
// 同一 op 原样重试（R3 自恢复，重试上限由 outbox retry_count 承接，≥3 转 CONFLICT 人工面）。
//
// 下载侧（queue）：pull 应用的 NOTE 事件 attachment_refs 入队 (PENDING)；每轮
// SyncAttachmentsOnce 下载 + sha256 校验（R6：不符删除重下，累计 3 次失败标 ERROR
// 占位降级，不崩渲染）；ReverifyAttachments 供启动线/篡改检测把磁盘坏件重排 PENDING。

var ErrAttachmentMissing = errors.New("attachment missing")

// ensureAttachmentTables 队列/账本表（并入 EnsureTables 幂等建表）。
const attachmentTables = `
CREATE TABLE IF NOT EXISTS sync_attachment_journal (
	filename   TEXT PRIMARY KEY,
	sha256     TEXT NOT NULL DEFAULT '',
	size       INTEGER NOT NULL DEFAULT 0,
	status     TEXT NOT NULL DEFAULT 'LOCAL', -- LOCAL | UPLOADED
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sync_attachments (
	filename   TEXT PRIMARY KEY,
	sha256     TEXT NOT NULL DEFAULT '',
	size       INTEGER NOT NULL DEFAULT 0,
	status     TEXT NOT NULL DEFAULT 'PENDING', -- PENDING | OK | ERROR
	retries    INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

// MarkAttachmentLocal 本地上传成功后登记（binding 层调用）。
func MarkAttachmentLocal(db *sql.DB, filename, sha string, size int64) error {
	_, err := db.Exec(`INSERT INTO sync_attachment_journal (filename, sha256, size, status, updated_at)
		VALUES (?, ?, ?, 'LOCAL', CURRENT_TIMESTAMP)
		ON CONFLICT(filename) DO UPDATE SET sha256 = excluded.sha256, size = excluded.size,
			status = 'LOCAL', updated_at = CURRENT_TIMESTAMP`, filename, sha, size)
	return err
}

// attachmentJournalStatus 查账本状态（不存在返回空串）。
func attachmentJournalStatus(db *sql.DB, filename, sha string) string {
	var st, stored string
	err := db.QueryRow(`SELECT status, sha256 FROM sync_attachment_journal WHERE filename = ?`, filename).Scan(&st, &stored)
	if err != nil || stored != sha {
		return "" // 无账或 sha 变了 → 需要（重）上传
	}
	return st
}

// attachmentUploaded 账本判定：UPLOADED 且 sha 一致才免传。
func attachmentUploaded(db *sql.DB, filename, sha string) bool {
	var st, stored string
	err := db.QueryRow(`SELECT status, sha256 FROM sync_attachment_journal WHERE filename = ?`, filename).Scan(&st, &stored)
	return err == nil && st == "UPLOADED" && (sha == "" || stored == sha)
}

func markAttachmentUploaded(db *sql.DB, filename, sha string, size int64) error {
	_, err := db.Exec(`INSERT INTO sync_attachment_journal (filename, sha256, size, status, updated_at)
		VALUES (?, ?, ?, 'UPLOADED', CURRENT_TIMESTAMP)
		ON CONFLICT(filename) DO UPDATE SET sha256 = excluded.sha256, size = excluded.size,
			status = 'UPLOADED', updated_at = CURRENT_TIMESTAMP`, filename, sha, size)
	return err
}

// ResetAttachmentsForReupload ATTACHMENT_NOT_FOUND 自恢复：涉事文件账本重置 LOCAL（R3）。
func ResetAttachmentsForReupload(db *sql.DB, filenames []string) error {
	for _, f := range filenames {
		if _, err := db.Exec(`UPDATE sync_attachment_journal SET status='LOCAL', updated_at=CURRENT_TIMESTAMP
			WHERE filename = ?`, f); err != nil {
			return err
		}
	}
	return nil
}

// execer *sql.Tx / *sql.DB 的公共执行面（pull 应用在事务内入队下载队列）。
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// QueueAttachmentsFromRefs pull 应用后把权威 refs 入下载队列：
// 新文件名或 sha 变化 → PENDING；本地文件已在且哈希一致 → 直接 OK（免重下）。
func QueueAttachmentsFromRefs(db execer, dir string, refs []syncspec.AttachmentRef) error {
	for _, r := range refs {
		if r.Filename == "" {
			continue
		}
		status := "PENDING"
		if r.Sha256 != "" && dir != "" {
			if localSHA, err := hashFile(filepath.Join(dir, filepath.Base(r.Filename))); err == nil && localSHA == r.Sha256 {
				status = "OK"
			}
		}
		if _, err := db.Exec(`INSERT INTO sync_attachments (filename, sha256, size, status, retries, updated_at)
			VALUES (?, ?, ?, ?, 0, CURRENT_TIMESTAMP)
			ON CONFLICT(filename) DO UPDATE SET
				sha256 = excluded.sha256, size = excluded.size,
				status = CASE WHEN sync_attachments.sha256 != excluded.sha256 THEN 'PENDING' ELSE excluded.status END,
				retries = CASE WHEN sync_attachments.sha256 != excluded.sha256 THEN 0 ELSE sync_attachments.retries END,
				updated_at = CURRENT_TIMESTAMP`, r.Filename, r.Sha256, r.Size, status); err != nil {
			return err
		}
	}
	return nil
}

const attachmentMaxRetries = 3

// SyncAttachmentsOnce 下载轮：PENDING 行逐个下载 + 权威校验（R6）。
// 磁盘文件已匹配（本地上传未清队/上次成功）→ 直接 OK；不符/缺失 → 下载临时文件校验，
// 匹配原子改名落位 + 账本记 UPLOADED（B 端之后编辑该件免重传）；
// 失败 retries++，达上限标 ERROR（占位降级，人工面 P4 处理）。
func (w *Worker) SyncAttachmentsOnce() (downloaded, repaired, failed int, err error) {
	if w.cl == nil {
		return 0, 0, 0, nil
	}
	rows, err := w.db.Query(`SELECT filename, sha256 FROM sync_attachments WHERE status='PENDING' ORDER BY filename`)
	if err != nil {
		return 0, 0, 0, err
	}
	type pend struct{ name, sha string }
	var list []pend
	for rows.Next() {
		var p pend
		if err := rows.Scan(&p.name, &p.sha); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		list = append(list, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}

	for _, p := range list {
		safe := filepath.Base(p.name) // 账本 filename 源自 payload：落盘强制 Base（防穿越，同 ServeUpload 纪律）
		final := filepath.Join(w.AttachDir, safe)
		if cur, herr := hashFile(final); herr == nil && p.sha != "" && cur == p.sha {
			w.markAttachmentDone(p.name, p.sha, 0, "OK")
			repaired++
			continue
		}
		data, derr := w.cl.DownloadAttachment(safe)
		if derr != nil {
			w.noteAttachmentFailure(p.name, derr)
			failed++
			continue
		}
		got := sha256.Sum256(data)
		if p.sha != "" && hex.EncodeToString(got[:]) != p.sha {
			// 篡改/半态（T-09）：拒落盘，走失败计数。
			w.noteAttachmentFailure(p.name, fmt.Errorf("sha256 mismatch on download"))
			failed++
			continue
		}
		tmp := final + ".att-tmp"
		if err := os.WriteFile(tmp, data, 0600); err != nil {
			w.noteAttachmentFailure(p.name, err)
			failed++
			continue
		}
		if err := os.Rename(tmp, final); err != nil {
			_ = os.Remove(tmp)
			w.noteAttachmentFailure(p.name, err)
			failed++
			continue
		}
		// 下载成功 = 服务端已有此内容：账本记 UPLOADED。
		if err := markAttachmentUploaded(w.db, p.name, hex.EncodeToString(got[:]), int64(len(data))); err != nil {
			return downloaded, repaired, failed, err
		}
		w.markAttachmentDone(p.name, hex.EncodeToString(got[:]), int64(len(data)), "OK")
		downloaded++
	}
	return downloaded, repaired, failed, nil
}

func (w *Worker) markAttachmentDone(name, sha string, size int64, status string) {
	_, _ = w.db.Exec(`UPDATE sync_attachments SET status=?, sha256=CASE WHEN sha256='' THEN ? ELSE sha256 END,
		size=CASE WHEN size=0 THEN ? ELSE size END, retries=0, last_error='', updated_at=CURRENT_TIMESTAMP
		WHERE filename=?`, status, sha, size, name)
}

func (w *Worker) noteAttachmentFailure(name string, e error) {
	var retries int
	_ = w.db.QueryRow(`SELECT retries FROM sync_attachments WHERE filename=?`, name).Scan(&retries)
	retries++
	status := "PENDING"
	if retries >= attachmentMaxRetries {
		status = "ERROR" // 占位降级（UI 渲染不崩；修复入口 P4）
	}
	_, _ = w.db.Exec(`UPDATE sync_attachments SET retries=?, status=?, last_error=?, updated_at=CURRENT_TIMESTAMP
		WHERE filename=?`, retries, status, truncateErr(e), name)
}

// ReverifyAttachments 启动线/手动修复入口：OK 行与磁盘比对，坏件重排 PENDING（T-09）。
func ReverifyAttachments(db *sql.DB, dir string) (requeued int, err error) {
	rows, err := db.Query(`SELECT filename, sha256 FROM sync_attachments WHERE status='OK'`)
	if err != nil {
		return 0, err
	}
	type item struct{ name, sha string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.name, &it.sha); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	for _, it := range items {
		cur, herr := hashFile(filepath.Join(dir, filepath.Base(it.name)))
		if herr == nil && it.sha != "" && cur == it.sha {
			continue
		}
		if _, err := db.Exec(`UPDATE sync_attachments SET status='PENDING', retries=0, updated_at=CURRENT_TIMESTAMP
			WHERE filename=?`, it.name); err != nil {
			return requeued, err
		}
		requeued++
	}
	return requeued, nil
}

// PendingAttachmentStats 数据中心可见性（待修复附件数）。
func PendingAttachmentStats(db *sql.DB) (pending, errored int, err error) {
	if err = db.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE status='PENDING'),
		COUNT(*) FILTER (WHERE status='ERROR') FROM sync_attachments`).Scan(&pending, &errored); err != nil {
		// 兼容无 FILTER 的旧 SQLite（modernc 支持 FILTER；此分支纯防御）。
		err = db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN status='PENDING' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status='ERROR' THEN 1 ELSE 0 END),0) FROM sync_attachments`).Scan(&pending, &errored)
	}
	return pending, errored, err
}

func hashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func truncateErr(e error) string {
	s := e.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
