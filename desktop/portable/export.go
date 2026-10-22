package portable

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"stickynote/backup"

	"stickynote-desktop/localstore"
)

// SyncProtocolVersion 产包时写入 manifest §111（V1=1）。
const SyncProtocolVersion = 1

// ExportOptions 便携导出入参。ProfileDir = localstore 档案根（generations/runtime/staging），
// UploadDir = profileDir/uploads（附件目录，与 generation 切换解耦的现行布局）。
type ExportOptions struct {
	ProfileDir string
	UploadDir  string
	Password   string
	OutPath    string // .snbak 目标路径（原子 .part → rename）
	AppVersion string
	// MaxPayloadBytes 明文归档帽；<=0 → 256MB（v20 §109 / backup.DefaultArchiveCapBytes）。
	MaxPayloadBytes int64
}

// ExportResult 导出摘要（绑定面回显）。
type ExportResult struct {
	BackupID      string
	BackupSetID   string
	PayloadBytes  int64 // tar.gz 明文
	FileBytes     int64 // db+附件 明文件总和
	OutBytes      int64 // .snbak 落盘大小
	Attachments   int
	NoteCount     int
	OutboxPending int
	ConflictCount int
	Cursor        int64
	Epoch         int64
	InstanceID    string
	Username      string
}

// Export 便携导出全链（§106~§111）：active generation 快照 → 脱敏 → 引用集附件收集
// （缺失 fail-closed）→ manifest(backup_type=PORTABLE) → tar.gz → 口令派生 master →
// SNBK 信封 → SNBP 容器原子落盘。>帽 → 明确拒绝（预检 + EnvelopeSeal 双闸）。
// 全程只读 active 库文件（VACUUM INTO 独立连接），零破坏。
func Export(opts ExportOptions) (*ExportResult, error) {
	if len(opts.Password) < MinPasswordLen {
		return nil, ErrPasswordWeak
	}
	if opts.ProfileDir == "" || opts.OutPath == "" || opts.UploadDir == "" {
		return nil, errors.New("portable: export requires ProfileDir/UploadDir/OutPath")
	}
	maxBytes := opts.MaxPayloadBytes
	if maxBytes <= 0 {
		maxBytes = backup.DefaultArchiveCapBytes
	}
	lay := localstore.Layout{ProfileDir: opts.ProfileDir}
	if err := lay.EnsureDirs(); err != nil {
		return nil, err
	}
	active, err := lay.Active()
	if err != nil {
		return nil, err
	}
	if active == "" {
		return nil, ErrEmptyProfile
	}
	src := lay.GenDB(active)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("%w: active db missing: %v", ErrEmptyProfile, err)
	}
	backupID, err := uuid4()
	if err != nil {
		return nil, err
	}
	setID, err := uuid4()
	if err != nil {
		return nil, err
	}
	salt := make([]byte, pbkdfSaltLen)
	if _, err := randRead(salt); err != nil {
		return nil, fmt.Errorf("portable: salt: %w", err)
	}

	// 1) 一致性快照（VACUUM INTO，独立连接，不占运行池）
	snap := filepath.Join(lay.StagingDir(), "portable-"+backupID+".db")
	defer os.Remove(snap)
	if err := backup.SnapshotTo(snap, src); err != nil {
		return nil, fmt.Errorf("portable: snapshot: %w", err)
	}

	// 2) 快照副本脱敏 + 元数据读取（单连接串行；active 库零触碰）
	meta, err := sanitizeAndInspect(snap)
	if err != nil {
		return nil, err
	}

	// 3) 附件打包集 = 引用集 ∪ 目录全量；引用缺失 fail-closed（红线 17/25）。
	pkg, err := collectAttachments(opts.UploadDir, meta.refs)
	if err != nil {
		return nil, err
	}
	dbSha, dbSize, err := hashFile(snap)
	if err != nil {
		return nil, err
	}
	total := dbSize
	for _, p := range pkg {
		total += p.Size
	}
	if total > maxBytes {
		return nil, fmt.Errorf("%w: portable contents %d bytes > cap %d（V1 不分块，请精简附件）",
			backup.ErrBackupTooLarge, total, maxBytes)
	}

	// 4) manifest（FileHashes 逐文件；不含 jwt_secret）
	man := &backup.InternalManifest{
		FormatVersion:   1,
		BackupID:        backupID,
		BackupType:      backup.BackupTypePortable,
		BackupSetID:     setID,
		SyncProtocolVer: SyncProtocolVersion,
		InstanceID:      meta.instanceID,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		Trigger:         "manual",
		AppVersion:      opts.AppVersion,
		Enc:             1,
		ServerEpoch:     meta.epoch,
	}
	man.Database.Path = "notepad.db"
	man.Database.Sha256 = dbSha
	man.Database.Size = dbSize
	man.JWTSecretIncluded = false
	man.Attachments.Count = int64(len(pkg))
	man.Attachments.TotalBytes = total - dbSize
	man.FileHashes = map[string]string{"notepad.db": dbSha}
	var entries []backup.FileEntry
	for _, p := range pkg {
		man.Attachments.Files = append(man.Attachments.Files,
			backup.AttachmentEntry{Name: p.Name, Sha256: p.Sha256, Size: p.Size})
		man.FileHashes["uploads/"+p.Name] = p.Sha256
		entries = append(entries, backup.FileEntry{Name: p.Name, AbsPath: p.AbsPath})
	}
	man.Stats.UserCount = meta.userCount
	man.Stats.NoteCount = meta.noteCount
	man.Stats.AttachmentCount = len(pkg)
	manBytes, err := marshalManifest(man)
	if err != nil {
		return nil, err
	}

	// 5) tar.gz + SNBK 信封（master=PBKDF2(口令,salt)）+ SNBP 容器落盘
	payload, err := backup.BuildArchive(backup.ArchiveInput{
		ManifestBytes: manBytes, DBPath: snap, Uploads: entries,
	})
	if err != nil {
		return nil, fmt.Errorf("portable: build archive: %w", err)
	}
	master := deriveMaster(opts.Password, salt, pbkdfIter)
	envelope, err := backup.EnvelopeSeal(payload, backupID, master, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("portable: seal: %w", err)
	}
	if err := writeContainer(opts.OutPath, salt, pbkdfIter, envelope); err != nil {
		return nil, err
	}
	st, err := os.Stat(opts.OutPath)
	if err != nil {
		return nil, err
	}
	return &ExportResult{
		BackupID: backupID, BackupSetID: setID,
		PayloadBytes: int64(len(payload)), FileBytes: total, OutBytes: st.Size(),
		Attachments: len(pkg), NoteCount: meta.noteCount,
		OutboxPending: meta.outboxPending, ConflictCount: meta.conflictCount,
		Cursor: meta.cursor, Epoch: meta.epoch, InstanceID: meta.instanceID,
		Username: meta.username,
	}, nil
}

// ---- 快照脱敏 + 元数据读取（一次打开，串行完成） ----

type snapshotMeta struct {
	refs          []string // 权威引用集（note_uploads ∪ attached）
	userCount     int
	noteCount     int
	outboxPending int
	conflictCount int
	cursor        int64
	epoch         int64
	instanceID    string
	username      string
}

func sanitizeAndInspect(snapPath string) (*snapshotMeta, error) {
	db, err := sql.Open("sqlite", snapPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := sanitizeSnapshot(db); err != nil {
		return nil, err
	}
	m := &snapshotMeta{}
	_ = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&m.userCount)
	_ = db.QueryRow(`SELECT COUNT(*) FROM notes WHERE deleted_at IS NULL`).Scan(&m.noteCount)
	_ = db.QueryRow(`SELECT username FROM users ORDER BY id LIMIT 1`).Scan(&m.username)
	if n, err := countIfTable(db, "sync_outbox",
		`SELECT COUNT(*) FROM sync_outbox WHERE status IN ('PENDING','SENDING','FAILED','CONFLICT')`); err == nil {
		m.outboxPending = n
	}
	if n, err := countIfTable(db, "sync_conflicts", `SELECT COUNT(*) FROM sync_conflicts`); err == nil {
		m.conflictCount = n
	}
	for _, k := range []string{"cursor", "server_epoch", "server_instance_id"} {
		var v string
		if err := db.QueryRow(`SELECT value FROM sync_state WHERE key=?`, k).Scan(&v); err != nil {
			continue
		}
		switch k {
		case "cursor":
			m.cursor, _ = strconv.ParseInt(v, 10, 64)
		case "server_epoch":
			m.epoch, _ = strconv.ParseInt(v, 10, 64)
		case "server_instance_id":
			m.instanceID = v
		}
	}
	// 引用集（服务端引擎同口径；老库无 note_uploads 退回 attached 单路；两表皆无 = 空引用容忍，
	// 目录全量仍会进包，不存在"引用未知导致少打包"——fail-closed 只作用在"引用了但盘上缺"）
	rows, err := db.Query(`SELECT DISTINCT filename FROM note_uploads
		UNION SELECT filename FROM uploads WHERE status='attached'`)
	if err != nil {
		rows, err = db.Query(`SELECT filename FROM uploads WHERE status='attached'`)
	}
	if err != nil {
		return m, nil
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && n != "" {
			m.refs = append(m.refs, n)
		}
	}
	return m, rows.Err()
}

func countIfTable(db *sql.DB, table, q string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	if err != nil || n == 0 {
		return 0, errors.New("table missing")
	}
	if err := db.QueryRow(q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ---- 附件收集 ----

type packedFile struct {
	Name, AbsPath, Sha256 string
	Size                  int64
}

func collectAttachments(uploadDir string, refs []string) ([]packedFile, error) {
	onDisk := map[string]bool{}
	var names []string
	ents, err := os.ReadDir(uploadDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("portable: read uploads dir: %w", err)
	}
	for _, e := range ents {
		if e.IsDir() || !isSafeBasename(e.Name()) {
			continue // 目录残留/非受控名不进包
		}
		onDisk[e.Name()] = true
		names = append(names, e.Name())
	}
	// 引用完整性 fail-closed（红线 17/25）：引用了但盘上没有 = 拒绝导出，绝不静默少打包。
	refSet := map[string]bool{}
	for _, r := range refs {
		if !isSafeBasename(r) || !onDisk[r] {
			return nil, fmt.Errorf("%w: %s", ErrAttachmentsMissing, r)
		}
		refSet[r] = true
	}
	sort.Strings(names)
	var out []packedFile
	for _, n := range names {
		p := filepath.Join(uploadDir, n)
		sha, size, herr := hashFile(p)
		if herr != nil {
			if refSet[n] {
				return nil, herr // 引用文件读不了 = fail-closed
			}
			continue // 非引用残件读不了 = 跳过（不影响可恢复性）
		}
		out = append(out, packedFile{Name: n, AbsPath: p, Sha256: sha, Size: size})
	}
	return out, nil
}
