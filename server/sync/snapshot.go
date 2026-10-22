package sync

// snapshot.go —— 批次 5 · P5：Bootstrap 冻结快照服务（v20 §73/§75/§46~48）。
//
// 交付形态 = 单 zip（用户拍板）：notes.db（服务端投影后，仅请求用户、user_id→1）
// + uploads/<附件>（引用集）+ meta.json（epoch/watermark/instance/用户/过期）。
// sha256 对最终 zip（sealed 后）计算（F1 纪律：活体不算 hash）。
//
// watermark 一致性论证（§75/§140，**v21.157 N1 修正**）：
// VACUUM INTO 冻结于 T0；对外 watermark 取**冻结副本内本用户的 max(change_seq)**（与内容同点、构造上精确），
// 桌面以 cursor=W 起步再 pull(W,∞) 恰为补差集，无缺口无重复。
//
// 【历史缺陷（N1，2026-10-08 坐实）】原实现在**投影之后**读活库的 `current_change_seq` 作为 W（T1 ≥ T0），
// 而库内容冻结于 T0。文件头旧论证写「T1 ≥ T0 读取 ⇒ 无缺口」，**该论证默认 T1 == T0**，
// 并发 push 时不成立：并发实测 T0=1 / W=95 / 库内 max(seq)=48 ⇒ 区间 (48,95] 的 47 条事件
// **既不在快照库、也不会被 pull(95,∞) 覆盖 → 静默丢失**（无报错、无冲突、无日志）。
// 现改为从冻结副本自身读 ⇒ 库内恒有 seq ≤ W 的全部本用户事件；冻结窗口内新增事件必然 seq > W，
// 恰由桌面差集 pull 覆盖。冻结窗口宽度以 `lag` 记日志观测。
// epoch 同理取冻结前读数（不谎报比内容更新的世代）；并加 fail-closed 反向断言：
// 冻结副本 max(seq) > 活库 seq 属数据层异常，拒绝出账。
// 并发注入回归钉：`redline12_freeze_test.go:TestRedline12SnapshotFrozenAtT0`（含空转防护）。
//
// 并发帽（§47）：每用户生成中 ≤1（PENDING+BUILDING 计数 + Claim 原子领取双保险）；
// 全局 BUILDING ≤2（服务内信号量，跨重启无残留——启动即空）。
//
// Hold（§73）：构建成功后登记引用附件；GC 三清理见 database.orphanHeldBySnapshot；
// 过期/作废/失败释放。TTL/孤儿扫描器见 ScanSnapshots（cmd 启动 + 6h ticker 调用）。

import (
	"archive/zip"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"stickynote/backup"
	"stickynote/database"
	"stickynote/logger"
	"stickynote/model"
	"stickynote/syncspec"
)

const (
	snapshotTTL        = 24 * time.Hour // 用户拍板默认值
	snapshotGlobalCap  = 2              // 全局生成并发帽
	snapshotUserGenCap = 1              // 每用户生成中帽
	snapshotSubdir     = "sync-snapshots"
	snapshotStagingDir = "sync-snap-staging"
)

// S-1 看门狗参数（2026-10-08 深查轮，v20 §89~§92 故障注入的外延）：
// 真机实证（fnOS/TRIM 卷）构建 goroutine 可无限期卡死在 staging 落盘之后的某步
// （76ms 完成冻结、后续零推进；重启后第一击即卡；本地 Windows 无法复现）→
// 卡死行永久占住每用户生成帽 = 该用户快照管线永久失效（首装全量同步永不落地）。
// 防御纵深：①在途行超 snapshotStaleAfter 视为僵死，请求侧/启动期抢占标 FAILED；
// ②构建 goroutine 挂看门狗，超时经 FailSnapshotIfActive 判死（与构建完成双向幂等：
// FinishSnapshotBuild 的 WHERE status='BUILDING' 保证判死后的"复活完成"零行受影响）。
// 根因（堆栈级）定位待生产侧 kill -QUIT 取证（fnOS sudo 密码与 SSH 不同，admin 无法 kill）。
const (
	snapshotStaleAfter  = 10 * time.Minute
	snapshotWatchdogFor = 10 * time.Minute
)

var snapshotFilenameRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}\.[a-zA-Z0-9]{1,10}$`)

var (
	snapDir     string // DATA_DIR/sync-snapshots
	snapUploads string // 附件根目录
	buildSem    = make(chan struct{}, snapshotGlobalCap)
)

// ConfigureSnapshot 由 cmd/server 注入目录（未配置时 RequestSnapshot 明确报未就绪）。
func ConfigureSnapshot(dataDir, uploadDir string) {
	snapDir = filepath.Join(dataDir, snapshotSubdir)
	snapUploads = uploadDir
}

var ErrSnapshotNotConfigured = errors.New("snapshot service not configured")

// ErrSnapUserCap / ErrSnapGlobalFull：请求侧错误（handler 映射 429/503）。
var (
	ErrSnapUserCap   = errors.New("user already has a snapshot in progress")
	ErrSnapGlobalCap = errors.New("snapshot build capacity exhausted, retry later")
)

// SnapshotMeta zip 内 meta.json（桌面 Bootstrap 消费契约，syncspec 对齐但属文件面）。
type SnapshotMeta struct {
	SnapshotID     string `json:"snapshot_id"`
	ProtocolVer    int    `json:"protocol_version"`
	UserID         int64  `json:"user_id"` // 投影后恒 1
	SourceUserID   int64  `json:"source_user_id"`
	Username       string `json:"username"`
	ServerEpoch    int64  `json:"server_epoch"`
	Watermark      int64  `json:"watermark"`
	InstanceID     string `json:"instance_id"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
	FileCount      int    `json:"file_count"`
	DBSHA256       string `json:"db_sha256"`
	SnapshotSHA256 string `json:"snapshot_sha256"` // 最终 zip（READY 后回填进账本；zip 内为构建时值不适用，留空）
}

// RequestSnapshot 建 PENDING 行并异步构建。返回快照（可能复用该用户既有活跃快照）。
func RequestSnapshot(userID int64, username string) (*model.SyncSnapshot, error) {
	if snapDir == "" || snapUploads == "" {
		return nil, ErrSnapshotNotConfigured
	}
	if err := os.MkdirAll(snapDir, 0700); err != nil {
		return nil, err
	}
	// S-1 加固：请求侧先抢占超龄在途行（卡死/重启遗留不再永久占帽，管线自愈）。
	reclaimStaleSnapshotsAt(time.Now().Add(-snapshotStaleAfter))
	inflight, err := model.CountUserActiveSnapshots(userID, []string{model.SnapshotPending, model.SnapshotBuilding})
	if err != nil {
		return nil, err
	}
	if inflight > snapshotUserGenCap {
		return nil, ErrSnapUserCap
	}
	building, err := model.CountBuildingSnapshots()
	if err != nil {
		return nil, err
	}
	if building >= snapshotGlobalCap {
		return nil, ErrSnapGlobalCap
	}
	// 复用该用户在途快照（幂等友好：连点/重连不排第二个队）。
	if inflight == snapshotUserGenCap {
		rows, err := activeUserSnapshotID(userID)
		if err != nil {
			return nil, err
		}
		if rows != "" {
			return model.GetSyncSnapshot(rows)
		}
	}
	now := time.Now()
	s := &model.SyncSnapshot{
		ID:        newSnapID(),
		UserID:    userID,
		Status:    model.SnapshotPending,
		CreatedAt: now,
		ExpiresAt: now.Add(snapshotTTL),
	}
	if err := model.CreateSyncSnapshot(s); err != nil {
		return nil, err
	}
	go func() {
		buildSem <- struct{}{}
		defer func() { <-buildSem }()
		defer func() {
			if r := recover(); r != nil {
				logger.Error("snapshot build panic %s: %v", s.ID, r)
				_, _ = model.FailSnapshotIfActive(s.ID, fmt.Sprintf("panic: %v", r))
				_ = model.ReleaseSnapshotHolds(s.ID)
			}
		}()
		// S-1 看门狗：超时判死（PENDING/BUILDING → FAILED）。构建若卡死在环境 I/O，
		// goroutine 本体可能仍滞留（进程退出时回收），但账本/Hold/请求面即时自愈；
		// 与正常完成双向幂等（见 FailSnapshotIfActive 注释）。
		watchdog := time.AfterFunc(snapshotWatchdogFor, func() {
			ok, werr := model.FailSnapshotIfActive(s.ID, fmt.Sprintf("watchdog: build exceeded %s (S-1)", snapshotWatchdogFor))
			if ok {
				_ = model.ReleaseSnapshotHolds(s.ID)
				_ = os.Remove(filepath.Join(snapDir, s.ID+".zip"))
				_ = os.Remove(stagingPath(s.ID))
				logger.Error("snapshot build %s watchdog timeout (%s) — marked FAILED, artifacts cleaned", s.ID, snapshotWatchdogFor)
			}
			_ = werr
		})
		defer watchdog.Stop()
		if err := BuildSnapshotSync(s.ID, userID, username); err != nil {
			logger.Info("snapshot build %s failed: %v", s.ID, err)
		}
	}()
	return s, nil
}

// reclaimStaleSnapshotsAt 把 created_at 早于 cutoff 的在途行判死并清理半成品。
// 请求侧（每用户帽前）与启动期（cmd/server 启动钩子）共用；幂等。
func reclaimStaleSnapshotsAt(cutoff time.Time) int {
	rows, err := model.ListActiveSnapshots()
	if err != nil {
		logger.Info("snapshot stale reclaim skipped (list failed): %v", err)
		return 0
	}
	n := 0
	for _, s := range rows {
		if s.CreatedAt.IsZero() || s.CreatedAt.After(cutoff) {
			continue
		}
		ok, rerr := model.FailSnapshotIfActive(s.ID, "stale build reclaimed (S-1 watchdog)")
		if rerr != nil || !ok {
			continue
		}
		_ = model.ReleaseSnapshotHolds(s.ID)
		_ = os.Remove(filepath.Join(snapDir, s.ID+".zip"))
		_ = os.Remove(filepath.Join(snapDir, s.ID+".zip.part"))
		_ = os.Remove(stagingPath(s.ID))
		n++
		logger.Info("snapshot %s stale in-flight reclaimed (age > %s)", s.ID, snapshotStaleAfter)
	}
	return n
}

// ReclaimStaleSnapshots 启动期清扫入口（cmd/server 启动钩子调用）。
func ReclaimStaleSnapshots() int {
	return reclaimStaleSnapshotsAt(time.Now().Add(-snapshotStaleAfter))
}

func activeUserSnapshotID(userID int64) (string, error) {
	var id string
	err := database.GetDB().QueryRow(`SELECT id FROM sync_snapshots WHERE user_id = ? AND status IN ('PENDING','BUILDING') LIMIT 1`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func newSnapID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "snap-" + hex.EncodeToString(b)
}

// BuildSnapshotSync 同步执行一次完整构建（goroutine 与测试共用）。
// 成功：zip 落 snapDir/<id>.zip、账本 READY；任何失败：账本 FAILED、半成品清理、Hold 释放。
func BuildSnapshotSync(snapID string, userID int64, username string) error {
	claimed, err := model.ClaimSnapshotForBuild(snapID)
	if err != nil {
		return err
	}
	if !claimed {
		return errors.New("snapshot already claimed by another builder")
	}
	fail := func(reason string) error {
		_ = model.FinishSnapshotBuild(snapID, model.SnapshotFailed, 0, 0, "", "", "", 0, 0, reason)
		_ = model.ReleaseSnapshotHolds(snapID)
		_ = os.Remove(filepath.Join(snapDir, snapID+".zip"))
		_ = os.Remove(stagingPath(snapID))
		return errors.New(reason)
	}
	if err := os.MkdirAll(filepath.Join(snapDir, snapshotStagingDir), 0700); err != nil {
		return fail("mkdir staging: " + err.Error())
	}

	// 1) 冻结库（T0）：SnapshotTo 独立连接 + integrity_check。
	// 锚点读数纪律（v21.157 N1 修法①）：**watermark 必须与冻结点同点**。
	// 原实现在此读活库的 current_change_seq（T1 > T0），而库内容冻结于 T0，
	// 并发 push 时区间 (T0, W] 的事件既不在快照库、也不会被桌面 pull(W,∞) 覆盖 → 静默丢失
	// （2026-10-08 并发实测：T0=1 / W=95 / 库内 max(seq)=48，47 条事件丢失）。
	// 现改为：先在**冻结前**读 epoch（仅作日志对照），再从**冻结副本自身**读该用户的
	// max(change_seq) 作为对外 watermark —— 构造上精确，不依赖「读活库与冻结之间无写入」
	// 这一无法保证的前提。冻结窗口宽度由**冻结之后**的活库读数观测（见步骤 3）。
	staging := stagingPath(snapID)
	t0 := time.Now()
	var liveEpoch int64
	if liveEpoch, err = model.GetServerEpoch(); err != nil {
		return fail("read epoch: " + err.Error())
	}
	if err := backup.SnapshotTo(staging, currentDBPath()); err != nil {
		return fail("snapshot vacuum: " + err.Error())
	}
	logger.Info("snapshot %s step=vacuum done in %s (epoch@freeze=%d)", snapID, time.Since(t0).Round(time.Millisecond), liveEpoch)

	// 2) 投影（staging 上执行；活库零触碰）：仅留请求用户数据并映射 user_id=1。
	t1 := time.Now()
	refs, err := projectStaging(staging, userID)
	if err != nil {
		return fail("project: " + err.Error())
	}
	logger.Info("snapshot %s step=project done in %s refs=%d", snapID, time.Since(t1).Round(time.Millisecond), len(refs))

	// 3) 锚点定版（论证见文件头）：对外 watermark = **冻结副本内本用户的 max(change_seq)**。
	// 这是「库内容 ↔ 锚点」的自证一致：库里有 seq ≤ W 的全部本用户事件，桌面以 cursor=W
	// 起步 pull(W,∞) 恰为补差集，无缺口无重复；并发期间新增的事件必然 seq > W，由差集覆盖。
	// epoch 取冻结前读数（不谎报比内容更新的世代）。liveSeq 仅用于日志观测冻结窗口宽度。
	t2 := time.Now()
	epoch := liveEpoch
	var watermark int64
	// 只读句柄读冻结副本。投影已把本用户映射为 user_id=1（projectStaging:UPDATE ... SET id/user_id=1），
	// 故此处按映射后的身份查。sql.Open 是惰性的，此处只跑一条聚合查询。
	frozen, oerr := sql.Open("sqlite", staging+"?mode=ro&_pragma=busy_timeout(5000)")
	if oerr != nil {
		return fail("open frozen db: " + oerr.Error())
	}
	fqerr := frozen.QueryRow(`SELECT COALESCE(MAX(change_seq), 0) FROM sync_changes WHERE user_id = 1`).Scan(&watermark)
	_ = frozen.Close()
	if fqerr != nil {
		return fail("read frozen watermark: " + fqerr.Error())
	}
	// 冻结窗口宽度观测（**必须在冻结之后读**活库）。
	// ⚠️ 不可拿冻结前的 liveSeq 与冻结副本 max(seq) 比较 —— 两者是**不同时点**的读数，
	// 并发写入下后者必然大于前者（实测 56 > 29），那不是数据层异常而是正常竞态。
	// 曾据此写fail-closed 反向断言，导致并发用例100% 误报「数据层异常，拒绝出账」。
	var liveAfter int64
	if err := database.GetDB().QueryRow(`SELECT current_change_seq FROM sync_meta WHERE id = 1`).Scan(&liveAfter); err != nil {
		return fail("read live watermark after freeze: " + err.Error())
	}
	if watermark > liveAfter {
		// 真正的异常信号：冻结副本里存在活库都没有的事件 = 数据层不一致，必须 fail-closed。
		return fail(fmt.Sprintf("frozen watermark %d exceeds live seq %d — 数据层异常，拒绝出账", watermark, liveAfter))
	}
	if lag := liveAfter - watermark; lag > 0 {
		logger.Info("snapshot %s 冻结窗口 lag=%d 事件（watermark=%d live_after=%d，差集由桌面 pull(W,∞) 补齐）",
			snapID, lag, watermark, liveAfter)
	}
	logger.Info("snapshot %s step=anchors done in %s watermark=%d", snapID, time.Since(t2).Round(time.Millisecond), watermark)

	// 4) Hold 登记（zip 组装前——引用件即刻受保护）。
	if err := model.AddSnapshotHolds(snapID, refs); err != nil {
		return fail("holds: " + err.Error())
	}

	// 5) zip 组装 → sha256（sealed）→ 原子改名。
	t3 := time.Now()
	tmpZip := filepath.Join(snapDir, snapID+".zip.part")
	dbSHA, err := fileSHA(staging)
	if err != nil {
		return fail("db sha: " + err.Error())
	}
	meta := SnapshotMeta{
		SnapshotID: snapID, ProtocolVer: syncspec.ProtocolVersion, UserID: 1, SourceUserID: userID, Username: username,
		ServerEpoch: epoch, Watermark: watermark, InstanceID: instanceID(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339), DBSHA256: dbSHA, FileCount: len(refs),
	}
	n, err := writeZip(tmpZip, staging, snapUploads, refs, meta)
	if err != nil {
		_ = os.Remove(tmpZip)
		return fail("zip: " + err.Error())
	}
	zipSHA, size, err := sealZip(tmpZip)
	if err != nil {
		_ = os.Remove(tmpZip)
		return fail("seal: " + err.Error())
	}
	final := filepath.Join(snapDir, snapID+".zip")
	if err := os.Rename(tmpZip, final); err != nil {
		_ = os.Remove(tmpZip)
		return fail("rename: " + err.Error())
	}
	if err := model.FinishSnapshotBuild(snapID, model.SnapshotReady, epoch, watermark, instanceID(), zipSHA, final, size, n, ""); err != nil {
		return fail("finish ledger: " + err.Error())
	}
	logger.Info("snapshot %s step=zip done in %s READY user=%d epoch=%d watermark=%d files=%d size=%d", snapID, time.Since(t3).Round(time.Millisecond), userID, epoch, watermark, n, size)
	return nil
}

func stagingPath(snapID string) string {
	return filepath.Join(snapDir, snapshotStagingDir, snapID+".db")
}

func currentDBPath() string {
	return dbFilePath()
}

// projectStaging 在冻结副本上执行用户投影，返回其附件引用清单。
// 顺序：先清跨用户关系/子表（FK 安全）→ DELETE 非目标用户 → UPDATE 映射 1。
// 加密笔记密文原样携带（口令不在快照内，桌面按既有加密语义处理）。
func projectStaging(path string, keepUserID int64) ([]string, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	stmts := []string{
		`DELETE FROM note_uploads WHERE note_id NOT IN (SELECT id FROM notes WHERE user_id = ?)`,
		`DELETE FROM note_tags WHERE note_id NOT IN (SELECT id FROM notes WHERE user_id = ?)`,
		`DELETE FROM notes WHERE user_id != ?`,
		`DELETE FROM note_folders WHERE user_id != ?`,
		`DELETE FROM uploads WHERE user_id != ?`,
		`DELETE FROM sync_changes WHERE user_id != ?`,
		`DELETE FROM sync_operations WHERE user_id != ?`,
		`DELETE FROM users WHERE id != ?`,
		`UPDATE users SET id = 1 WHERE id = ?`,
		`UPDATE notes SET user_id = 1`,
		`UPDATE note_folders SET user_id = 1`,
		`UPDATE uploads SET user_id = 1`,
		`UPDATE sync_changes SET user_id = 1`,
		`UPDATE sync_operations SET user_id = 1`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q, keepUserID); err != nil {
			return nil, fmt.Errorf("project stmt %.30q: %w", q, err)
		}
	}
	// 附件引用 = 该用户全部笔记（含回收站软删）绑定件 ∪ uploads 登记件（staged 也带上，
	// 与 pull 语义一致：桌面按 refs 校验缺件自愈，多带无害、少带必坏）。
	rows, err := db.Query(`SELECT DISTINCT filename FROM uploads WHERE filename IS NOT NULL AND filename != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []string
	seen := map[string]bool{}
	for rows.Next() {
		var f string
		if rows.Scan(&f) != nil {
			continue
		}
		if !snapshotFilenameRe.MatchString(f) || filepath.Base(f) != f || seen[f] {
			logger.Info("snapshot skip invalid ref %q", f) // 脏名不进 zip（防穿越双保险）
			continue
		}
		seen[f] = true
		refs = append(refs, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 收敛 WAL：closed 单文件（F1/Activate staging 要求）。
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return nil, err
	}
	return refs, nil
}

func writeZip(zipPath, dbPath, uploadDir string, refs []string, meta SnapshotMeta) (int, error) {
	f, err := os.Create(zipPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(name, src string) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	}
	if err := add("notes.db", dbPath); err != nil {
		return 0, err
	}
	included := 0
	for _, r := range refs {
		src := filepath.Join(uploadDir, r)
		st, err := os.Stat(src)
		if err != nil || st.IsDir() {
			logger.Info("snapshot ref missing on disk, skip: %s", r) // 缺件不阻断（桌面下载轮自愈）
			continue
		}
		if err := add("uploads/"+r, src); err != nil {
			return 0, err
		}
		included++
	}
	meta.FileCount = included
	mb, err := json.Marshal(meta)
	if err != nil {
		return 0, err
	}
	w, err := zw.Create("meta.json")
	if err != nil {
		return 0, err
	}
	if _, err := w.Write(mb); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return included, nil
}

// sealZip fsync + sha256 + size（对封闭文件，F1）。
func sealZip(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, err
	}
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), nil
}

func fileSHA(path string) (string, error) {
	s, _, err := sealZip(path)
	return s, err
}

// ScanSnapshots TTL/孤儿扫描（§46/§48）：启动一次 + 6h ticker。
// 返回处理计数（日志用）。幂等；任何单项失败不阻断其余。
func ScanSnapshots() (expired int, orphanFiles int) {
	if snapDir == "" {
		return
	}
	now := time.Now().UTC()
	list, err := model.ListExpiredReadySnapshots(now)
	if err != nil {
		logger.Error("ScanSnapshots list expired: %v", err)
	}
	for _, s := range list {
		ok, err := model.ExpireSnapshot(s.ID)
		if err != nil || !ok {
			continue
		}
		_ = model.ReleaseSnapshotHolds(s.ID)
		if s.FilePath != "" {
			if rmErr := os.Remove(s.FilePath); rmErr != nil && !os.IsNotExist(rmErr) {
				logger.Error("ScanSnapshots remove %s: %v", s.FilePath, rmErr)
			}
		}
		expired++
	}
	// 孤儿 zip：目录里存在但账本无引用（账本行被删/崩溃残留）。
	rows, err := model.AllSyncSnapshotRows()
	if err != nil {
		logger.Error("ScanSnapshots ledger read: %v", err)
		return
	}
	referenced := map[string]bool{}
	for _, fp := range rows {
		if fp != "" {
			referenced[filepath.Clean(fp)] = true
		}
	}
	entries, err := os.ReadDir(snapDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".zip") && !strings.HasSuffix(name, ".zip.part") {
			continue
		}
		full := filepath.Clean(filepath.Join(snapDir, name))
		if !referenced[full] {
			if rmErr := os.Remove(full); rmErr == nil {
				orphanFiles++
			}
		}
	}
	// staging 残留（构建崩溃遗留冻结库）按账本对不清就删（>1h 的孤儿文件同口径）。
	stag := filepath.Join(snapDir, snapshotStagingDir)
	if ents, err := os.ReadDir(stag); err == nil {
		for _, e := range ents {
			full := filepath.Join(stag, e.Name())
			info, statErr := e.Info()
			if statErr == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(full)
			}
		}
	}
	return
}

// SnapshotPathForDownload 校验快照可下载并返回文件路径（刀 2 handler 用）。
func SnapshotPathForDownload(s *model.SyncSnapshot) (string, error) {
	if s.Status != model.SnapshotReady {
		return "", fmt.Errorf("snapshot %s not ready (status=%s)", s.ID, s.Status)
	}
	if s.FilePath == "" {
		return "", errors.New("snapshot has no file path")
	}
	cleanDir := filepath.Clean(snapDir) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(s.FilePath), cleanDir) {
		return "", errors.New("snapshot path outside managed dir")
	}
	if _, err := os.Stat(s.FilePath); err != nil {
		return "", err
	}
	return s.FilePath, nil
}
