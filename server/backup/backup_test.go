package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEnvelopeRoundTripAndTamper(t *testing.T) {
	master := mustMaster(t)
	bid := "11111111-2222-3333-4444-555555555555"
	payload := bytes.Repeat([]byte("note-bytes\x00\xff"), 100)

	env, err := EnvelopeSeal(payload, bid, master, 0)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, gotID, err := EnvelopeOpen(env, master, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if gotID != bid || !bytes.Equal(got, payload) {
		t.Fatal("roundtrip mismatch")
	}

	// 错密钥 → 认证失败
	if _, _, err := EnvelopeOpen(env, mustMaster(t), 0); err == nil {
		t.Fatal("expected failure with wrong master key")
	}
	// 篡改密文任意一字节 → 认证失败
	bad := append([]byte(nil), env...)
	bad[len(bad)-1] ^= 0x01
	if _, _, err := EnvelopeOpen(bad, master, 0); err == nil {
		t.Fatal("expected auth failure on tampered ciphertext")
	}
	// 篡改 header 里的 backup_id（进 AAD）→ 认证失败
	idOff := 7
	bad2 := append([]byte(nil), env...)
	bad2[idOff] = '9'
	if _, _, err := EnvelopeOpen(bad2, master, 0); err == nil {
		t.Fatal("expected auth failure when backup_id (AAD) tampered")
	}
	// 篡改 enc 字段 → 拒绝（不"按 header 选解析路径"）
	bad3 := append([]byte(nil), env...)
	bad3[5] = envEncV2
	if _, _, err := EnvelopeOpen(bad3, master, 0); err == nil {
		t.Fatal("expected rejection when enc field tampered to v2")
	}
}

func TestEnvelopeCap(t *testing.T) {
	master := mustMaster(t)
	payload := make([]byte, 1000)
	if _, err := EnvelopeSeal(payload, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", master, 100); err == nil {
		t.Fatal("expected ErrBackupTooLarge when payload > cap")
	}
}

func TestArchiveZipSlipRejected(t *testing.T) {
	for _, bad := range []string{"../evil", "/abs", "uploads/a/b.png", "uploads/..", "weird\\x"} {
		if _, err := sanitizeArcName(bad); err == nil {
			t.Fatalf("sanitizeArcName(%q) should reject", bad)
		}
	}
	for _, ok := range []string{"manifest.json", "notepad.db", "jwt_secret.enc", "uploads/abc.png"} {
		if _, err := sanitizeArcName(ok); err != nil {
			t.Fatalf("sanitizeArcName(%q) should accept: %v", ok, err)
		}
	}
}

func TestMasterKeyInstanceIDLifecycle(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("key len %d", len(k1))
	}
	k2, err := LoadOrCreateMasterKey(dir)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatal("master key not stable across reload")
	}
	if MasterKeyKid(k1) != MasterKeyKid(k2) {
		t.Fatal("kid not deterministic")
	}
	id1, _ := LoadOrCreateInstanceID(dir)
	id2, _ := LoadOrCreateInstanceID(dir)
	if id1 != id2 || len(id1) != 36 {
		t.Fatalf("instance id unstable/wrong len: %q %q", id1, id2)
	}
	// 导出→导入往返，kid 自洽
	bundle := ExportBundle(k1, id1)
	mi, iid, err := ParseExportBundle(bundle)
	if err != nil || !bytes.Equal(mi, k1) || iid != id1 {
		t.Fatalf("parse export bundle: %v", err)
	}
}

func TestPendingIdentity(t *testing.T) {
	dir := t.TempDir()
	master := mustMaster(t)
	if err := WritePendingIdentity(dir, PendingIdentity{RestoreID: "r1", InstanceID: "i1", MasterKey: master}); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPendingIdentity(dir)
	if err != nil || p == nil || p.RestoreID != "r1" || !bytes.Equal(p.MasterKey, master) {
		t.Fatalf("load pending: %v %+v", err, p)
	}
	if err := ClearPendingIdentity(dir); err != nil {
		t.Fatal(err)
	}
	p2, err := LoadPendingIdentity(dir)
	if err != nil || p2 != nil {
		t.Fatal("pending should be cleared")
	}
	// 幂等删除
	if err := ClearPendingIdentity(dir); err != nil {
		t.Fatal(err)
	}
}

// ---- 端到端：真实 temp notepad.db → CreateBackup → 解开校验 ----

func TestCreateBackupEndToEnd(t *testing.T) {
	dataDir, uploadDir := seedInstance(t /*withAttachment*/, true)
	env := Env{DataDir: dataDir, UploadDir: uploadDir, AppVersion: "1.0.167", SchemaVer: 2}

	bid := "aaaaaaaa-0000-0000-0000-000000000001"
	res, err := CreateBackup(env, bid, false)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if res.Incomplete || len(res.MissingUploads) != 0 {
		t.Fatalf("unexpected incomplete: %+v", res.MissingUploads)
	}
	if res.Stats.NoteCount != 1 || res.Stats.UserCount < 1 {
		t.Fatalf("stats wrong: %+v", res.Stats)
	}
	// .snbak 能被同主密钥解开 → tar.gz → 含 manifest/notepad.db/jwt_secret.enc/uploads/<uuid>
	envBytes, err := os.ReadFile(res.SnbakPath)
	if err != nil {
		t.Fatal(err)
	}
	master, _ := ReadMasterKey(dataDir)
	payload, gotID, err := EnvelopeOpen(envBytes, master, 0)
	if err != nil || gotID != bid {
		t.Fatalf("open envelope: %v id=%s", err, gotID)
	}
	names := tarNames(t, payload)
	for _, want := range []string{"manifest.json", "notepad.db", "jwt_secret.enc", "uploads/"} {
		if !containsPrefix(names, want) {
			t.Fatalf("archive missing %q; has %v", want, names)
		}
	}
	// jwt_secret.enc 能还原出原 .jwt_secret
	jwtFileContent, _ := os.ReadFile(filepath.Join(dataDir, ".jwt_secret"))
	jwtFromArc := tarFile(t, payload, "jwt_secret.enc")
	un, err := UnwrapJWTSecret(jwtFromArc, master)
	if err != nil || strings.TrimSpace(string(un)) != strings.TrimSpace(string(jwtFileContent)) {
		t.Fatalf("jwt unwrap mismatch: %v", err)
	}
	// 内部 manifest：含 instance_id、不含 archive_sha256 字段（自引用消除）、file_hashes 覆盖 db
	manBytes := tarFile(t, payload, "manifest.json")
	man, err := ParseInternalManifest(manBytes)
	if err != nil {
		t.Fatal(err)
	}
	if man.InstanceID == "" || man.FileHashes["notepad.db"] == "" || len(man.Database.Sha256) != 64 {
		t.Fatalf("manifest incomplete: inst=%q hashes=%v", man.InstanceID, man.FileHashes)
	}
	// 解出的 notepad.db 应通过 integrity_check
	dbOut := tarFile(t, payload, "notepad.db")
	tmpdb := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(tmpdb, dbOut, 0600); err != nil {
		t.Fatal(err)
	}
	if err := IntegrityCheck(tmpdb); err != nil {
		t.Fatalf("restored db integrity: %v", err)
	}
	// VerifyArchive：整档自检（GCM+AAD+逐文件哈希+快照 integrity）应通过
	vm, err := VerifyArchive(env, res.SnbakPath)
	if err != nil {
		t.Fatalf("VerifyArchive: %v", err)
	}
	if vm.BackupID != bid || len(vm.FileHashes) == 0 {
		t.Fatalf("VerifyArchive manifest wrong: id=%s hashes=%d", vm.BackupID, len(vm.FileHashes))
	}
}

// TestVerifyRejectsUndeclaredFile（v21.133 GPT-07，审计中危）：
// 归档内若含 manifest 未声明的额外文件，VerifyArchive 必须拒绝——否则 placeIn 会把
// 攻击者可控备份包里未列入 manifest 的文件复制进正式 upload 目录（同源内容/占空间风险）。
// 反向验证：注入额外文件 → 校验必失败且错误串含 undeclared。
func TestVerifyRejectsUndeclaredFile(t *testing.T) {
	dataDir, uploadDir := seedInstance(t, true)
	env := Env{DataDir: dataDir, UploadDir: uploadDir, AppVersion: "1.0.167", SchemaVer: 2}
	bid := "aaaaaaaa-0000-0000-0000-000000000001"
	res, err := CreateBackup(env, bid, false)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	master, err := ReadMasterKey(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	envBytes, err := os.ReadFile(res.SnbakPath)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := EnvelopeOpen(envBytes, master, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 基线：合法归档应校验通过
	if _, err := VerifyArchive(env, res.SnbakPath); err != nil {
		t.Fatalf("合法归档 VerifyArchive 应通过，实得 %v", err)
	}
	// 篡改：追加一个 manifest 未声明的 uploads/evil.html，用同一主密钥重新封存
	tampered := tarWithExtraFile(t, payload, "uploads/evil.html", []byte("<script>alert(1)</script>"))
	sealed, err := EnvelopeSeal(tampered, bid, master, 0)
	if err != nil {
		t.Fatalf("EnvelopeSeal: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "evil.snbak")
	if err := os.WriteFile(badPath, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArchive(env, badPath); err == nil || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("含未声明额外文件的归档应被拒（GPT-07），实得 err=%v", err)
	}
}

// tarWithExtraFile 把 gzip 的 tar payload 解开、原样复制全部成员、追加一个额外文件后重新 gzip。
func tarWithExtraFile(t *testing.T, gz []byte, extraName string, extraBody []byte) []byte {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	hdr := &tar.Header{Name: extraName, Mode: 0600, Size: int64(len(extraBody)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(extraBody); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestCreateBackupMissingAttachmentFailClosed(t *testing.T) {
	dataDir, uploadDir := seedInstance(t, true)
	// 删掉被引用的附件文件 → 采集 fail-closed
	entries, _ := os.ReadDir(uploadDir)
	for _, e := range entries {
		os.Remove(filepath.Join(uploadDir, e.Name()))
	}
	env := Env{DataDir: dataDir, UploadDir: uploadDir, AppVersion: "1.0.167", SchemaVer: 2}

	if _, err := CreateBackup(env, "aaaaaaaa-0000-0000-0000-000000000002", false); err == nil {
		t.Fatal("expected ErrAttachmentMissing when referenced file gone")
	}
	// allowMissing=true → 降级 INCOMPLETE，不报错
	res, err := CreateBackup(env, "aaaaaaaa-0000-0000-0000-000000000003", true)
	if err != nil {
		t.Fatalf("allowMissing should not error: %v", err)
	}
	if !res.Incomplete || len(res.MissingUploads) == 0 {
		t.Fatalf("expected Incomplete with missing list, got %+v", res)
	}
}

func mustMaster(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	k, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// seedInstance 建一个临时"实例"：DataDir 含 notepad.db（users/notes/uploads/note_uploads 各一行，
// 附件 referenced）+ .jwt_secret；UploadDir 含该附件文件。withAttachment=false 时不建物理文件。
func seedInstance(t *testing.T, withAttachment bool) (dataDir, uploadDir string) {
	t.Helper()
	dataDir = t.TempDir()
	uploadDir = t.TempDir()
	dbPath := filepath.Join(dataDir, "notepad.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec(t, db, `CREATE TABLE users(id INTEGER PRIMARY KEY, username TEXT)`)
	mustExec(t, db, `CREATE TABLE notes(id INTEGER PRIMARY KEY, deleted_at DATETIME)`)
	mustExec(t, db, `CREATE TABLE uploads(filename TEXT PRIMARY KEY, status TEXT)`)
	mustExec(t, db, `CREATE TABLE note_uploads(note_id INTEGER, filename TEXT)`)
	mustExec(t, db, `INSERT INTO users VALUES(1,'admin')`)
	mustExec(t, db, `INSERT INTO notes VALUES(1,NULL)`)
	mustExec(t, db, `INSERT INTO uploads VALUES('pic.png','attached')`)
	mustExec(t, db, `INSERT INTO note_uploads VALUES(1,'pic.png')`)
	if withAttachment {
		if err := os.WriteFile(filepath.Join(uploadDir, "pic.png"), []byte("PNGDATA"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".jwt_secret"), []byte("abc123secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dataDir, uploadDir
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func tarNames(t *testing.T, gz []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name)
	}
	return out
}

func tarFile(t *testing.T, gz []byte, name string) []byte {
	t.Helper()
	zr, _ := gzip.NewReader(bytes.NewReader(gz))
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == name {
			b, _ := io.ReadAll(tr)
			return b
		}
	}
	t.Fatalf("tar member %q not found", name)
	return nil
}

func containsPrefix(names []string, want string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, want) {
			return true
		}
	}
	return false
}
