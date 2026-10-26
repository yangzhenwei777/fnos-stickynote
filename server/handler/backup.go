package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"stickynote/backup"
	"stickynote/database"
	"stickynote/logger"
	"stickynote/model"
	"stickynote/restore"
)

// 系统备份管理 API（v1.0.167 切片 2）。全部挂在 admin 组下（middleware.RequireAdmin）。
// 路径约定与既有后台一致：/api/backup/...（不是 /api/admin/...）。
//
// 安全要点：
//   - 明文 WebDAV 密码只在本函数栈内出现，入库前 EncryptSecret；任何响应不回显密码/CA/密文。
//   - 主密钥导出/再生成需二次口令校验（AuthenticateUser），口令验后即弃、不落日志。
//   - 备份执行为异步：run 立即 202 + job_id，前端轮询 jobs/:id。运行中重复触发 → 409。

var backupManager *backup.Manager

// SetBackupManager 在 cmd/server 启动时注入进程级单例。
func SetBackupManager(m *backup.Manager) { backupManager = m }

// restoreController 在线恢复编排器（切片 3）；与 backupManager 共用运行互斥锁。
var restoreController *restore.Controller

// SetRestoreController 在 cmd/server 启动时注入。
func SetRestoreController(c *restore.Controller) { restoreController = c }

func managerReady(c *gin.Context) bool {
	if backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "备份功能未启用"})
		return false
	}
	return true
}

func idParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法 id"})
		return 0, false
	}
	return id, true
}

// ---- targets CRUD ----

type targetRequest struct {
	Name             string `json:"name" binding:"required"`
	Type             string `json:"type"`
	BaseURL          string `json:"base_url" binding:"required"`
	Username         string `json:"username"`
	Password         string `json:"password"` // 明文；留空=更新时保持原密文
	RemotePath       string `json:"remote_path"`
	Enabled          bool   `json:"enabled"`
	RetentionCount   int    `json:"retention_count"`
	ScheduleInterval string `json:"schedule_interval"`
	AllowLoopback    bool   `json:"allow_loopback"`
	AllowInsecureTLS bool   `json:"allow_insecure_tls"`
	CustomCAPem      string `json:"custom_ca_pem"`
}

func (r targetRequest) toTarget(secretEnc string) model.BackupTarget {
	typ := strings.TrimSpace(r.Type)
	if typ == "" {
		typ = "webdav"
	}
	return model.BackupTarget{
		Name: r.Name, Type: typ, BaseURL: strings.TrimSpace(r.BaseURL), Username: r.Username,
		SecretEncrypted: secretEnc, RemotePath: r.RemotePath, Enabled: r.Enabled,
		RetentionCount: r.RetentionCount, ScheduleInterval: r.ScheduleInterval,
		AllowLoopback: r.AllowLoopback, AllowInsecureTLS: r.AllowInsecureTLS, CustomCAPem: r.CustomCAPem,
	}
}

func ListBackupTargets(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	targets, err := model.ListBackupTargets()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取备份目标失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"targets": targets})
}

func CreateBackupTarget(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	var req targetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	secretEnc, err := backupManager.EncryptSecret(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "凭据加密失败"})
		return
	}
	id, err := model.CreateBackupTarget(req.toTarget(secretEnc))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建备份目标失败"})
		return
	}
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "新建备份目标", req.Name, req.BaseURL, c.ClientIP())
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func UpdateBackupTarget(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	existing, err := model.GetBackupTarget(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "目标不存在"})
		return
	}
	var req targetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	secretEnc := "" // 留空 → model 保持原密文
	if req.Password != "" {
		secretEnc, err = backupManager.EncryptSecret(req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "凭据加密失败"})
			return
		}
	}
	t := req.toTarget(secretEnc)
	t.ID = existing.ID
	if err := model.UpdateBackupTarget(t); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新备份目标失败"})
		return
	}
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "更新备份目标", req.Name, req.BaseURL, c.ClientIP())

	// 需求6：保留份数改小 → 立即轮转超量旧档（不必等下次备份）。配置已落库，
	// 故 prune 尽力而为：失败只调整提示文案，绝不回滚/报错（下次备份成功仍会兜底轮转）。
	msg := "已更新"
	if req.RetentionCount < existing.RetentionCount {
		pruned, warns, perr := backupManager.PruneTarget(id)
		switch {
		case perr != nil && errors.Is(perr, backup.ErrAlreadyRunning):
			msg = "已更新；但有备份/恢复操作正在运行，超量旧档将在其结束后或下次备份时清理"
		case perr != nil:
			msg = "已更新；但立即清理超量旧档失败（目标连接/凭据异常），将在下次备份成功时自动轮转：" + perr.Error()
		case len(warns) > 0:
			msg = fmt.Sprintf("已更新；保留份数调小，已清理 %d 份，但 %d 份远端删除失败（下次备份重试）：%s",
				pruned, len(warns), strings.Join(warns, "；"))
		case pruned > 0:
			msg = fmt.Sprintf("已更新；保留份数调小，已立即清理 %d 份超量旧备份", pruned)
		default:
			msg = "已更新；当前备份数未超过新保留份数，无需清理"
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": msg})
}

func DeleteBackupTarget(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	if err := model.DeleteBackupTarget(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "删除备份目标", strconv.FormatInt(id, 10), "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "已删除（远端归档不受影响）"})
}

func TestBackupTarget(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	cap, err := backupManager.TestTarget(id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "supports_move": cap.SupportsMove, "max_object_size": cap.MaxObjectSize})
}

// ---- 执行 ----

func RunBackupNow(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	jobID, bid, err := backupManager.BeginJob(id, "manual")
	if err != nil {
		if errors.Is(err, backup.ErrAlreadyRunning) {
			c.JSON(http.StatusConflict, gin.H{"error": "已有备份/密钥操作在运行，请稍后再试", "code": "BACKUP_ALREADY_RUNNING"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 异步执行；错误写进 job 状态，前端轮询。
	go func() {
		if rerr := backupManager.RunBackupJob(id, "manual", jobID, bid); rerr != nil {
			logger.Error("backup job %d failed: %v", jobID, rerr)
		}
	}()
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "手动备份", bid, fmt.Sprintf("target=%d", id), c.ClientIP())
	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID, "backup_id": bid})
}

func GetBackupJob(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	job, err := model.GetBackupJob(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	c.JSON(http.StatusOK, job)
}

func ListBackupJobs(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	jobs, err := model.ListBackupJobs(50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取任务列表失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"jobs": jobs})
}

// ClearBackupJobs v2.2.1 清除执行记录（管理后台「执行记录」面板【清除记录】按钮）。
// fail-closed：只清已终结记录（SUCCESS/FAILED/INTERRUPTED），运行中的
// （STARTING/RUNNING）保留（前端轮询依赖 + 执行留痕），响应附保留数供 UI 提示。
func ClearBackupJobs(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	deleted, err := model.ClearBackupJobs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清除执行记录失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已清除执行记录", "deleted": deleted})
}

func ListBackupSnapshots(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	rows, err := model.ListBackupSnapshots(100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取快照列表失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"snapshots": rows})
}

// DiscoverSnapshots 扫描所有启用目标的远端目录，把历史归档登记进本地账本，
// 供清空/重装后接管历史备份（之后校验/下载/在线恢复才可用，需已导入正确主密钥）。
func DiscoverSnapshots(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	added, err := backupManager.DiscoverAllEnabled()
	if err != nil && added == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "added": 0, "error": err.Error()})
		return
	}
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "发现远端快照", "", fmt.Sprintf("added=%d", added), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"ok": true, "added": added})
}

func BackupStatus(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"running": backupManager.IsRunning(), "now": time.Now().UTC().Format(time.RFC3339)})
}

// ---- 快照校验 / 下载 ----

func VerifyBackupSnapshot(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	bid := strings.TrimSpace(c.Param("backup_id"))
	if bid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 backup_id"})
		return
	}
	if err := backupManager.VerifyRemote(bid); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func DownloadBackup(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	bid := strings.TrimSpace(c.Param("backup_id"))
	if bid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 backup_id"})
		return
	}
	data, filename, err := backupManager.DownloadArchive(bid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Header("Content-Length", strconv.Itoa(len(data)))
	c.Data(http.StatusOK, "application/octet-stream", data)
}

// DeleteBackupSnapshot 手动删除一份备份：删该 backup_id 在**所有目标**上的远端
// .snbak + .index.json，并清本地快照行（placement 经 FK 级联）。运行中（备份/恢复/密钥）→ 409。
// 远端删除尽力而为：部分目标失败时本地行仍删，失败原因经 warnings 透传前端提示。
func DeleteBackupSnapshot(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	bid := strings.TrimSpace(c.Param("backup_id"))
	if bid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 backup_id"})
		return
	}
	deleted, warnings, err := backupManager.DeleteSnapshot(bid)
	if err != nil {
		if errors.Is(err, backup.ErrAlreadyRunning) {
			c.JSON(http.StatusConflict, gin.H{"error": "已有备份/恢复/密钥操作在运行，请稍后再试", "code": "BACKUP_ALREADY_RUNNING"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败：" + err.Error()})
		return
	}
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "删除备份快照", bid,
		fmt.Sprintf("remote_deleted=%d warnings=%d", deleted, len(warnings)), c.ClientIP())
	logger.AuditEvent("backup_snapshot_delete", c.GetString("username"), c.ClientIP(), "success", "backup_id="+bid)
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted_remote": deleted, "warnings": warnings})
}

// ---- 主密钥四操作 ----

type keyExportRequest struct {
	Password string `json:"password" binding:"required"`
}

// KeyExport 导出主密钥文本（灾难恢复/换机迁移）。需当前管理员口令二次校验。
func KeyExport(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	var req keyExportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需输入当前账号密码确认"})
		return
	}
	if _, err := model.AuthenticateUser(c.GetString("username"), req.Password); err != nil {
		logger.AuditEvent("backup_key_export_denied", c.GetString("username"), c.ClientIP(), "fail", "口令校验未通过")
		c.JSON(http.StatusForbidden, gin.H{"error": "口令校验未通过"})
		return
	}
	text, err := backupManager.ExportKey()
	if err != nil {
		if errors.Is(err, backup.ErrAlreadyRunning) {
			c.JSON(http.StatusConflict, gin.H{"error": "有备份/密钥操作在运行", "code": "BACKUP_ALREADY_RUNNING"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "导出失败"})
		return
	}
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "导出备份主密钥", "", "", c.ClientIP())
	logger.AuditEvent("backup_key_export", c.GetString("username"), c.ClientIP(), "success", "")
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=\"backup_master_key.txt\"")
	c.String(http.StatusOK, text)
}

type keyImportRequest struct {
	Content  string `json:"content" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// KeyImport 导入主密钥文本（接管历史归档）。需口令二次校验。
func KeyImport(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	var req keyImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需输入密钥文件内容与账号密码"})
		return
	}
	if _, err := model.AuthenticateUser(c.GetString("username"), req.Password); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "口令校验未通过"})
		return
	}
	kid, err := backupManager.ImportKey(req.Content)
	if err != nil {
		if errors.Is(err, backup.ErrAlreadyRunning) {
			c.JSON(http.StatusConflict, gin.H{"error": "有备份/密钥操作在运行"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "导入失败：" + err.Error()})
		return
	}
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "导入备份主密钥", kid, "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"kid": kid, "message": "已导入主密钥。若这是清空/重装后的新实例，请到「快照与下载」点「从远端发现历史快照」把历史归档登记进来，之后才能校验/下载/在线恢复。"})
}

type keyRegenRequest struct {
	Password string `json:"password" binding:"required"`
	Confirm  bool   `json:"confirm"`
}

// KeyRegenerate 生成全新主密钥（既有归档将不可解）。需口令 + confirm=true。
func KeyRegenerate(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	var req keyRegenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需输入密码并确认"})
		return
	}
	if !req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须显式确认：再生成会使既有归档不可解"})
		return
	}
	if _, err := model.AuthenticateUser(c.GetString("username"), req.Password); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "口令校验未通过"})
		return
	}
	kid, err := backupManager.RegenerateKey()
	if err != nil {
		if errors.Is(err, backup.ErrAlreadyRunning) {
			c.JSON(http.StatusConflict, gin.H{"error": "有备份/密钥操作在运行"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "再生成失败：" + err.Error()})
		return
	}
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "再生成备份主密钥", kid, "旧归档作废", c.ClientIP())
	logger.AuditEvent("backup_key_regenerate", c.GetString("username"), c.ClientIP(), "success", "kid="+kid)
	c.JSON(http.StatusOK, gin.H{"kid": kid, "message": "已生成新主密钥；历史归档不再可用，请尽快重新备份"})
}

// ---- 在线全量恢复（切片 3）----

type restoreRequest struct {
	BackupID            string `json:"backup_id" binding:"required"`
	Password            string `json:"password" binding:"required"` // 当前管理员口令二次校验
	KeyFileText         string `json:"key_file_text"`               // 可选：粘贴主密钥文件（换实例/灾难恢复）
	AllowIdentityChange bool   `json:"allow_identity_change"`       // 显式允许用别的实例身份整体替换
	Confirm             bool   `json:"confirm"`                     // 显式确认整站覆盖
}

// RestoreFromBackup 触发在线全量恢复：下载归档→抢运行锁→建 restore job→异步跑状态机。
// 返回 202 + job_id；破坏性窗口内其它请求 503（见 RestorationGate），前端以 /health 判完成。
func RestoreFromBackup(c *gin.Context) {
	if !managerReady(c) {
		return
	}
	if restoreController == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "恢复功能未启用"})
		return
	}
	var req restoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：需 backup_id 与当前账号密码"})
		return
	}
	if !req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须显式确认：恢复将用该备份整体覆盖当前全部数据与附件"})
		return
	}
	if _, err := model.AuthenticateUser(c.GetString("username"), req.Password); err != nil {
		logger.AuditEvent("backup_restore_denied", c.GetString("username"), c.ClientIP(), "fail", "口令校验未通过")
		c.JSON(http.StatusForbidden, gin.H{"error": "口令校验未通过"})
		return
	}

	// 锁外先把归档取回（可能上百 MB，耗时），避免长时间占运行锁。
	data, _, err := backupManager.DownloadArchive(req.BackupID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "取回归档失败：" + err.Error()})
		return
	}
	if err := backupManager.Lock(); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "已有备份/恢复/密钥操作在运行，请稍后再试", "code": "BACKUP_ALREADY_RUNNING"})
		return
	}
	// 抢到锁后建 job；此后任何早退都须 Unlock。
	jobID, err := model.CreateBackupJob("restore", 0, req.BackupID)
	if err != nil {
		backupManager.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建恢复任务失败"})
		return
	}
	restoreID := strconv.FormatInt(jobID, 10)

	go func() {
		defer backupManager.Unlock()
		if rerr := restoreController.Run(restore.Request{
			RestoreID: restoreID, BackupID: req.BackupID, ArchiveBytes: data,
			KeyFileText: req.KeyFileText, AllowIdentityChange: req.AllowIdentityChange,
		}); rerr != nil {
			logger.Error("restore job %d failed: %v", jobID, rerr)
			_ = model.FinishJob(jobID, "FAILED", restoreErrCode(rerr), rerr.Error())
		} else {
			_ = model.FinishJob(jobID, "SUCCESS", "", "")
		}
	}()

	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "在线全量恢复", req.BackupID, "job="+restoreID, c.ClientIP())
	logger.AuditEvent("backup_restore_start", c.GetString("username"), c.ClientIP(), "success", req.BackupID)
	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID, "restore_id": restoreID})
}

// RestoreProgress 返回当前是否处于恢复窗口 + 最近相位（内存态，不含敏感数据）。
func RestoreProgress(c *gin.Context) {
	if restoreController == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "恢复功能未启用"})
		return
	}
	state, errMsg := restoreController.Last()
	c.JSON(http.StatusOK, gin.H{
		"restoring": database.IsRestoring(),
		"running":   backupManager != nil && backupManager.IsRunning(),
		"phase":     state,
		"error":     errMsg,
	})
}

func restoreErrCode(err error) string {
	switch {
	case errors.Is(err, restore.ErrRefuseEnv):
		return "REFUSE_FULL_RESTORE"
	case errors.Is(err, restore.ErrIdentityMismatch):
		return "RESTORE_IDENTITY_MISMATCH"
	case errors.Is(err, restore.ErrNoUsableKey):
		return "RESTORE_NO_USABLE_KEY"
	case errors.Is(err, restore.ErrArchiveVerify):
		return "RESTORE_ARCHIVE_VERIFY_FAILED"
	case errors.Is(err, restore.ErrDrainTimeout):
		return "RESTORE_DRAIN_TIMEOUT"
	default:
		return "RESTORE_FAILED"
	}
}
