package backup

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"stickynote/crypto"
	"stickynote/logger"
	"stickynote/model"
	"stickynote/webdav"
)

// 备份服务层（切片 2）：把切片 1 的本地产档引擎、webdav 传输、model 记录层编排成
// 一次完整的"采集→打包→自检→上传→提交标记→轮转"，并对主密钥四操作做全局互斥。
//
// 关键纪律（v4.3-final）：
//   - 单实例串行：备份运行 与 主密钥 导出/导入/再生成 共用一把 running 互斥锁，
//     运行中不得换钥（否则 credential-store 重包与归档 kid 会打架）。
//   - 先立后破：新归档 PUT+index 校验通过（VERIFIED）后，才轮转删旧；且保留数 ≥1（never-to-zero）。
//   - 提交标记：index 最后写、且 GET 回来核对 backup_id+archive_sha256，只有它可信才算备份成功。
//   - 凭据不落明文：WebDAV 密码用 master 派生子密钥 credential-store 加密后入 backup_targets。

const credentialKeyInfo = "credential-store"

// Manager 备份编排器。进程内单例（cmd/server 启动时构造并注入 handler/scheduler）。
type Manager struct {
	env Env

	mu      sync.Mutex // 保护 running
	running bool       // 备份运行 / 主密钥操作 互斥标志

	newClient func(webdav.Options) (*webdav.Client, error) // 测试可注入；默认 webdav.New
	nowFunc   func() time.Time                             // 测试可注入调度时间
}

// NewManager 构造编排器。appVersion 已去 v 前缀由调用方保证。
func NewManager(dataDir, uploadDir, appVersion string, schemaVer int) *Manager {
	return &Manager{
		env:       Env{DataDir: dataDir, UploadDir: uploadDir, AppVersion: appVersion, SchemaVer: schemaVer},
		newClient: webdav.New,
		nowFunc:   time.Now,
	}
}

// Env 返回运行环境（handler 只读用）。
func (m *Manager) Env() Env { return m.env }

// IsRunning 是否有备份/密钥操作在跑（状态端点用）。
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// DownloadArchive 从某备份的一个 VERIFIED 目标把归档 .snbak 全量取回（离线恢复/下载用）。
// V1 全量入内存（受归档帽 256MB 约束）。返回字节 + 建议文件名。
func (m *Manager) DownloadArchive(backupID string) ([]byte, string, error) {
	targetID, remotePath, ok, err := model.GetPlacementForDownload(backupID)
	if err != nil {
		return nil, "", err
	}
	if !ok || remotePath == "" {
		return nil, "", fmt.Errorf("未找到备份 %s 的可下载副本（可能尚未上传成功或已被轮转删除）", backupID)
	}
	t, err := model.GetBackupTarget(targetID)
	if err != nil {
		return nil, "", err
	}
	c, err := m.clientFor(t)
	if err != nil {
		return nil, "", err
	}
	data, err := c.Get(remotePath)
	if err != nil {
		return nil, "", mapWebDAVErr(err)
	}
	return data, backupID + ".snbak", nil
}

// VerifyRemote 从远端取回归档做完整自检（GCM+AAD+逐文件哈希+integrity），确认存储副本仍可信。
func (m *Manager) VerifyRemote(backupID string) error {
	data, _, err := m.DownloadArchive(backupID)
	if err != nil {
		return err
	}
	tmp := filepath.Join(m.env.tmpDir(), "verify-"+backupID+".snbak")
	if err := os.MkdirAll(m.env.tmpDir(), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := VerifyArchive(m.env, tmp); err != nil {
		return err
	}
	return nil
}

// DiscoverRemote 扫描某目标远端目录，把已存在的历史归档（*.index.json + 对应 .snbak）
// 登记进本地快照账本 + placement(VERIFIED)，使「快照与下载 / 校验 / 下载 / 在线恢复」在
// **清空数据或换机重装后**仍能看到并接管历史备份。
//
// 语义：
//   - 发现只需读明文 index + HEAD 归档尺寸，**不需要主密钥**；但之后校验/下载/恢复需已导入正确主密钥。
//   - placement 记 VERIFIED 与 runJob 的 VERIFIED 同义（对象在位 + index 自洽），非"已解密"。
//   - 已存在的本地快照不覆盖（保留更丰富的 stats/status）；只补登本目标 placement。
//   - 孤儿 index（无对应 .snbak）或尺寸不符（损坏）→ 跳过。
func (m *Manager) DiscoverRemote(targetID int64) (int, error) {
	t, err := model.GetBackupTarget(targetID)
	if err != nil {
		return 0, err
	}
	c, err := m.clientFor(t)
	if err != nil {
		return 0, err
	}
	dir := normalizeDir(t.RemotePath)
	names, err := c.ListNames(dir)
	if err != nil {
		return 0, mapWebDAVErr(err)
	}
	added := 0
	for _, name := range names {
		if !strings.HasSuffix(name, ".index.json") {
			continue
		}
		idxBytes, gerr := c.Get(dir + name)
		if gerr != nil {
			continue
		}
		xi, perr := ParseExternalIndex(idxBytes)
		if perr != nil || xi.BackupID == "" {
			continue
		}
		arcRemote := dir + xi.BackupID + ".snbak"
		size, exists, herr := c.Head(arcRemote)
		if herr != nil || !exists {
			continue // ORPHAN：有 index 无归档
		}
		if xi.ArchiveSizeBytes > 0 && size != xi.ArchiveSizeBytes {
			continue // 尺寸不符：归档损坏/被截断
		}
		if ex, _ := model.SnapshotExists(xi.BackupID); !ex {
			ct, _ := time.Parse(time.RFC3339, xi.CreatedAt)
			_ = model.InsertBackupSnapshot(model.BackupSnapshot{
				BackupID:      xi.BackupID,
				CreatedAt:     ct,
				Trigger:       "discovered",
				ArchiveSha256: xi.ArchiveSha256,
				SizeBytes:     xi.ArchiveSizeBytes,
				AppVersion:    xi.AppVersion,
				SchemaVersion: xi.SchemaVersion,
				Status:        "DISCOVERED",
			})
			added++
		}
		_ = model.UpsertBackupPlacement(xi.BackupID, targetID, "VERIFIED", arcRemote, "")
	}
	return added, nil
}

// DiscoverAllEnabled 对所有启用目标做远端发现，返回新登记快照总数（单目标失败不影响其余）。
func (m *Manager) DiscoverAllEnabled() (int, error) {
	targets, err := model.ListBackupTargets()
	if err != nil {
		return 0, err
	}
	total := 0
	var lastErr error
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		n, derr := m.DiscoverRemote(t.ID)
		if derr != nil {
			lastErr = derr
			logger.Error("backup discover target %d failed: %v", t.ID, derr)
			continue
		}
		total += n
	}
	return total, lastErr
}

// ---- 互斥 ----

func (m *Manager) tryBegin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return false
	}
	m.running = true
	return true
}

func (m *Manager) end() {
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
}

// Lock / Unlock 暴露运行互斥锁给在线恢复复用：备份与恢复严格串行、绝不并发。
// 已有操作在跑 → ErrAlreadyRunning。
func (m *Manager) Lock() error {
	if !m.tryBegin() {
		return ErrAlreadyRunning
	}
	return nil
}

func (m *Manager) Unlock() { m.end() }

// ---- 凭据加解密 ----

func (m *Manager) credKey() ([]byte, error) {
	mk, err := LoadOrCreateMasterKey(m.env.DataDir)
	if err != nil {
		return nil, err
	}
	return crypto.DeriveKey(mk, credentialKeyInfo)
}

// EncryptSecret 加密明文 WebDAV 密码（入库前调）。
func (m *Manager) EncryptSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	k, err := m.credKey()
	if err != nil {
		return "", err
	}
	return crypto.EncryptRaw([]byte(plain), k)
}

// DecryptSecret 解密库里的密码密文（构造客户端时调）。
func (m *Manager) DecryptSecret(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	k, err := m.credKey()
	if err != nil {
		return "", err
	}
	b, err := crypto.DecryptRaw(enc, k)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---- 目标安全门控 + 客户端构造 ----

// validateTargetSecurity 落实"坚果云等公网禁 InsecureSkipTLS"（自签仅限自建/局域网）。
func validateTargetSecurity(t model.BackupTarget) error {
	if !t.AllowInsecureTLS {
		return nil
	}
	host := hostOf(t.BaseURL)
	if isPublicCloud(host) {
		return fmt.Errorf("%w: 公网 WebDAV（%s）不允许跳过 TLS 证书校验", ErrConfigInvalid, host)
	}
	return nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	h := u.Hostname()
	return strings.ToLower(strings.Trim(h, "[]"))
}

// isPublicCloud 粗判常见公网云 WebDAV 域名（这些绝不能 InsecureSkipTLS）。
func isPublicCloud(host string) bool {
	for _, s := range []string{"jianguoyun.com", "dropbox.com", "box.com", "mydrive", "onedrive", "drive.google", "webdav.si", "koofr", "pcloud"} {
		if strings.Contains(host, s) {
			return true
		}
	}
	return false
}

func (m *Manager) clientFor(t model.BackupTarget) (*webdav.Client, error) {
	if err := validateTargetSecurity(t); err != nil {
		return nil, err
	}
	pass, err := m.DecryptSecret(t.SecretEncrypted)
	if err != nil {
		return nil, fmt.Errorf("解密目标凭据失败（主密钥是否已变更？）: %w", err)
	}
	return m.newClient(webdav.Options{
		URL:             t.BaseURL,
		User:            t.Username,
		Pass:            pass,
		AllowLoopback:   t.AllowLoopback,
		InsecureSkipTLS: t.AllowInsecureTLS,
		CustomCAPEM:     t.CustomCAPem,
	})
}

// TestTarget 探测连通/鉴权/目录可建 + **可写性** + MOVE 能力。供 handler "测试连接" 用。
//
// 真机教训（fnOS 自带 WebDAV）：对不存在集合的 PUT 返 405，而 gowebdav 把 MKCOL 的 405
// 强制当 201 吞掉（`if status==405 {status=201}`），于是 MkdirAll"看似"成功、Test 误报 ok，
// 实际根本写不进。故必须做一次真实写探针（写→HEAD 可见→删），任一步失败即判目标不可用——
// 绝不允许"连接 OK 但一备份就 UPLOAD 阶段 405 失败"的假绿。
func (m *Manager) TestTarget(targetID int64) (webdav.ProviderCapability, error) {
	var cap webdav.ProviderCapability
	t, err := model.GetBackupTarget(targetID)
	if err != nil {
		return cap, err
	}
	c, err := m.clientFor(t)
	if err != nil {
		return cap, err
	}
	if err := c.Test(); err != nil {
		return cap, mapWebDAVErr(err)
	}
	dir := normalizeDir(t.RemotePath)
	if err := c.MkdirAll(dir); err != nil {
		return cap, mapWebDAVErr(err)
	}
	// 真实写探针：确认该目录确实可写、可读回、可删。
	probe := dir + ".snbk-write-probe.tmp"
	if err := c.PutBytes(probe, []byte("ping")); err != nil {
		return cap, fmt.Errorf("远端目录不可写（测试连接要求可写；多数实现需该集合已存在或授予写权限）: %w", mapWebDAVErr(err))
	}
	if _, exists, err := c.Head(probe); err != nil {
		return cap, mapWebDAVErr(err)
	} else if !exists {
		return cap, fmt.Errorf("写入探针后远端不可见（HEAD 未见对象），目录不可用")
	}
	_ = c.Delete(probe) // 尽力清理；删不掉不影响可写判定
	return c.ProbeMove(dir), nil
}

// ---- 备份执行 ----

// BeginJob 抢互斥 + 登记 job 行，快速返回 job_id/backup_id（供 handler 202 后异步跑 runJob）。
// 已有备份/密钥操作在跑 → ErrAlreadyRunning。
func (m *Manager) BeginJob(targetID int64, trigger string) (jobID int64, backupID string, err error) {
	if !m.tryBegin() {
		return 0, "", ErrAlreadyRunning
	}
	ok := false
	defer func() {
		if !ok {
			m.end()
		}
	}()
	bid, err := NewBackupID()
	if err != nil {
		return 0, "", err
	}
	jid, err := model.CreateBackupJob("backup", targetID, bid)
	if err != nil {
		return 0, "", err
	}
	ok = true
	return jid, bid, nil
}

// RunBackup 同步跑完整备份（调度器用）。内部 BeginJob + runJob。
func (m *Manager) RunBackup(targetID int64, trigger string) (int64, error) {
	jobID, bid, err := m.BeginJob(targetID, trigger)
	if err != nil {
		return 0, err
	}
	return jobID, m.runJob(targetID, trigger, jobID, bid)
}

// RunBackupJob 执行一个已由 BeginJob 预占互斥的 job（handler 异步分支用）。
// 前置：调用方必须先成功 BeginJob（running 已置位）；本函数结束时释放互斥。
func (m *Manager) RunBackupJob(targetID int64, trigger string, jobID int64, backupID string) error {
	return m.runJob(targetID, trigger, jobID, backupID)
}

// runJob 执行一个已 Begin 的 job，结束释放互斥。返回的 error 同时写进 job 状态。
// 约定：BeginJob 成功后必须调用本函数（handler 异步分支），否则互斥锁悬挂。
func (m *Manager) runJob(targetID int64, trigger string, jobID int64, backupID string) (rerr error) {
	defer m.end()
	defer func() {
		if rerr != nil {
			_ = model.FinishJob(jobID, "FAILED", errCodeString(rerr), rerr.Error())
			_ = model.SetTargetRunResult(targetID, false, rerr.Error())
		}
	}()

	t, err := model.GetBackupTarget(targetID)
	if err != nil {
		return err
	}
	if !t.Enabled {
		return fmt.Errorf("目标 %d 未启用", targetID)
	}

	_ = model.UpdateJobPhase(jobID, "RUNNING", "SNAPSHOT")
	if err := m.preflightDisk(); err != nil {
		return err
	}

	res, err := CreateBackup(m.env, backupID, false)
	if err != nil {
		return err
	}
	defer os.Remove(res.SnbakPath) // 本地暂存产物上传后清理

	// 产档自检：解不开就不算数（GCM+AAD+逐文件哈希+integrity）。
	_ = model.UpdateJobPhase(jobID, "RUNNING", "VERIFY")
	if _, err := VerifyArchive(m.env, res.SnbakPath); err != nil {
		_ = m.recordSnapshot(res, backupID, trigger, "FAILED", true)
		return fmt.Errorf("自检失败: %w", err)
	}
	if err := m.recordSnapshot(res, backupID, trigger, "LOCAL_VERIFIED", res.Incomplete); err != nil {
		return err
	}

	// 上传归档（可靠落盘）。
	_ = model.UpdateJobPhase(jobID, "RUNNING", "UPLOAD")
	c, err := m.clientFor(t)
	if err != nil {
		return err
	}
	dir := normalizeDir(t.RemotePath)
	if err := c.MkdirAll(dir); err != nil {
		return mapWebDAVErr(err)
	}
	arcRemote := dir + backupID + ".snbak"
	partRemote := dir + backupID + ".snbak.part-" + nonce()
	if _, err := c.PutLocalVerified(res.SnbakPath, arcRemote, partRemote); err != nil {
		_ = model.UpsertBackupPlacement(backupID, targetID, "FAILED", arcRemote, err.Error())
		return mapWebDAVErr(err)
	}
	_ = model.UpsertBackupPlacement(backupID, targetID, "UPLOADED", arcRemote, "")

	// index = 提交标记：最后写，且 GET 回来核对内容才算 committed。
	idxBytes, err := json.Marshal(res.Index)
	if err != nil {
		return err
	}
	idxRemote := dir + backupID + ".index.json"
	if err := c.PutBytes(idxRemote, idxBytes); err != nil {
		_ = model.UpsertBackupPlacement(backupID, targetID, "FAILED", idxRemote, err.Error())
		return mapWebDAVErr(err)
	}
	got, err := c.Get(idxRemote)
	if err != nil {
		return mapWebDAVErr(err)
	}
	xi, err := ParseExternalIndex(got)
	if err != nil || xi.BackupID != backupID || xi.ArchiveSha256 != res.SnbakSha256 {
		_ = model.UpsertBackupPlacement(backupID, targetID, "FAILED", idxRemote, "index 内容核对失败")
		return fmt.Errorf("WEBDAV_INDEX_CORRUPT: 提交标记内容不自洽（backup_id/archive_sha256 不匹配）")
	}
	if err := model.UpsertBackupPlacement(backupID, targetID, "VERIFIED", arcRemote, ""); err != nil {
		return err
	}

	// 先立后破：新档 VERIFIED 后才轮转旧档。轮转失败不影响本次备份成功（仅记日志，下次重试）。
	_ = model.UpdateJobPhase(jobID, "RUNNING", "ROTATE")
	if _, warns := m.prune(c, t, dir, backupID); len(warns) > 0 {
		logger.Error("backup %s rotate warnings: %v", backupID, warns)
	}

	if err := model.FinishJob(jobID, "SUCCESS", "", ""); err != nil {
		return err
	}
	if err := model.SetTargetRunResult(targetID, true, ""); err != nil {
		return err
	}
	rerr = nil
	return nil
}

func (m *Manager) recordSnapshot(res *Result, backupID, trigger, status string, incomplete bool) error {
	return model.InsertBackupSnapshot(model.BackupSnapshot{
		BackupID:        backupID,
		CreatedAt:       time.Now().UTC(),
		Trigger:         trigger,
		ArchiveSha256:   res.SnbakSha256,
		SizeBytes:       res.SnbakSize,
		AppVersion:      m.env.AppVersion,
		SchemaVersion:   m.env.SchemaVer,
		NoteCount:       res.Stats.NoteCount,
		UserCount:       res.Stats.UserCount,
		AttachmentCount: res.Stats.AttachmentCount,
		Status:          status,
		Incomplete:      incomplete,
	})
}

// prune 删除超出保留数的旧档（远端对象 + 本地 placement）。never-to-zero 由
// ListPrunableBackupIDs 保证（keep≥1 且总保留最新那批），再额外跳过本次刚上传的 id。
//
// 返回 (成功删除数, 失败详情)。关键纪律（修复"保留份数不生效"根因）：
//   - 远端删除**不再静默吞错**：404 视作幂等成功，其余错误记入 warnings 并**保留 placement**，
//     下次轮转重试——绝不出现"本地以为删了、远端文件还在"的失忆孤儿。
//   - 仅当远端 .snbak 与 .index.json 都确认删除后，才删本目标 placement；若该快照在所有目标
//     上都已无放置（孤儿）→ 一并删 backup_snapshots 行，避免「快照与下载」列表残留死记录。
func (m *Manager) prune(c *webdav.Client, t model.BackupTarget, dir, justUploaded string) (int, []string) {
	ids, err := model.ListPrunableBackupIDs(t.ID, t.RetentionCount)
	if err != nil {
		return 0, []string{err.Error()}
	}
	deleted := 0
	var warnings []string
	for _, id := range ids {
		if id == justUploaded {
			continue
		}
		aerr := c.Delete(dir + id + ".snbak")
		ierr := c.Delete(dir + id + ".index.json")
		aOK := aerr == nil || webdav.IsNotFound(aerr)
		iOK := ierr == nil || webdav.IsNotFound(ierr)
		if !aOK || !iOK {
			msg := "备份 " + id + " 远端删除失败："
			if !aOK {
				msg += fmt.Sprintf("归档(%v)；", aerr)
			}
			if !iOK {
				msg += fmt.Sprintf("索引(%v)", ierr)
			}
			warnings = append(warnings, msg)
			continue // 保留 placement，下次轮转重试
		}
		_ = model.DeleteBackupPlacement(id, t.ID)
		// 孤儿快照清理：所有目标均无放置 → 删快照行（避免列表残留）。
		if n, cerr := model.CountBackupPlacements(id); cerr == nil && n == 0 {
			_ = model.DeleteBackupSnapshot(id)
		}
		deleted++
	}
	return deleted, warnings
}

// PruneTarget 按目标**当前** retention_count 立即轮转超量旧档（用户把「保留份数」改小后
// 即时生效，不必等到下次备份）。与备份/恢复/密钥操作互斥（tryBegin）。
// 返回 (成功删除数, 失败详情, 致命错误)。
//
// 边界：连接/凭据失败返回 error，但调用方（handler）**不应**因此回滚已保存的目标配置——
// 配置已落库，远端清理失败只提示用户（下次备份成功时 prune 仍会兜底轮转）。
func (m *Manager) PruneTarget(targetID int64) (int, []string, error) {
	if !m.tryBegin() {
		return 0, nil, ErrAlreadyRunning
	}
	defer m.end()
	t, err := model.GetBackupTarget(targetID)
	if err != nil {
		return 0, nil, err
	}
	c, err := m.clientFor(t)
	if err != nil {
		return 0, nil, err
	}
	deleted, warnings := m.prune(c, t, normalizeDir(t.RemotePath), "")
	return deleted, warnings, nil
}

// DeleteSnapshot 手动删除一份备份（用户点「删除备份」）。遍历该 backup_id 在**所有目标**
// 上的放置，逐个删远端 .snbak + .index.json（尽力而为：单目标连不上/凭据失效不阻断其余，
// 404 视作幂等成功），最后删本地快照行——placement 经 FK ON DELETE CASCADE 自动清空。
//
// 语义与边界：
//   - 运行互斥：备份/恢复/密钥操作进行中禁止删除（避免与 upload/prune 竞争同一远端对象）。
//   - 本地行必删：即使部分远端删除失败，本地快照行仍删除（用户明确要"从列表移除"）；
//     删不掉的远端对象成为孤儿，下次「从远端发现」可重新登记（可接受，不静默吞错）。
//   - 返回 (deletedRemote, warnings, err)：deletedRemote=成功删除远端归档的目标数，
//     warnings=各目标失败原因（供 handler 透传前端提示），err=本地删行等致命错误。
func (m *Manager) DeleteSnapshot(backupID string) (int, []string, error) {
	if !m.tryBegin() {
		return 0, nil, ErrAlreadyRunning
	}
	defer m.end()

	placements, err := model.ListBackupPlacements(backupID)
	if err != nil {
		return 0, nil, err
	}
	deleted := 0
	var warnings []string
	for _, p := range placements {
		t, gerr := model.GetBackupTarget(p.TargetID)
		if gerr != nil {
			// 目标行已不存在：远端对象无从定位，跳过（placement 随快照行级联清除）。
			warnings = append(warnings, fmt.Sprintf("目标 #%d 已不存在，跳过其远端删除", p.TargetID))
			continue
		}
		c, cerr := m.clientFor(t)
		if cerr != nil {
			warnings = append(warnings, fmt.Sprintf("目标「%s」连接失败：%v", t.Name, cerr))
			continue
		}
		dir := normalizeDir(t.RemotePath)
		arcRemote := dir + backupID + ".snbak"
		idxRemote := dir + backupID + ".index.json"
		aerr := c.Delete(arcRemote)
		ierr := c.Delete(idxRemote)
		// 404 = 对象本就不在，视作已删（幂等）。
		aOK := aerr == nil || webdav.IsNotFound(aerr)
		iOK := ierr == nil || webdav.IsNotFound(ierr)
		if aOK && iOK {
			deleted++
			continue
		}
		msg := fmt.Sprintf("目标「%s」远端删除未完全成功：", t.Name)
		if !aOK {
			msg += fmt.Sprintf("归档(%v)；", aerr)
		}
		if !iOK {
			msg += fmt.Sprintf("索引(%v)；", ierr)
		}
		warnings = append(warnings, msg)
	}
	// 本地行必删（placement 级联清空）。
	if derr := model.DeleteBackupSnapshot(backupID); derr != nil {
		return deleted, warnings, derr
	}
	return deleted, warnings, nil
}

// ---- 磁盘预检 ----

func (m *Manager) preflightDisk() error {
	dbSize := int64(0)
	if fi, err := os.Stat(filepath.Join(m.env.DataDir, "notepad.db")); err == nil {
		dbSize = fi.Size()
	}
	req := SpaceRequest{
		SnapshotPeak:   dbSize,
		ArchiveStaging: dbSize, // 归档量级 ≈ 库大小（含附件时更大，保守再留固定余量）
		SafetyMargin:   64 << 20,
	}
	return CheckDiskSpace(m.env.DataDir, req)
}

// ---- 主密钥四操作（全局互斥；备份运行中禁止）----

// ExportKey 生成可下载的备份主密钥文本（含 instance_id/kid 供灾难定位）。
func (m *Manager) ExportKey() (string, error) {
	if !m.tryBegin() {
		return "", ErrAlreadyRunning
	}
	defer m.end()
	mk, err := LoadOrCreateMasterKey(m.env.DataDir)
	if err != nil {
		return "", err
	}
	inst, err := LoadOrCreateInstanceID(m.env.DataDir)
	if err != nil {
		return "", err
	}
	return ExportBundle(mk, inst), nil
}

// adoptMaster 用 newMaster 覆盖磁盘主密钥，并把所有已存的 WebDAV 目标凭据从旧主密钥
// 派生的 credential-store 子密钥**重包**到新主密钥，避免换钥后目标密码解不开。
// 调用方须已持运行锁（ImportKey / RegenerateKey 内部调用）。
//
// 真机教训：用户最自然的顺序是"建目标→测通→导入旧主密钥→发现历史备份"，而目标密码是
// 用导入前的主密钥加密的；若换钥不重包凭据，之后 clientFor→DecryptSecret 会
// "message authentication failed"，发现/备份/恢复全部连带失效。
func (m *Manager) adoptMaster(newMaster []byte) error {
	var oldCred []byte
	if om, err := ReadMasterKey(m.env.DataDir); err == nil {
		oldCred, err = crypto.DeriveKey(om, credentialKeyInfo)
		if err != nil {
			return err
		}
	}
	newCred, err := crypto.DeriveKey(newMaster, credentialKeyInfo)
	if err != nil {
		return err
	}
	targets, err := model.ListBackupTargets()
	if err != nil {
		return err
	}
	for _, t := range targets {
		if t.SecretEncrypted == "" {
			continue
		}
		if oldCred == nil {
			return fmt.Errorf("目标 %d 有已存凭据但本机无旧主密钥，无法重包（请重填 WebDAV 密码）", t.ID)
		}
		plain, derr := crypto.DecryptRaw(t.SecretEncrypted, oldCred)
		if derr != nil {
			return fmt.Errorf("重包目标 %d 凭据失败（旧主密钥解不开，可能已换过钥）: %w", t.ID, derr)
		}
		enc, e := crypto.EncryptRaw(plain, newCred)
		if e != nil {
			return e
		}
		t.SecretEncrypted = enc
		if e := model.UpdateBackupTarget(t); e != nil {
			return fmt.Errorf("更新目标 %d 失败: %w", t.ID, e)
		}
	}
	return SetMasterKey(m.env.DataDir, newMaster)
}

// ImportKey 用导出的密钥文本覆盖当前主密钥/instance（同机重建或换盘后接管旧归档）。
// 换钥同时重包已存目标凭据，保证导入后仍能连远端发现/下载/恢复。返回导入后的 kid。
func (m *Manager) ImportKey(text string) (kid string, err error) {
	if !m.tryBegin() {
		return "", ErrAlreadyRunning
	}
	defer m.end()
	mk, inst, err := ParseExportBundle(text)
	if err != nil {
		return "", err
	}
	if err := m.adoptMaster(mk); err != nil {
		return "", err
	}
	if inst != "" {
		if err := SetInstanceID(m.env.DataDir, inst); err != nil {
			return "", err
		}
	}
	return MasterKeyKid(mk), nil
}

// RegenerateKey 生成全新随机主密钥并回写（会令既有归档不可解）。换钥时同样重包目标凭据，
// 保住已存目标可用。破坏性：仅在"明确要废弃历史归档"时使用。
func (m *Manager) RegenerateKey() (kid string, err error) {
	if !m.tryBegin() {
		return "", ErrAlreadyRunning
	}
	defer m.end()
	newKey, err := randomBytes(masterKeyLen)
	if err != nil {
		return "", err
	}
	if err := m.adoptMaster(newKey); err != nil {
		return "", err
	}
	return MasterKeyKid(newKey), nil
}

// ---- 启动期崩溃扫描 ----

// OnStartup 在服务启动时调用：把上次进程残留的 STARTING/RUNNING job 标 INTERRUPTED。
func (m *Manager) OnStartup() (int, error) {
	return model.InterruptStaleJobs()
}

// ---- 错误码 / 工具 ----

func mapWebDAVErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, webdav.ErrAuth) {
		return fmt.Errorf("%w: %v", ErrWebDAVAuth, err)
	}
	if errors.Is(err, webdav.ErrBlockedAddr) {
		return fmt.Errorf("WEBDAV_SSRF_BLOCKED: %w", err)
	}
	return err
}

// errCodeString 从错误链里提取稳定 BACKUP_*/WEBDAV_* 码，供 jobs.error_code。
func errCodeString(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAlreadyRunning):
		return "BACKUP_ALREADY_RUNNING"
	case errors.Is(err, ErrAttachmentMissing):
		return "BACKUP_ATTACHMENT_MISSING"
	case errors.Is(err, ErrDiskInsufficient):
		return "BACKUP_DISK_INSUFFICIENT"
	case errors.Is(err, ErrDatabaseCorrupted):
		return "BACKUP_DATABASE_CORRUPTED"
	case errors.Is(err, ErrBackupTooLarge):
		return "BACKUP_TOO_LARGE"
	case errors.Is(err, ErrWebDAVAuth):
		return "WEBDAV_AUTH_FAILED"
	case errors.Is(err, webdav.ErrBlockedAddr):
		return "WEBDAV_SSRF_BLOCKED"
	case errors.Is(err, ErrDecryptFailed):
		return "BACKUP_DECRYPT_FAILED"
	default:
		return "BACKUP_FAILED"
	}
}

// normalizeDir 保证 WebDAV 目录以 / 开头且以 / 结尾。
func normalizeDir(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p = p + "/"
	}
	return p
}

func nonce() string {
	b, err := randomBytes(6)
	if err != nil {
		return "000000"
	}
	return hex.EncodeToString(b)
}
