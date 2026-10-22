package changelog

// blob_test.go —— §32~36 P1-b 服务端单测（2026-10-15）。
//
// 反向验证纪律：每个断言都对应一条可判定行为——分流边界、blob 行字段形状、
// 文件 sha256=ref、size 对拍、§35 引用判定、未配置回退内联（兼容旧部署）。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"stickynote/database"
	"stickynote/syncspec"
)

func setupBlobDB(t *testing.T) *sql.DB {
	t.Helper()
	if err := database.Init(filepath.Join(t.TempDir(), "blob-test.db"), "99.0.0"); err != nil {
		t.Skipf("database.Init 不可用: %v", err)
	}
	t.Cleanup(func() { database.DB.Close(); database.DB = nil })
	return database.DB
}

func setupBlobDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sync-payloads")
	ConfigurePayloads(dir)
	t.Cleanup(func() { ConfigurePayloads("") }) // 包级状态复位，不泄漏给其他用例
	return dir
}

func bigPayload(contentLen int) syncspec.NotePayload {
	return syncspec.NotePayload{
		SyncID:       "sync-abc",
		Title:        "big",
		Content:      strings.Repeat("x", contentLen),
		Tags:         "t",
		Version:      3,
		FolderSyncID: nil,
	}
}

func TestShouldInlineNoteBoundary(t *testing.T) {
	// 4MB - Overhead 以内 → 内联；明显超过 → blob。
	if !ShouldInlineNote(bigPayload(1024)) {
		t.Fatal("小载荷应判内联")
	}
	if ShouldInlineNote(bigPayload(InlinePayloadLimit)) {
		t.Fatal("≥4MB 内容体应判 blob（预判含 Overhead 余量，宁可 blob 不塞 DB）")
	}
}

func TestAppendNotePayloadBlobRoundTrip(t *testing.T) {
	db := setupBlobDB(t)
	setupBlobDir(t)
	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','user')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seq, err := NextChangeSeq(tx)
	if err != nil {
		t.Fatal(err)
	}
	syncID := "sync-abc"
	p := bigPayload(InlinePayloadLimit + 1024) // >4MB
	if err := AppendNotePayload(tx, seq, 1, &syncID, "UPDATE", 3, p, "dev-x"); err != nil {
		t.Fatalf("blob 分流写入失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var kind, ref, payloadJSON string
	var size int64
	if err := db.QueryRow(`SELECT payload_kind, payload_ref, payload_json, payload_size
		FROM sync_changes WHERE change_seq = ?`, seq).
		Scan(&kind, &ref, &payloadJSON, &size); err != nil {
		t.Fatal(err)
	}
	if kind != "blob_ref" || payloadJSON != "" {
		t.Fatalf("应落 blob_ref 行且 payload_json 为空: kind=%s json=%q", kind, payloadJSON)
	}
	if !ValidPayloadRef(ref) {
		t.Fatalf("ref 形状非法: %q", ref)
	}
	if size <= 0 {
		t.Fatalf("payload_size 应为正: %d", size)
	}
	// 文件在位且 sha256 == ref（写盘路径自证一致）。
	f, sz, err := OpenPayload(ref)
	if err != nil {
		t.Fatalf("blob 文件应可打开: %v", err)
	}
	defer f.Close()
	if sz != size {
		t.Fatalf("文件大小与 payload_size 不符: file=%d row=%d", sz, size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != ref {
		t.Fatalf("文件 sha256 与 ref 不符: %s vs %s", got, ref)
	}
	// 内容还原：读回解码应得原 payload。
	b, err := os.ReadFile(PayloadPath(ref))
	if err != nil {
		t.Fatal(err)
	}
	var back syncspec.NotePayload
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Content != p.Content || back.SyncID != p.SyncID {
		t.Fatal("blob 还原内容与原 payload 不一致")
	}
	// §34 权限 0600 仅 POSIX 文件系统可断言（NTFS 的 Perm() 恒 0666，chmod 只影响只读位）。
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(PayloadPath(ref))
		if st.Mode().Perm() != 0600 {
			t.Fatalf("§34 blob 权限应为 0600，实得 %v", st.Mode().Perm())
		}
	}
}

func TestAppendNotePayloadInlineSmall(t *testing.T) {
	db := setupBlobDB(t)
	setupBlobDir(t) // 已配置 dir，但小载荷仍走内联
	tx, _ := db.Begin()
	defer tx.Rollback()
	seq, _ := NextChangeSeq(tx)
	syncID := "sync-small"
	p := bigPayload(1024)
	if err := AppendNotePayload(tx, seq, 1, &syncID, "UPDATE", 1, p, ""); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := tx.QueryRow(`SELECT payload_kind FROM sync_changes WHERE change_seq = ?`, seq).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "json" {
		t.Fatalf("小载荷应保持内联 json，实得 %s", kind)
	}
}

func TestAppendNotePayloadUnconfiguredFallsBackInline(t *testing.T) {
	// 兼容语义：payloadDir 未配置（旧部署/部分单测）→ 全部内联，行为与改造前逐字一致。
	db := setupBlobDB(t)
	ConfigurePayloads("") // 显式未配置
	tx, _ := db.Begin()
	defer tx.Rollback()
	seq, _ := NextChangeSeq(tx)
	syncID := "sync-fallback"
	if err := AppendNotePayload(tx, seq, 1, &syncID, "UPDATE", 1, bigPayload(InlinePayloadLimit*2), ""); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := tx.QueryRow(`SELECT payload_kind FROM sync_changes WHERE change_seq = ?`, seq).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "json" {
		t.Fatalf("未配置 dir 时大载荷应回退内联（兼容），实得 %s", kind)
	}
}

func TestPayloadReferencedScopedToUser(t *testing.T) {
	db := setupBlobDB(t)
	setupBlobDir(t)
	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','user'),(2,'u2','x','user')`); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	seq, _ := NextChangeSeq(tx)
	syncID := "s1"
	ref := strings.Repeat("ab", 32)
	if _, err := tx.Exec(`INSERT INTO sync_changes (change_seq, user_id, entity_type, entity_sync_id, operation, server_version, payload_kind, payload_ref)
		VALUES (?, 1, 'NOTE', ?, 'UPDATE', 1, 'blob_ref', ?)`, seq, &syncID, ref); err != nil {
		t.Fatal(err)
	}
	tx.Commit()

	ok, err := PayloadReferenced(db, 1, ref)
	if err != nil || !ok {
		t.Fatalf("属主引用应为 true: ok=%v err=%v", ok, err)
	}
	ok, err = PayloadReferenced(db, 2, ref)
	if err != nil || ok {
		t.Fatalf("§35 跨用户引用必须为 false（不能只因知道 hash 就读取）: ok=%v err=%v", ok, err)
	}
	ok, _ = PayloadReferenced(db, 1, strings.Repeat("cd", 32))
	if ok {
		t.Fatal("无引用 hash 应为 false")
	}
}

func TestValidPayloadRef(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{strings.Repeat("a", 64), true},
		{strings.Repeat("A", 64), false}, // 大写：Windows 大小写不敏感 FS 上会与小写同路径
		{strings.Repeat("a", 63), false},
		{strings.Repeat("g", 64), false}, // 非 hex
		{"../../etc/passwd" + strings.Repeat("a", 47), false},
	}
	for _, c := range cases {
		if got := ValidPayloadRef(c.ref); got != c.want {
			t.Fatalf("ValidPayloadRef(%q) = %v, want %v", c.ref[:12]+"…", got, c.want)
		}
	}
}

func TestListPayloadRefs(t *testing.T) {
	db := setupBlobDB(t)
	setupBlobDir(t)
	tx, _ := db.Begin()
	seq1, _ := NextChangeSeq(tx)
	seq2, _ := NextChangeSeq(tx)
	r1, r2 := strings.Repeat("ef", 32), strings.Repeat("12", 32)
	for _, tc := range []struct {
		seq int64
		ref string
	}{{seq1, r1}, {seq2, r2}} {
		syncID := "s"
		if _, err := tx.Exec(`INSERT INTO sync_changes (change_seq, user_id, entity_type, entity_sync_id, operation, server_version, payload_kind, payload_ref)
			VALUES (?, 1, 'NOTE', ?, 'UPDATE', 1, 'blob_ref', ?)`, tc.seq, &syncID, tc.ref); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	refs, err := ListPayloadRefs(db)
	if err != nil {
		t.Fatal(err)
	}
	if !refs[r1] || !refs[r2] {
		t.Fatalf("引用集合应含两个 ref: %v", refs)
	}
}
