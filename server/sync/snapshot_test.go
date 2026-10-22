package sync

// snapshot_test.go —— 批次 5 · 刀 1：Bootstrap 快照服务契约测试。
// 钉：§75 watermark 语义（T1 读数 ≥ 冻结点）、§73 附件 Hold、§46/§48 TTL/孤儿扫描、
// §47 并发帽（复用 + 全局拒 + Claim 原子领取双保险）、投影（多用户 → 单用户 user_id=1）。

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stickynote/changelog"
	"stickynote/database"
	"stickynote/model"

	_ "modernc.org/sqlite"
)

func openReadOnlyDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)")
}

func setupSnapEnv(t *testing.T) (uploadDir string) {
	t.Helper()
	setupSyncDB(t) // users 1/2 + Configure(dataDir) + gin 路由
	base := t.TempDir()
	uploadDir = filepath.Join(base, "upload")
	if err := os.MkdirAll(uploadDir, 0700); err != nil {
		t.Fatal(err)
	}
	ConfigureSnapshot(filepath.Join(base, "data"), uploadDir)
	return uploadDir
}

func newSnapRow(t *testing.T, userID int64, status string, expires time.Time) string {
	t.Helper()
	id := newSnapID()
	if _, err := database.DB.Exec(`INSERT INTO sync_snapshots (id, user_id, status, expires_at) VALUES (?,?,?,?)`,
		id, userID, status, expires.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	return id
}

// pushNoteForUser 以指定用户产一条 CREATE 事件（复用 changelog 原语，等价 Web 写路径）。
func pushNoteForUser(t *testing.T, userID int64, syncID, title string) {
	t.Helper()
	tx, err := database.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	seq, err := changelog.NextChangeSeq(tx)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO notes (user_id, title, content, content_text, sync_id, version) VALUES (?,?,'<p>c</p>','',?,1)`,
		userID, title, syncID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"sync_id": syncID, "title": title, "content": "<p>c</p>"})
	if err := changelog.AppendChange(tx, seq, userID, "NOTE", &syncID, "CREATE", 1, string(payload), "d1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func currentSeq(t *testing.T) int64 {
	t.Helper()
	var v int64
	if err := database.DB.QueryRow(`SELECT current_change_seq FROM sync_meta WHERE id=1`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestInvalidateAllActive(t *testing.T) {
	setupSnapEnv(t)
	// 本测试置于文件最前（Go 按源码序执行）：后续 API 测试会泄漏受控后台构建，
	// 任何清场补丁都存在插行竞态——源头隔离才是正解。
	ready := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
	if err := BuildSnapshotSync(ready, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	s, _ := model.GetSyncSnapshot(ready)
	pending := newSnapRow(t, 2, model.SnapshotPending, time.Now().Add(time.Hour))
	files, err := model.InvalidateAllActiveSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("must invalidate both active rows (ready+pending): %+v", files)
	}
	byID := map[string]string{}
	for _, r := range files {
		byID[r.ID] = r.FilePath
	}
	if filepath.Base(byID[ready]) != filepath.Base(s.FilePath) || byID[pending] != "" {
		t.Fatalf("refs mismatch: %+v", files)
	}
	var st1, st2 string
	database.DB.QueryRow(`SELECT status FROM sync_snapshots WHERE id=?`, ready).Scan(&st1)
	database.DB.QueryRow(`SELECT status FROM sync_snapshots WHERE id=?`, pending).Scan(&st2)
	if st1 != model.SnapshotInvalidated || st2 != model.SnapshotInvalidated {
		t.Fatalf("statuses: %s %s", st1, st2)
	}
	// 幂等：再跑一次零行。
	again, _ := model.InvalidateAllActiveSnapshots()
	if len(again) != 0 {
		t.Fatalf("second invalidate must be empty, got %v", again)
	}
}

func TestSnapshotBuildHappyPath(t *testing.T) {
	setupSnapEnv(t)
	pushNoteForUser(t, 1, "aabbccddeeff00112233445566778899", "keep-me")
	snapID := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
	if err := BuildSnapshotSync(snapID, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	s, err := model.GetSyncSnapshot(snapID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != model.SnapshotReady {
		t.Fatalf("status=%s err=%s", s.Status, s.Error)
	}
	// §75/§140：watermark = 构建时 T1 读数；断言覆盖冻结点且为当前值。
	if s.Watermark != currentSeq(t) || s.Watermark < 1 {
		t.Fatalf("watermark=%d current=%d", s.Watermark, currentSeq(t))
	}
	if s.ServerEpoch < 1 || s.Sha256 == "" || len(s.Sha256) != 64 {
		t.Fatalf("anchor fields: %+v", s)
	}
	// zip 三件套齐备。
	zr, err := zip.OpenReader(s.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"notes.db", "meta.json"} {
		if !names[want] {
			t.Fatalf("zip missing %v: %v", want, names)
		}
	}
	var meta SnapshotMeta
	if mf, ok := names["meta.json"]; ok {
		_ = mf
		f, _ := zr.Open("meta.json")
		b, _ := io.ReadAll(f)
		f.Close()
		if err := json.Unmarshal(b, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Watermark != s.Watermark || meta.ServerEpoch != s.ServerEpoch || meta.UserID != 1 || meta.SourceUserID != 1 {
			t.Fatalf("meta mismatch: %+v", meta)
		}
	}
}

func TestSnapshotProjectionMultiUser(t *testing.T) {
	setupSnapEnv(t)
	pushNoteForUser(t, 1, "1111111122223333444455556666777a", "alice-note")
	pushNoteForUser(t, 2, "aaaabbccddeeff0011223344556677ff", "bob-note")
	snapID := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
	if err := BuildSnapshotSync(snapID, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	s, _ := model.GetSyncSnapshot(snapID)
	zr, err := zip.OpenReader(s.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	tmp := filepath.Join(t.TempDir(), "proj.db")
	f, err := zr.Open("notes.db")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.Create(tmp)
	_, _ = io.Copy(out, f)
	f.Close()
	out.Close()
	db, err := openReadOnlyDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var titles []string
	rows, _ := db.Query(`SELECT user_id, title FROM notes ORDER BY user_id, title`)
	for rows.Next() {
		var uid int64
		var title string
		rows.Scan(&uid, &title)
		if uid != 1 {
			t.Fatalf("projected row with user_id=%d leaked", uid)
		}
		titles = append(titles, title)
	}
	rows.Close()
	if len(titles) != 1 || titles[0] != "alice-note" {
		t.Fatalf("projection kept wrong notes: %v", titles)
	}
	var users int
	db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	if users != 1 {
		t.Fatalf("users must project to single id=1, got %d", users)
	}
}

func TestSnapshotHoldBlocksGC(t *testing.T) {
	upload := setupSnapEnv(t)
	fname := "11112222-3333-4444-5555-666677778888.png"
	if err := os.WriteFile(filepath.Join(upload, fname), []byte("img"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	if _, err := database.DB.Exec(`INSERT INTO uploads (filename, user_id, size, status, created_at, status_changed_at)
		VALUES (?,?,3,'staged',?,?)`, fname, 1, old, old); err != nil {
		t.Fatal(err)
	}
	pushNoteForUser(t, 1, "cafe00001111222233334444555566aa", "with-img")
	if _, err := database.DB.Exec(`INSERT INTO note_uploads (note_id, filename)
		SELECT id, ? FROM notes WHERE user_id=1 AND title='with-img'`, fname); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`UPDATE uploads SET status='attached' WHERE filename=?`, fname); err != nil {
		t.Fatal(err)
	}
	snapID := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
	if err := BuildSnapshotSync(snapID, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	s, _ := model.GetSyncSnapshot(snapID)
	if s.FileCount != 1 {
		t.Fatalf("zip should carry 1 attachment, got %d", s.FileCount)
	}
	// 附件改 staged 且超宽限 → GC 必须因 Hold 跳过（§73）。
	if _, err := database.DB.Exec(`UPDATE uploads SET status='staged', created_at='2000-01-01 00:00:00' WHERE filename=?`, fname); err != nil {
		t.Fatal(err)
	}
	if n := database.CleanStagedUploads(upload); n != 0 {
		t.Fatalf("held staged upload must survive GC, cleaned=%d", n)
	}
	if _, err := os.Stat(filepath.Join(upload, fname)); err != nil {
		t.Fatal("held file deleted despite hold")
	}
	// Hold 语义=整段跳过：连"转 attached"状态流转也不得发生（刀 1 坐实）。
	var st string
	if err := database.DB.QueryRow(`SELECT status FROM uploads WHERE filename=?`, fname).Scan(&st); err != nil || st != "staged" {
		t.Fatalf("held row must stay staged untouched, got %q (%v)", st, err)
	}
	// 过期扫描后 Hold 释放 → 下一轮 GC 正常回收。
	if _, err := database.DB.Exec(`UPDATE sync_snapshots SET expires_at=? WHERE id=?`,
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), snapID); err != nil {
		t.Fatal(err)
	}
	if ne, _ := ScanSnapshots(); ne != 1 {
		t.Fatalf("scan must expire 1, got %d", ne)
	}
	// 解除引用（模拟笔记删除）后再跑：无 Hold 且无引用 → 正常回收。
	if _, err := database.DB.Exec(`DELETE FROM note_uploads WHERE filename=?`, fname); err != nil {
		t.Fatal(err)
	}
	if n := database.CleanStagedUploads(upload); n != 1 {
		var st, ca string
		database.DB.QueryRow(`SELECT status, created_at FROM uploads WHERE filename=?`, fname).Scan(&st, &ca)
		var nh int
		database.DB.QueryRow(`SELECT COUNT(*) FROM sync_snapshots_hold WHERE filename=?`, fname).Scan(&nh)
		var sstat string
		database.DB.QueryRow(`SELECT status FROM sync_snapshots WHERE id=?`, snapID).Scan(&sstat)
		t.Fatalf("after expiry+detach GC must reclaim, cleaned=%d | upload=%s/%s holds=%d snap=%s", n, st, ca, nh, sstat)
	}
}

func TestSnapshotTTLAndOrphanSweep(t *testing.T) {
	setupSnapEnv(t)
	snapID := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(-time.Minute))
	if err := BuildSnapshotSync(snapID, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	s, _ := model.GetSyncSnapshot(snapID)
	if _, err := os.Stat(s.FilePath); err != nil {
		t.Fatal(err)
	}
	if ne, _ := ScanSnapshots(); ne != 1 {
		t.Fatalf("expired snapshot must be swept, got %d", ne)
	}
	if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
		t.Fatal("expired zip must be deleted")
	}
	// 孤儿 zip（账本无引用）同轮回收。
	orphan := filepath.Join(snapDir, "snap-deadbeef.zip")
	if err := os.WriteFile(orphan, []byte("PK"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, nf := ScanSnapshots(); nf != 1 {
		t.Fatalf("orphan zip must be swept, got %d", nf)
	}
}

func TestSnapshotCapsAndReuse(t *testing.T) {
	setupSnapEnv(t)
	// 用户在途 PENDING → Request 复用同一快照（不排第二队）。
	pending := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
	got, err := RequestSnapshot(1, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != pending {
		t.Fatalf("must reuse in-flight snapshot %s, got %s", pending, got.ID)
	}
	// 全局 BUILDING 达帽 → ErrSnapGlobalCap（用户 2 无在途也应被全局帽拒）。
	if _, err := database.DB.Exec(`UPDATE sync_snapshots SET status='BUILDING' WHERE id=?`, pending); err != nil {
		t.Fatal(err)
	}
	other := newSnapRow(t, 1, model.SnapshotBuilding, time.Now().Add(time.Hour))
	_ = other
	if _, err := RequestSnapshot(2, "u2"); err != ErrSnapGlobalCap {
		t.Fatalf("want ErrSnapGlobalCap, got %v", err)
	}
}

func TestClaimAtomicSingleWinner(t *testing.T) {
	setupSnapEnv(t)
	id := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
	ok1, err := model.ClaimSnapshotForBuild(id)
	if err != nil || !ok1 {
		t.Fatalf("first claim must win: %v %v", ok1, err)
	}
	ok2, err := model.ClaimSnapshotForBuild(id)
	if err != nil || ok2 {
		t.Fatalf("second claim must lose: %v %v", ok2, err)
	}
}

func TestSnapshotPathGuard(t *testing.T) {
	setupSnapEnv(t)
	s := &model.SyncSnapshot{Status: model.SnapshotReady, FilePath: filepath.Join(snapDir, "snap-x.zip")}
	if _, err := SnapshotPathForDownload(s); err == nil {
		t.Fatal("missing file must error")
	}
	s.FilePath = filepath.Join(t.TempDir(), "..", "evil.zip")
	if _, err := SnapshotPathForDownload(s); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("path escape must be rejected, got %v", err)
	}
	s.FilePath = ""
	if _, err := SnapshotPathForDownload(&model.SyncSnapshot{Status: model.SnapshotBuilding, FilePath: "x"}); err == nil {
		t.Fatal("non-READY must error")
	}
}

// TestReclaimStaleSnapshots v21.194 S-1：超龄在途行判死 + 半成品清理；新行不受影响。
// 背景（真机实证）：fnOS/TRIM 卷上构建 goroutine 可无限期卡死（staging 落盘后零推进），
// 卡死行永久占住每用户生成帽 → 该用户快照管线永久失效，重启都清不掉。
func TestReclaimStaleSnapshots(t *testing.T) {
	setupSnapEnv(t)
	stale := newSnapRow(t, 1, model.SnapshotBuilding, time.Now().Add(time.Hour))
	fresh := newSnapRow(t, 1, model.SnapshotPending, time.Now().Add(time.Hour))
	// 把 stale 的 created_at 拨回 11 分钟前（默认 CURRENT_TIMESTAMP=现在）
	if _, err := database.DB.Exec(`UPDATE sync_snapshots SET created_at = ? WHERE id = ?`,
		time.Now().Add(-11*time.Minute).UTC().Format(time.RFC3339), stale); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(snapDir, stale+".zip")
	stagingFile := stagingPath(stale)
	if err := os.MkdirAll(filepath.Dir(zipPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(stagingFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, []byte("PK"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagingFile, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}

	if n := reclaimStaleSnapshotsAt(time.Now().Add(-snapshotStaleAfter)); n != 1 {
		t.Fatalf("应恰好判死 1 行（超龄），got %d", n)
	}
	s, _ := model.GetSyncSnapshot(stale)
	if s.Status != model.SnapshotFailed {
		t.Fatalf("超龄行应标 FAILED，got %s", s.Status)
	}
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Fatal("僵死 zip 半成品应被清理")
	}
	if _, err := os.Stat(stagingFile); !os.IsNotExist(err) {
		t.Fatal("僵死 staging 应被清理")
	}
	f2, _ := model.GetSyncSnapshot(fresh)
	if f2.Status != model.SnapshotPending {
		t.Fatalf("新在途行不得被误伤，got %s", f2.Status)
	}
}

// TestFailSnapshotIfActiveCas v21.194 S-1：终态行不可判死（看门狗与正常完成双向幂等的另一半）。
func TestFailSnapshotIfActiveCas(t *testing.T) {
	setupSnapEnv(t)
	ready := newSnapRow(t, 1, model.SnapshotReady, time.Now().Add(time.Hour))
	if _, err := model.FailSnapshotIfActive(ready, "should not apply"); err != nil {
		t.Fatal(err)
	}
	s, _ := model.GetSyncSnapshot(ready)
	if s.Status != model.SnapshotReady {
		t.Fatalf("READY 行不得被 FailSnapshotIfActive 改写，got %s", s.Status)
	}
	bld := newSnapRow(t, 1, model.SnapshotBuilding, time.Now().Add(time.Hour))
	ok, err := model.FailSnapshotIfActive(bld, "watchdog")
	if err != nil || !ok {
		t.Fatalf("BUILDING 行应可判死 ok=%v err=%v", ok, err)
	}
	s2, _ := model.GetSyncSnapshot(bld)
	if s2.Status != model.SnapshotFailed || s2.Error != "watchdog" {
		t.Fatalf("判死结果不符: %s %q", s2.Status, s2.Error)
	}
}
