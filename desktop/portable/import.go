package portable

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"stickynote/backup"

	"stickynote-desktop/bootstrap"
	"stickynote-desktop/localstore"
	"stickynote-desktop/synclocal"
)

// ImportOptions 便携导入入参。InPath = .snbak 文件；UploadDir = 本机附件目录（合并目标）。
type ImportOptions struct {
	ProfileDir      string
	UploadDir       string
	InPath          string
	Password        string
	MaxPayloadBytes int64 // <=0 → 256MB（§109 同帽；导入侧同样不接超帽包）
}

// ImportResult 导入摘要（绑定面回显）。
type ImportResult struct {
	BackupID          string
	Generation        string
	AttachmentsMerged int
	Carried           int // 本机旧代并入的未推送 op 数（红线 1 对冲：导入不吞 B 的账）
	CarryConflict     int
	CarryDropped      int
	NoteCount         int
	OutboxPending     int
	Cursor            int64
	Epoch             int64
	InstanceID        string
	InstanceMatch     bool // B 已绑服务器与包来源一致（false 且 HadProfile=true → 跨服务器，须重绑）
	HadProfile        bool // 本机导入前已有设备档案
	ReseededProfile   bool // 已把本机档案重植进新代
}

// Import 便携导入全链（§107/§108/§148；红线 25/26/27）：
//
//	解容器 → PBKDF2(口令) → EnvelopeOpen → 解包（防穿越/资源帽）→ manifest 权威校验
//	（backup_type=PORTABLE / backup_id 绑定 / 文件集双向相等 / 逐文件 sha256 / 声明尺寸帽）
//	→ integrity_check → 引用附件复检 → Activator 双代激活（lkg 先写，崩溃由 Recover 对账）
//	→ 附件只增合并 → 新代 EnsureTables + 本机 client_profile 重植 + 本机未推送 Outbox 并入。
//
// Activate 之前任何失败 = 本机零破坏（全部在 staging 解包区）。跨服务器包自然命中
// worker 锚校验停推（红线 10），绑定面按 InstanceMatch=false 引导重绑，绝不自动同步（§108）。
func Import(opts ImportOptions) (*ImportResult, error) {
	if opts.ProfileDir == "" || opts.UploadDir == "" || opts.InPath == "" {
		return nil, errors.New("portable: import requires ProfileDir/UploadDir/InPath")
	}
	maxBytes := opts.MaxPayloadBytes
	if maxBytes <= 0 {
		maxBytes = backup.DefaultArchiveCapBytes
	}
	lay := localstore.Layout{ProfileDir: opts.ProfileDir}
	if err := lay.EnsureDirs(); err != nil {
		return nil, err
	}

	// 1) 容器 + 信封（口令错误 → ErrDecryptFailed，无预言机、零副作用）
	salt, iter, envelope, err := readContainer(opts.InPath, maxBytes+1<<20)
	if err != nil {
		return nil, err
	}
	master := deriveMaster(opts.Password, salt, iter)
	payload, envBackupID, err := backup.EnvelopeOpen(envelope, master, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("portable: open: %w", err)
	}

	// 2) 解包 staging（ExtractArchive 自带 zip-slip 白名单 + 解压资源帽）
	exDir := filepath.Join(lay.StagingDir(), "portable-in-"+envBackupID)
	if err := os.RemoveAll(exDir); err != nil {
		return nil, err
	}
	defer os.RemoveAll(exDir)
	extracted, err := backup.ExtractArchive(payload, exDir)
	if err != nil {
		return nil, fmt.Errorf("portable: extract: %w", err)
	}
	manRaw, err := os.ReadFile(filepath.Join(exDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: manifest.json missing", ErrArchiveMismatch)
	}
	man, err := backup.ParseInternalManifest(manRaw)
	if err != nil {
		return nil, err
	}

	// 3) 权威校验：类型 / backup_id 双绑 / 文件集双向相等 + 逐文件哈希 / 声明帽复检
	if man.BackupType != backup.BackupTypePortable {
		return nil, ErrNotPortable
	}
	if man.BackupID != envBackupID {
		return nil, fmt.Errorf("%w: manifest backup_id != envelope", ErrArchiveMismatch)
	}
	if len(extracted) != len(man.FileHashes)+1 { // +manifest.json
		return nil, fmt.Errorf("%w: entry count mismatch (%d vs %d+1)", ErrArchiveMismatch, len(extracted), len(man.FileHashes))
	}
	for rel, want := range man.FileHashes {
		got, ok := extracted[rel]
		if !ok || got != want {
			return nil, fmt.Errorf("%w: %s missing or sha256 mismatch", ErrArchiveMismatch, rel)
		}
	}
	// 反向集合校验：包内不得有 manifest 未声明的成员（夹带 jwt_secret.enc/他人文件的包即拒）
	for rel := range extracted {
		if rel == "manifest.json" {
			continue
		}
		if _, ok := man.FileHashes[rel]; !ok {
			return nil, fmt.Errorf("%w: undeclared member %s", ErrArchiveMismatch, rel)
		}
	}
	var declared int64 = man.Database.Size + man.Attachments.TotalBytes
	if man.Database.Path != "notepad.db" || declared > maxBytes {
		return nil, fmt.Errorf("%w: declared size %d or db path invalid", ErrArchiveMismatch, declared)
	}

	// 4) 冻结库完整性（早失败）+ 引用附件复检（fail-closed，红线 25）
	stagingDB := filepath.Join(exDir, "notepad.db")
	if err := localstore.IntegrityCheckFile(stagingDB); err != nil {
		return nil, err
	}
	if err := verifyRefsAgainstPkg(stagingDB, filepath.Join(exDir, "uploads")); err != nil {
		return nil, err
	}

	// 5) 本机现状捕获（红线 1 对冲：B 的未推送账与设备身份不得被导入吞掉）
	res := &ImportResult{BackupID: envBackupID}
	var oldDB *sql.DB
	if cur, aerr := lay.Active(); aerr == nil && cur != "" {
		if p := lay.GenDB(cur); fileExists(p) {
			if odb, oerr := localstore.OpenGenDB(p); oerr == nil {
				oldDB = odb
				defer oldDB.Close()
				res.HadProfile = true
				if inst, _ := syncStateValue(oldDB, "server_instance_id"); inst != "" {
					res.InstanceMatch = inst == man.InstanceID && inst != ""
				}
			}
		}
	}

	// 6) 双代激活（lkg 先写 + 四相位 journal；manifest.json 原样进代目录作 provenance）
	//
	// v21.203：与 bootstrap 侧同纪律 —— 记录 prev + 封相（SealCommitted）语义完全对称。
	// 导入侧的后置步骤（附件合并 / 档案重植 / outbox 并入）失败时同样必须回退到旧代
	// 并删除残缺新代，否则「导入失败」= 用户数据被一份半残的新代替换。
	prevGen, err := lay.Active()
	if err != nil {
		return nil, err
	}
	gen := ""
	activated, committed := false, false
	defer func() {
		if committed {
			return
		}
		if activated {
			if rerr := lay.RollbackActiveDiscard(prevGen, gen); rerr != nil {
				log.Printf("[portable] ⚠ 导入后置步骤失败且指针回退失败（需人工介入）：prev=%s err=%v", prevGen, rerr)
			} else {
				log.Printf("[portable] 导入后置步骤失败，已回退 active → %s 并删除残缺代 %s", prevGen, gen)
			}
		}
		lay.GC()
	}()

	act := localstore.NewActivator(opts.ProfileDir)
	g, aerr := act.Activate(stagingDB, manRaw)
	if aerr != nil {
		return nil, fmt.Errorf("portable: activate: %w", aerr)
	}
	gen, activated = g, true
	res.Generation = gen

	// 7) 附件只增合并（同名同内容跳过；同名异内容留给 sha256 自愈轮——bootstrap 同语义）
	merged, err := bootstrap.MergeAttachments(filepath.Join(exDir, "uploads"), opts.UploadDir)
	if err != nil {
		return nil, fmt.Errorf("portable: merge attachments: %w", err)
	}
	res.AttachmentsMerged = merged

	// 8) 新代收尾：兜底建表 → 本机档案重植 → 本机未推送 Outbox 并入（CarryOverOutbox 单一实现）
	newDB, err := localstore.OpenGenDB(lay.GenDB(gen))
	if err != nil {
		return nil, err
	}
	defer newDB.Close()
	if err := synclocal.EnsureTables(newDB); err != nil {
		return nil, err
	}
	if oldDB != nil {
		if reseeded, rerr := reseedClientProfile(oldDB, newDB); rerr != nil {
			return nil, rerr
		} else {
			res.ReseededProfile = reseeded
		}
		c, cf, dp, cerr := bootstrap.CarryOverOutbox(oldDB, newDB)
		if cerr != nil {
			return nil, fmt.Errorf("portable: carry outbox: %w", cerr)
		}
		res.Carried, res.CarryConflict, res.CarryDropped = c, cf, dp
	}
	// 结果读数
	if n, err := countIfTable(newDB, "notes", `SELECT COUNT(*) FROM notes WHERE deleted_at IS NULL`); err == nil {
		res.NoteCount = n
	}
	if n, err := countIfTable(newDB, "sync_outbox",
		`SELECT COUNT(*) FROM sync_outbox WHERE status IN ('PENDING','SENDING','FAILED','CONFLICT')`); err == nil {
		res.OutboxPending = n
	}
	if v, _ := syncStateValue(newDB, "cursor"); v != "" {
		res.Cursor, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, _ := syncStateValue(newDB, "server_epoch"); v != "" {
		res.Epoch, _ = strconv.ParseInt(v, 10, 64)
	}
	res.InstanceID = man.InstanceID
	// v21.203 封相：全部后置步骤成功后落 COMMITTED（与 bootstrap 侧同一纪律）。
	if err := act.SealCommitted(gen); err != nil {
		return nil, fmt.Errorf("portable: seal: %w", err)
	}
	committed = true
	lay.GC()
	return res, nil
}

// verifyRefsAgainstPkg 冻结库引用集必须全部在包内（导出已 fail-closed，这里是双保险：
// 篡改/半包/未来 writer 漏打包都在 Activate 前被拒）。
func verifyRefsAgainstPkg(dbPath, uploadsDir string) error {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT DISTINCT filename FROM note_uploads
		UNION SELECT filename FROM uploads WHERE status='attached'`)
	if err != nil {
		rows, err = db.Query(`SELECT filename FROM uploads WHERE status='attached'`)
	}
	if err != nil {
		return nil // 两表皆无（极老快照）→ 无附件面，放行
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) != nil || n == "" {
			continue
		}
		if !isSafeBasename(n) {
			return fmt.Errorf("%w: unsafe ref %q", ErrArchiveMismatch, n)
		}
		if !fileExists(filepath.Join(uploadsDir, n)) {
			return fmt.Errorf("%w: %s", ErrAttachmentsMissing, n)
		}
	}
	return rows.Err()
}

// reseedClientProfile 把本机旧代 client_profile 单行整行复制进新代（DPAPI 封存件按本机
// 用户域可解，身份/凭据不出机器；导出的 A 档案已在导出侧剔除）。列集取两侧交集并剔除 id，
// 跨版本容忍；两侧任一缺表 = no-op。
func reseedClientProfile(oldDB, newDB *sql.DB) (bool, error) {
	oldCols, err := tableColumns(oldDB, "client_profile")
	if err != nil || len(oldCols) == 0 {
		return false, err
	}
	newCols, err := tableColumns(newDB, "client_profile")
	if err != nil || len(newCols) == 0 {
		return false, err
	}
	var cols []string
	for _, c := range oldCols {
		if c != "id" && contains(newCols, c) {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return false, nil
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := oldDB.QueryRow(`SELECT ` + strings.Join(cols, ",") + ` FROM client_profile WHERE id=1`).Scan(ptrs...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("portable: read old profile: %w", err)
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	ins := `INSERT OR REPLACE INTO client_profile (id,` + strings.Join(cols, ",") + `) VALUES(1,` + ph + `)`
	if _, err := newDB.Exec(ins, vals...); err != nil {
		return false, fmt.Errorf("portable: reseed profile: %w", err)
	}
	return true, nil
}

func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func syncStateValue(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM sync_state WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
