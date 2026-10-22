package synclocal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"encoding/json"

	"stickynote/database"
	"stickynote/syncspec"
)

// attachment_test.go —— P1 批次3 桌面附件通道验收（ADR-003 R2/R6 + ADR-004 R3）：
// 双桌面图片笔记收敛（上传预传 → pull 下载校验）、篡改/缺失自愈（T-09）、
// 服务端 GC 后重传闭环（ATTACHMENT_NOT_FOUND → 同 op 重试）。

const attachName = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.png"

func pngBytes(seed byte) []byte {
	b := make([]byte, 40)
	copy(b, []byte{0x89, 'P', 'N', 'G', 13, 10, 26, 10})
	for i := 8; i < len(b); i++ {
		b[i] = seed
	}
	return b
}

func writeLocalFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// seedLocalAttachment 本地库造 上传归属 + 笔记引用（推送侧夹具）。
func seedLocalAttachment(t *testing.T, db *sql.DB, name string, data []byte, title string) int64 {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO uploads (filename, user_id, size) VALUES (?, 1, ?)`, name, len(data)); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO notes (user_id, title, content, content_text, tags, sync_id)
		VALUES (1, ?, '<img src="/uploads/' || ? || '">', 'x', '', ?)`, title, name, "att-"+title)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO note_uploads (note_id, filename) VALUES (?, ?)`, id, name); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueNoteOp(db, id, "CREATE"); err != nil {
		t.Fatal(err)
	}
	return id
}

func newAttachWorker(t *testing.T, db *sql.DB, ts *httptest.Server, dev, dir string) *Worker {
	t.Helper()
	w := NewWorker(db, NewClient(ts.URL, "tok", dev))
	w.AttachDir = dir
	return w
}

func TestAttachmentEndToEndAndSelfHeal(t *testing.T) {
	ts := newServer(t)
	a, b := newStore(t), newStore(t)
	dirA, dirB := t.TempDir(), t.TempDir()

	data := pngBytes(7)
	sha := sha256.Sum256(data)
	writeLocalFile(t, dirA, attachName, data)
	seedLocalAttachment(t, a, attachName, data, "图笔")

	// A 推送：预传附件（服务端实算权威哈希入库）+ 笔记 op ACK。
	if _, err := newAttachWorker(t, a, ts, "devA", dirA).RunOnce(nilCtx()); err != nil {
		t.Fatal(err)
	}
	// B 拉取：refs 入下载队列 → 同轮下载 + sha256 校验落位。
	sumB, err := newAttachWorker(t, b, ts, "devB", dirB).RunOnce(nilCtx())
	if err != nil {
		t.Fatal(err)
	}
	if sumB.Downloaded != 1 {
		t.Fatalf("B 应下载 1 个附件: %+v", sumB)
	}
	got, err := os.ReadFile(filepath.Join(dirB, attachName))
	if err != nil || hex.EncodeToString(func() []byte { h := sha256.Sum256(got); return h[:] }()) != hex.EncodeToString(sha[:]) {
		t.Fatalf("B 附件字节必须与服务端权威一致: %v", err)
	}

	// T-09 篡改自愈：B 磁盘文件被改 → Reverify 重排 PENDING → 下轮命中校验重下复原。
	writeLocalFile(t, dirB, attachName, []byte("TAMPERED-TAMPERED-TAMPERED-TAMPERED!!!"))
	requeued, err := ReverifyAttachments(b, dirB)
	if err != nil || requeued != 1 {
		t.Fatalf("篡改应被重排队: requeued=%d err=%v", requeued, err)
	}
	if sum, err := newAttachWorker(t, b, ts, "devB", dirB).RunOnce(nilCtx()); err != nil || sum.Repaired+sum.Downloaded < 1 {
		t.Fatalf("重下轮异常: %+v %v", sum, err)
	}
	got, err = os.ReadFile(filepath.Join(dirB, attachName))
	if err != nil || len(got) != len(data) {
		t.Fatalf("篡改件未复原: %v", err)
	}
	if hex.EncodeToString(func() []byte { h := sha256.Sum256(got); return h[:] }()) != hex.EncodeToString(sha[:]) {
		t.Fatal("复原字节不一致")
	}

	// 双端皆不可得（服务端被回收 + 本地已删）→ 3 次上限 → ERROR 占位降级（不崩渲染）。
	_ = os.Remove(filepath.Join(dirB, attachName))
	if _, err := b.Exec(`UPDATE sync_attachments SET status='PENDING', retries=2 WHERE filename=?`, attachName); err != nil {
		t.Fatal(err)
	}
	deadW := NewWorker(b, NewClient("http://127.0.0.1:1", "tok", "devB")) // 传输必失败
	deadW.AttachDir = dirB
	if _, _, _, err := deadW.SyncAttachmentsOnce(); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := b.QueryRow(`SELECT status FROM sync_attachments WHERE filename=?`, attachName).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ERROR" {
		t.Fatalf("三连败应 ERROR 占位降级，得 %q", status)
	}
	pending, errored, err := PendingAttachmentStats(b)
	if err != nil || errored != 1 || pending != 0 {
		t.Fatalf("数据中心计数不符: pending=%d errored=%d err=%v", pending, errored, err)
	}
}

// TestServerGCForcesReupload —— ADR-004 R3 全闭环：服务端 GC 掉已上传件后，
// push 得 ATTACHMENT_NOT_FOUND（op 不消费）→ 账本重置 → 同 op_id 下一轮重传 → ACKED。
func TestServerGCForcesReupload(t *testing.T) {
	ts := newServer(t)
	a := newStore(t)
	dirA := t.TempDir()
	data := pngBytes(9)
	writeLocalFile(t, dirA, attachName, data)
	seedLocalAttachment(t, a, attachName, data, "GC笔")

	w := newAttachWorker(t, a, ts, "devA", dirA)
	if _, err := w.RunOnce(nilCtx()); err != nil {
		t.Fatal(err)
	}
	// 服务端"GC"：删行 + 删文件（模拟 24h 宽限过期，两步之间断网的桌面从此不可见）。
	// 服务端测试库即 global database.DB（newServer 同源初始化）。
	if _, err := database.DB.Exec(`DELETE FROM uploads WHERE filename=?`, attachName); err != nil {
		t.Fatal(err)
	}
	if sha := GetUploadSHAForTest(attachName); sha != "" {
		t.Fatal("前置：服务端行应已删")
	}

	// A 再编辑（引用同一附件）→ 第二轮 push → ATTACHMENT_NOT_FOUND（FAILED 不消费）。
	var nid int64
	if err := a.QueryRow(`SELECT id FROM notes WHERE title='GC笔'`).Scan(&nid); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(`UPDATE notes SET title='GC笔2' WHERE id=?`, nid); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueNoteOp(a, nid, "UPDATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RunOnce(nilCtx()); err != nil {
		t.Fatal(err)
	}
	var st string
	var rcn int
	if err := a.QueryRow(`SELECT status, retry_count FROM sync_outbox WHERE operation='UPDATE' ORDER BY seq DESC LIMIT 1`).Scan(&st, &rcn); err != nil {
		t.Fatal(err)
	}
	if st != "FAILED" || rcn < 1 {
		t.Fatalf("缺 ref 拒绝应 FAILED 待重试（不 CONFLICT 不 ACKED）: %s %d", st, rcn)
	}

	// 第三轮：账本自动重置 LOCAL → 重传 → 同 op_id 重试 ACKED。
	if _, err := w.RunOnce(nilCtx()); err != nil {
		t.Fatal(err)
	}
	if err := a.QueryRow(`SELECT status FROM sync_outbox WHERE operation='UPDATE' ORDER BY seq DESC LIMIT 1`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "ACKED" {
		t.Fatalf("重传后同 op 应 ACKED（R3 闭环），得 %s", st)
	}
	// 事件 refs 携带非空权威哈希（保证位）。
	pull, err := NewClient(ts.URL, "tok", "devC").Pull(0, 1<<60, 500)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ch := range pull.Changes {
		if ch.EntityType != syncspec.EntityTypeNote {
			continue
		}
		var p struct {
			Refs []struct {
				Filename string `json:"filename"`
				Sha256   string `json:"sha256"`
			} `json:"attachment_refs"`
		}
		_ = json.Unmarshal(ch.Payload, &p)
		for _, r := range p.Refs {
			if r.Filename == attachName && len(r.Sha256) == 64 {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("事件 attachment_refs 必须携带 64 位权威哈希（R4 保证位）")
	}
}

// GetUploadSHAForTest 服务端库直读（前置断言用）。
func GetUploadSHAForTest(name string) string {
	var s string
	if err := database.DB.QueryRow(`SELECT COALESCE(sha256, '') FROM uploads WHERE filename = ?`, name).Scan(&s); err != nil {
		return ""
	}
	return s
}
