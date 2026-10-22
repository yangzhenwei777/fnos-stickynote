package restore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"stickynote/auth"
	"stickynote/backup"
	"stickynote/database"
	"stickynote/logger"
	"stickynote/model"
)

// 在线全量恢复状态机（v1.0.168 切片 3；v1.0.175 R0 起叠加代际屏障）。
//
// 在**不关闭 HTTP** 的前提下，用「写前日志 + 改名移开原件（非删除）+ 排空在途请求 +
// 安全窗口 503」把整站（DB + 附件目录 + .jwt_secret，必要时含主密钥/instance）原子替换为
// 某个备份的内容；任一步失败即在窗口内回滚到原状态，进程崩溃则重启时据日志回滚或补收尾。
//
// R0 Ready 屏障（v20 §87，红线 15/16）：成功路径尾部严格为
//
//	epoch 落库(新库) → integrity_check → Journal DONE → SESSION_REVOKE → 开闸(Health 200)
//
// ——开闸是最后一步；epoch 未固化或吊销未完成时绝不允许 Health=200。
// SESSION_REVOKE 只在成功路径执行一次（restore_finalizations 幂等）；回滚不吊销会话
// （系统已回到恢复前，旧令牌本就有效）——与评审结论一致，无 SESSION_REVOKE_AGAIN。

var (
	ErrRefuseEnv         = errors.New("REFUSE_FULL_RESTORE")           // 部署以 env JWT_SECRET 为准，禁在线全量恢复
	ErrIdentityMismatch  = errors.New("RESTORE_IDENTITY_MISMATCH")     // 归档 instance 与当前不符且未显式允许换身份
	ErrNoUsableKey       = errors.New("RESTORE_NO_USABLE_KEY")         // 主密钥四来源均无法解开归档
	ErrArchiveVerify     = errors.New("RESTORE_ARCHIVE_VERIFY_FAILED") // 归档完整性/哈希校验未通过
	ErrDrainTimeout      = errors.New("RESTORE_DRAIN_TIMEOUT")         // 在途请求排空超时，动手前放弃
	ErrAlreadyFinalizing = errors.New("RESTORE_ALREADY_IN_PROGRESS")
)

// Options 恢复器运行环境（由 cmd/server 注入）。
type Options struct {
	DataDir        string
	UploadDir      string
	DBPath         string
	AppVersion     string
	JWTSecretIsEnv bool
	ArchiveCap     int64
	DrainTimeout   time.Duration
}

// Request 一次恢复请求。ArchiveBytes 由上层从 WebDAV 取回（≤归档帽）。
type Request struct {
	RestoreID           string
	BackupID            string
	ArchiveBytes        []byte
	KeyFileText         string // 密钥来源 tier-3：管理员当场粘贴的 backup_master_key.txt
	AllowIdentityChange bool   // 显式允许用别的实例身份覆盖当前实例
}

// Controller 恢复编排器。进程内单例；同时只允许一个恢复（manager 互斥为主，此处兜底）。
type Controller struct {
	opt Options

	busy bool

	// 相位回调（测试注入故障用；生产为 nil）。在对应 State 即将推进前调用，返回 err 即中止。
	// R0 语义分界：StateDone 落盘**前**触发 → 回滚区（epoch 不落生效库、开闸恢复窗口）；
	// StateFinalizing 触发 → 已提交区（不回滚、保持 503 窗口，必测 §145）。
	failAt func(next State) error

	// 最近一次结果（供带外进度/审计查询；仅内存，不含敏感数据）。
	// stateMu 保护三字段：异步恢复 goroutine 经 setPhase 写，RestoreProgress handler 经 Last 读。
	stateMu   sync.Mutex
	lastState State
	lastErr   string
	lastAt    time.Time
}

func New(opt Options) *Controller {
	if opt.DrainTimeout <= 0 {
		opt.DrainTimeout = 15 * time.Second
	}
	if opt.ArchiveCap <= 0 {
		opt.ArchiveCap = backup.DefaultArchiveCapBytes
	}
	return &Controller{opt: opt}
}

func (c *Controller) env() backup.Env {
	return backup.Env{DataDir: c.opt.DataDir, UploadDir: c.opt.UploadDir, AppVersion: c.opt.AppVersion, ArchiveCap: c.opt.ArchiveCap}
}

func (c *Controller) setPhase(s State, e string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.lastState, c.lastErr, c.lastAt = s, e, time.Now()
}

// Last 返回最近一次恢复的相位/错误（内存态，供带外进度查询与审计；无敏感数据）。
func (c *Controller) Last() (State, string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.lastState, c.lastErr
}

// Run 执行一次在线全量恢复。调用方须已持有 manager 运行锁（与备份互斥）。
func (c *Controller) Run(req Request) (err error) {
	c.setPhase(StateIntent, "")

	// ===== 只读预检（任何破坏性动作之前，失败即无副作用返回）=====
	if c.opt.JWTSecretIsEnv {
		return fmt.Errorf("%w：当前部署以环境变量 JWT_SECRET 为权威，在线整库换密钥语义不一致；请改用停服后 CLI 恢复", ErrRefuseEnv)
	}
	if req.RestoreID == "" || req.BackupID == "" {
		return errors.New("restore: empty restore_id/backup_id")
	}

	env := c.env()
	man, payload, master, adopted, verr := c.openArchive(req)
	if verr != nil {
		c.setPhase(StateIntent, verr.Error())
		return verr
	}
	// 归档完整性：逐文件哈希 + db integrity（payload 已 EnvelopeOpen 认证解密）。
	if err := c.verifyPayload(env, man, payload); err != nil {
		c.setPhase(StateIntent, err.Error())
		return err
	}
	// 身份校验。
	curInst := currentInstanceID(c.opt.DataDir)
	if curInst != "" && man.InstanceID != curInst && !req.AllowIdentityChange {
		return fmt.Errorf("%w：归档属实例 %s，当前实例 %s；如确要整体替换请勾选“允许更换实例身份”", ErrIdentityMismatch, man.InstanceID, curInst)
	}

	// ===== R0（v20 §85）：进入窗口前解析代际演进。live 库读 pre_epoch 失败 = fail-closed =====
	preEpoch, perr := model.GetServerEpoch()
	if perr != nil {
		return fmt.Errorf("restore: read current server_epoch failed (fail-closed): %w", perr)
	}
	newEpoch := model.NextRestoreEpoch(preEpoch, man.ServerEpoch)

	// ===== 进入安全窗口 + 排空在途 =====
	database.SetRestoring(true)
	if !database.WaitInflight(c.opt.DrainTimeout) {
		database.SetRestoring(false)
		return fmt.Errorf("%w：仍有进行中的请求，稍后重试", ErrDrainTimeout)
	}

	j := &Journal{
		RestoreID: req.RestoreID, BackupID: req.BackupID, ArchiveInstance: man.InstanceID,
		PreRestoreEpoch: preEpoch, RestoredEpoch: man.ServerEpoch, NewEpoch: newEpoch,
		State: StateIntent, StageDir: filepath.Join(c.opt.DataDir, ".restore-stage-"+req.RestoreID),
		JournalPath: journalPath(c.opt.DataDir),
	}
	// committed：epoch 已随 DONE 固化到新库。此后禁止回滚、且不得开闸（保 503 待收尾），
	// 直到 finalize（会话吊销）成功——v20 §87 / 必测 §145。
	committed := false
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("restore panic: %v", r)
		}
		if err == nil {
			return
		}
		if committed {
			// 数据与代际已提交，仅收尾失败：保持维护窗口（Health≠200），重启后由
			// StartupRecover/NeedsStartupFinalize 补做吊销。此处绝不 rollback（红线 13）。
			logger.Error("CRITICAL restore %s committed (epoch=%d) but tail failed, maintenance window retained: %v", req.RestoreID, newEpoch, err)
			return
		}
		c.rollback(j)
		database.SetRestoring(false)
		c.setPhase(StateRollingBack, err.Error())
		logger.Error("restore %s failed, rolled back: %v", req.RestoreID, err)
	}()
	if err := j.write(); err != nil { // INTENT 落盘
		return err
	}

	// 解包到 staging（非破坏性；staging 在 DataDir 子树，失败仅删目录）。
	if _, err := backup.ExtractArchive(payload, j.StageDir); err != nil {
		return fmt.Errorf("extract to stage: %w", err)
	}

	// ===== 破坏性区 =====
	if err := c.fault(StateSwapped); err != nil {
		return err
	}
	if err := c.swapOut(j, master, adopted); err != nil { // checkpoint+close，原件改名移开，State=SWAPPED
		return err
	}
	if err := c.fault(StateExtracted); err != nil {
		return err
	}
	newJWT, err := c.placeIn(j, master, adopted) // staging 文件落到正式位 + jwt 解密 + 可选采纳新密钥，State=EXTRACTED
	if err != nil {
		return err
	}
	if err := c.fault(StateMigrated); err != nil {
		return err
	}
	if err := c.reopenAndMigrate(req.RestoreID, newJWT); err != nil { // 重开跑迁移 + restore_generation + auth.Init，State=MIGRATED
		return err
	}
	// ===== R0 Ready 屏障（§87）：epoch 落库 → integrity → DONE，全部成功才算提交 =====
	if err := c.commitReady(newEpoch); err != nil {
		return err // 仍在可回滚区：新库（连同已写 epoch）整体废弃，旧库复位后 epoch 不变
	}
	j.State = StateDone
	if err := j.write(); err != nil {
		return err
	}
	committed = true // 此后禁回滚；开闸必须等 finalize 成功
	c.setPhase(StateDone, "")

	// 测试注入点（v20 必测 §145）：模拟"epoch 已固化、吊销未完成"的尾部失败。
	if err := c.fault(StateFinalizing); err != nil {
		return err
	}
	// ===== 收尾（会话吊销，幂等）在窗口内完成；失败保持 503，绝不提前 Health=200 =====
	if ferr := c.finalize(req.RestoreID, adopted); ferr != nil {
		return fmt.Errorf("restore committed (epoch=%d) but finalize/session-revoke failed, maintenance window kept — restart to finish: %w", newEpoch, ferr)
	}
	database.SetRestoring(false) // 最后一步：Health Ready（§87 顺序红线）
	c.setPhase(StateFinalizing, "")
	return nil
}

// commitReady 在**新库**上固化代际并做全库完整性校验（§87：epoch 落库 → integrity）。
// 顺序说明：先写 epoch 再校验——integrity 通过即代表"含新代际的库"整体健康；
// 任一步失败由调用方在可回滚区返回，新库连同其中的 epoch 一起废弃（§85 失败 epoch 不变）。
func (c *Controller) commitReady(newEpoch int64) error {
	if err := model.SetServerEpoch(newEpoch); err != nil {
		return err
	}
	var verdict string
	if err := database.DB.QueryRow("PRAGMA integrity_check").Scan(&verdict); err != nil {
		return fmt.Errorf("restored db integrity_check query: %w", err)
	}
	if verdict != "ok" {
		return fmt.Errorf("%w：恢复库 integrity_check 返回 %q", ErrArchiveVerify, verdict)
	}
	return nil
}

// openArchive 用主密钥四来源逐一试解归档（GCM tag 为权威），返回可解开的那个 + 是否需采纳新密钥。
// tier1 磁盘 master；tier2 待采纳 pending；tier3 请求内联 keyFileText。
func (c *Controller) openArchive(req Request) (*backup.InternalManifest, []byte, []byte, bool, error) {
	type cand struct {
		key     []byte
		adopted bool // true=非当前磁盘密钥，成功后需采纳为运行密钥
	}
	var cands []cand
	if k, err := backup.ReadMasterKey(c.opt.DataDir); err == nil {
		cands = append(cands, cand{key: k, adopted: false})
	}
	if p, err := backup.LoadPendingIdentity(c.opt.DataDir); err == nil && p != nil {
		cands = append(cands, cand{key: p.MasterKey, adopted: true})
	}
	if strings.TrimSpace(req.KeyFileText) != "" {
		if k, _, kerr := backup.ParseExportBundle(req.KeyFileText); kerr == nil {
			cands = append(cands, cand{key: k, adopted: true})
		}
	}
	for _, cd := range cands {
		payload, _, oerr := backup.EnvelopeOpen(req.ArchiveBytes, cd.key, c.opt.ArchiveCap)
		if oerr != nil {
			continue // 非该密钥（认证失败）→ 试下一个来源
		}
		mj, err := readMemberManifest(payload, c.env())
		if err != nil {
			return nil, nil, nil, false, err
		}
		if mj.BackupID != req.BackupID {
			return nil, nil, nil, false, fmt.Errorf("%w：envelope 解出的 backup_id 与请求不符", ErrArchiveVerify)
		}
		return mj, payload, cd.key, cd.adopted, nil
	}
	return nil, nil, nil, false, fmt.Errorf("%w：本地/待采纳/内联密钥均无法解开此归档", ErrNoUsableKey)
}

// verifyPayload 逐文件哈希比对 + db integrity。
func (c *Controller) verifyPayload(env backup.Env, man *backup.InternalManifest, payload []byte) error {
	tmp, err := os.MkdirTemp("", "snbak-rvfy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	computed, err := backup.ExtractArchive(payload, tmp)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArchiveVerify, err)
	}
	for rel, want := range man.FileHashes {
		if computed[rel] != want {
			return fmt.Errorf("%w: %s 哈希不符（归档损坏）", ErrArchiveVerify, rel)
		}
	}
	if err := backup.IntegrityCheck(filepath.Join(tmp, "notepad.db")); err != nil {
		return err
	}
	return nil
}

// swapOut checkpoint+关库，把原件改名移作 .pre-restore，推进 State=SWAPPED。
func (c *Controller) swapOut(j *Journal, master []byte, adopt bool) error {
	if database.DB != nil {
		_, _ = database.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		database.Close()
	}
	// 需移开的文件：db(+wal/shm 若在)、.jwt_secret、采纳时 (master_key/instance_id)；目录：upload。
	cands := []string{
		filepath.Join(c.opt.DataDir, "notepad.db"),
		filepath.Join(c.opt.DataDir, "notepad.db-wal"),
		filepath.Join(c.opt.DataDir, "notepad.db-shm"),
		filepath.Join(c.opt.DataDir, ".jwt_secret"),
	}
	if adopt {
		cands = append(cands,
			filepath.Join(c.opt.DataDir, ".backup_master_key"),
			filepath.Join(c.opt.DataDir, ".instance_id"),
		)
	}
	for _, orig := range cands {
		if err := c.renameAside(j, orig, "file"); err != nil {
			return err
		}
	}
	if err := c.renameAside(j, c.opt.UploadDir, "dir"); err != nil {
		return err
	}
	j.State = StateSwapped
	return j.write()
}

// renameAside 把一个已存在的 original 改名移开并登记；不存在则记 Created 且不动。
func (c *Controller) renameAside(j *Journal, original, kind string) error {
	_, statErr := os.Lstat(original)
	if os.IsNotExist(statErr) {
		return nil // 没有原件（如 fresh 无 wal）——placeIn 直接创建，回滚删除即可
	} else if statErr != nil {
		return statErr
	}
	pre := original + ".pre-restore-" + j.RestoreID
	if err := os.Rename(original, pre); err != nil {
		return fmt.Errorf("rename aside %s: %w", original, err)
	}
	j.Ops = append(j.Ops, RenameOp{Kind: kind, Original: original, PreRestore: pre})
	syncPath(filepath.Dir(pre))
	return nil
}

// placeIn 把 staging 内容落到正式位；返回新 jwt 明文；采纳时写主密钥/instance。推进 State=EXTRACTED。
func (c *Controller) placeIn(j *Journal, master []byte, adopt bool) (string, error) {
	dbSrc := filepath.Join(j.StageDir, "notepad.db")
	dbDst := filepath.Join(c.opt.DataDir, "notepad.db")
	if err := copyFile(dbSrc, dbDst, 0600); err != nil {
		return "", err
	}
	syncPath(dbDst)

	newJWT := ""
	if enc, err := os.ReadFile(filepath.Join(j.StageDir, "jwt_secret.enc")); err == nil {
		// 归档含 jwt_secret.enc → 必须成功解出并落盘；解密失败说明 master/归档不一致，
		// 静默跳过会让恢复后沿用错误 secret → 会话/TOTP 全废。fail-closed（审计 GPT-06）。
		plain, derr := backup.UnwrapJWTSecret(enc, master)
		if derr != nil || len(plain) == 0 {
			if derr == nil {
				derr = fmt.Errorf("unwrapped secret is empty")
			}
			return "", fmt.Errorf("restore: unwrap staged jwt_secret failed: %w", derr)
		}
		newJWT = strings.TrimRight(string(plain), "\n")
		if werr := os.WriteFile(filepath.Join(c.opt.DataDir, ".jwt_secret"), []byte(newJWT+"\n"), 0600); werr != nil {
			return "", werr
		}
	} // 无 jwt_secret.enc（env 部署产的档 / 旧版无该文件）→ 保留空：恢复后仍靠 env 或既有 secret

	// 采纳非磁盘密钥：把解开本归档的 master 落为运行主密钥（swapOut 已把旧 master_key 移开）。
	if adopt {
		if err := backup.SetMasterKey(c.opt.DataDir, master); err != nil {
			return "", err
		}
	}

	// 附件目录：staging/uploads/* 灌入新建 UploadDir。
	if mkErr := os.MkdirAll(c.opt.UploadDir, 0700); mkErr != nil {
		return "", mkErr
	}
	stageUploads := filepath.Join(j.StageDir, "uploads")
	ents, rerr := os.ReadDir(stageUploads)
	if rerr != nil && !os.IsNotExist(rerr) {
		// 目录存在却读失败（IO/权限）→ 附件可能整批丢失，必须 fail-closed 触发回滚（审计 GPT-05）；
		// ENOENT（归档本就无附件）是合法空集，继续。
		return "", fmt.Errorf("restore: read staged uploads dir failed: %w", rerr)
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if cerr := copyFile(filepath.Join(stageUploads, e.Name()), filepath.Join(c.opt.UploadDir, e.Name()), 0600); cerr != nil {
			return "", cerr
		}
	}

	if j.ArchiveInstance != "" {
		if err := backup.SetInstanceID(c.opt.DataDir, j.ArchiveInstance); err != nil {
			return "", err
		}
	}
	j.State = StateExtracted
	return newJWT, j.write()
}

// reopenAndMigrate 重开库（跑迁移=MIGRATE）、写 restore_generation、复位 auth。推进 State=MIGRATED。
func (c *Controller) reopenAndMigrate(restoreID, newJWT string) error {
	if err := database.Init(c.opt.DBPath, c.opt.AppVersion); err != nil {
		return fmt.Errorf("reopen restored db: %w", err)
	}
	if err := model.SetConfig("restore_generation", restoreID); err != nil {
		return err
	}
	if newJWT != "" {
		auth.Init(newJWT)
	}
	return nil
}

// finalize 会话吊销（幂等）+ 清理 pending/暂存 + 删日志。
func (c *Controller) finalize(restoreID string, adoptedFromNonDiskKey bool) error {
	j, err := loadJournal(c.opt.DataDir)
	if err != nil {
		return err
	}
	if j == nil {
		return nil // 已被清理
	}
	first, err := model.MarkRestoreFinalized(restoreID)
	if err != nil {
		return err
	}
	if first {
		if err := model.RevokeAllSessions(); err != nil {
			return err
		}
	}
	// 批次5 P5：epoch 换代后 Bootstrap 快照全部作废（幂等，重启补收尾也走此路）。
	// 钩子由 cmd/server 注入（sync.InvalidateBootstrapSnapshots），避免 restore→sync 反向依赖。
	if OnEpochCommitted != nil {
		OnEpochCommitted()
	}
	if adoptedFromNonDiskKey {
		_ = backup.ClearPendingIdentity(c.opt.DataDir)
	}
	c.cleanupAside(j)
	return clearJournal(c.opt.DataDir)
}

// OnEpochCommitted 恢复成功固化新 epoch 后的回调挂点（cmd/server 注入快照作废逻辑）。
var OnEpochCommitted func()

// NeedsStartupFinalize 供启动期调用：若发现 DONE/FINALIZING 但未收尾的日志，返回其 restoreID
// （调用方在 database.Init 之后转交 Finalize）。无则返回 ""。
func (c *Controller) NeedsStartupFinalize() string {
	j, err := loadJournal(c.opt.DataDir)
	if err != nil || j == nil {
		return ""
	}
	if j.State == StateDone || j.State == StateFinalizing {
		return j.RestoreID
	}
	return ""
}

// Finalize 供启动期在 database.Init 之后补做崩溃于 DONE 未收尾的恢复（幂等）。
func (c *Controller) Finalize(restoreID string) error {
	return c.finalize(restoreID, true)
}

// StartupRecover 在 database.Init **之前**执行崩溃恢复：
//   - 无日志 / INTENT（未破坏）→ 清 staging + 日志。
//   - SWAPPED/EXTRACTED/MIGRATED（破坏未 DONE）→ 文件级回滚到原件（此刻 DB 尚未开）。
//   - DONE/FINALIZING → 保留恢复结果，删原件备份与 staging，交后续 finalize。
func (c *Controller) StartupRecover() error {
	j, err := loadJournal(c.opt.DataDir)
	if err != nil {
		return err
	}
	if j == nil {
		return nil
	}
	switch j.State {
	case StateIntent:
		os.RemoveAll(j.StageDir)
		return clearJournal(c.opt.DataDir)
	case StateSwapped, StateExtracted, StateMigrated, StateRollingBack:
		if err := rollbackOps(j.Ops); err != nil {
			return fmt.Errorf("startup rollback failed (manual intervention needed): %w", err)
		}
		os.RemoveAll(j.StageDir)
		return clearJournal(c.opt.DataDir)
	case StateDone, StateFinalizing:
		c.cleanupAside(j) // 删 .pre-restore 原件（已被成功恢复取代）+ staging
		return nil        // 保留日志，等 NeedsStartupFinalize→finalize
	default:
		return nil
	}
}

// rollback 进程内失败回滚（可能在 DB 已重开状态下）：关库→文件回滚→重开原库→复位 auth。
func (c *Controller) rollback(j *Journal) {
	if database.DB != nil {
		database.Close()
	}
	if err := rollbackOps(j.Ops); err != nil {
		logger.Error("CRITICAL restore rollback failed for %s: %v（线上库可能处于半恢复态，需人工介入）", j.RestoreID, err)
		return
	}
	os.RemoveAll(j.StageDir)
	// 重开回滚后的原库；auth 用回滚后的 .jwt_secret（若有）。
	if err := database.Init(c.opt.DBPath, c.opt.AppVersion); err != nil {
		logger.Error("CRITICAL restore rollback reopen failed for %s: %v", j.RestoreID, err)
		return
	}
	if b, err := os.ReadFile(filepath.Join(c.opt.DataDir, ".jwt_secret")); err == nil && len(b) > 0 {
		auth.Init(strings.TrimRight(string(b), "\n"))
	}
	_ = clearJournal(c.opt.DataDir)
}

// rollbackOps 把登记的改名逆序还原：删掉正式位上的恢复产物，再把 .pre-restore 移回。
func rollbackOps(ops []RenameOp) error {
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		if _, err := os.Lstat(op.Original); err == nil {
			if op.Kind == "dir" {
				_ = os.RemoveAll(op.Original)
			} else {
				_ = os.Remove(op.Original)
			}
		}
		if op.PreRestore != "" {
			if err := os.Rename(op.PreRestore, op.Original); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("restore %s←%s: %w", op.Original, op.PreRestore, err)
			}
		}
	}
	return nil
}

// cleanupAside 删除成功恢复后被取代的 .pre-restore 原件 + staging（此时已确认新状态生效）。
func (c *Controller) cleanupAside(j *Journal) {
	for _, op := range j.Ops {
		if op.PreRestore != "" {
			if op.Kind == "dir" {
				_ = os.RemoveAll(op.PreRestore)
			} else {
				_ = os.Remove(op.PreRestore)
			}
		}
	}
	_ = os.RemoveAll(j.StageDir)
}

func (c *Controller) fault(next State) error {
	if c.failAt != nil {
		return c.failAt(next)
	}
	return nil
}

// ---- 小工具 ----

func currentInstanceID(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, ".instance_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readMemberManifest 从已解密的 payload 里读 manifest.json（不整包重解，复用 ExtractArchive 到临时）。
func readMemberManifest(payload []byte, env backup.Env) (*backup.InternalManifest, error) {
	tmp, err := os.MkdirTemp("", "snbak-man-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if _, err := backup.ExtractArchive(payload, tmp); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(tmp, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: manifest 缺失: %v", ErrArchiveVerify, err)
	}
	man, err := backup.ParseInternalManifest(b)
	if err != nil {
		return nil, err
	}
	// v20 §106/§107 fail-closed 守卫：PORTABLE 便携包是单用户脱敏导出（无 .jwt_secret、
	// TOTP/凭据已剔除、仅一个 users 行），绝不可被当作整站灾备归档恢复。
	if man.BackupType == backup.BackupTypePortable {
		return nil, fmt.Errorf("%w: 该归档是便携备份（PORTABLE），服务端整站恢复不接受；请在桌面客户端导入", ErrArchiveVerify)
	}
	return man, nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
