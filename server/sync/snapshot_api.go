package sync

// snapshot_api.go —— 批次 5 · 刀 2：Bootstrap 快照 HTTP 面。
//
//	POST /api/sync/snapshot          → 202 {snapshot_id,status}（在途复用同 id）
//	GET  /api/sync/snapshot/:id      → 状态轮询（仅本人；admin 可见全部）
//	GET  /api/sync/snapshot/:id/download → READY 后 zip 流式下载（本人）
//
// 错误语义：429 SNAP_USER_CAP / 503 SNAP_GLOBAL_FULL / 404 / 403。
// 快照 zip 是**投影后的单用户数据**，本人下载无越权面；sha256 在账本，桌面下载后校验。

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"

	"stickynote/logger"
	"stickynote/model"
)

func snapshotActor(c *gin.Context) (int64, bool, bool) {
	v, ok := c.Get("userID")
	if !ok {
		return 0, false, false
	}
	uid, ok2 := v.(int64)
	role, _ := c.Get("role")
	isAdmin, _ := role.(string)
	return uid, ok2, isAdmin == "admin"
}

// SnapshotCreate 本人请求一次 Bootstrap 快照（异步构建，轮询取结果）。
func SnapshotCreate(c *gin.Context) {
	uid, ok, _ := snapshotActor(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}
	username := c.GetString("username")
	s, err := RequestSnapshot(uid, username)
	switch {
	case errors.Is(err, ErrSnapUserCap):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "SNAP_USER_CAP"})
	case errors.Is(err, ErrSnapGlobalCap):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SNAP_GLOBAL_FULL"})
	case errors.Is(err, ErrSnapshotNotConfigured):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SNAPSHOT_UNCONFIGURED"})
	case err != nil:
		logger.Error("SnapshotCreate user=%d: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "快照创建失败"})
	default:
		c.JSON(http.StatusAccepted, gin.H{"snapshot_id": s.ID, "status": s.Status})
	}
}

// loadSnapshotForActor 归属校验：本人或 admin。
func loadSnapshotForActor(c *gin.Context) (*model.SyncSnapshot, bool) {
	uid, ok, isAdmin := snapshotActor(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return nil, false
	}
	s, err := model.GetSyncSnapshot(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "SNAPSHOT_NOT_FOUND"})
		return nil, false
	}
	if s.UserID != uid && !isAdmin {
		// 不区分"不存在/无权限"（防快照 id 枚举 oracle），统一 404。
		c.JSON(http.StatusNotFound, gin.H{"error": "SNAPSHOT_NOT_FOUND"})
		return nil, false
	}
	return s, true
}

// SnapshotStatus 构建进度/就绪元数据。
func SnapshotStatus(c *gin.Context) {
	s, ok := loadSnapshotForActor(c)
	if !ok {
		return
	}
	body := gin.H{"snapshot_id": s.ID, "status": s.Status, "server_epoch": s.ServerEpoch,
		"watermark": s.Watermark, "instance_id": s.InstanceID}
	switch s.Status {
	case model.SnapshotReady:
		body["sha256"] = s.Sha256
		body["size_bytes"] = s.SizeBytes
		body["file_count"] = s.FileCount
		body["expires_at"] = s.ExpiresAt
		body["download_url"] = "/api/sync/snapshot/" + s.ID + "/download"
	case model.SnapshotFailed:
		body["error"] = s.Error
	}
	c.JSON(http.StatusOK, body)
}

// SnapshotDownload READY zip 流式下载（本人）。
func SnapshotDownload(c *gin.Context) {
	s, ok := loadSnapshotForActor(c)
	if !ok {
		return
	}
	path, err := SnapshotPathForDownload(s)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = model.MarkSnapshotMissingFile(s.ID)
			c.JSON(http.StatusNotFound, gin.H{"error": "SNAPSHOT_NOT_FOUND"})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "SNAPSHOT_NOT_READY", "status": s.Status})
		return
	}
	c.Header("Content-Disposition", "attachment; filename=\""+filepath.Base(path)+"\"")
	c.Header("Content-Type", "application/zip")
	c.Header("X-Snapshot-Sha256", s.Sha256)
	c.Header("X-Snapshot-Watermark", strconv.FormatInt(s.Watermark, 10))
	c.File(path)
}

// InvalidateBootstrapSnapshots 恢复换代联动（cmd/server 经 restore.OnEpochCommitted 钩子调用）：
// 活跃快照全部 INVALIDATED + 释放 Hold + 删文件（旧锚作废，防分发跨代数据）。
func InvalidateBootstrapSnapshots() {
	rows, err := model.InvalidateAllActiveSnapshots()
	if err != nil {
		logger.Error("InvalidateBootstrapSnapshots: %v", err)
		return
	}
	for _, r := range rows {
		_ = model.ReleaseSnapshotHolds(r.ID)
		if r.FilePath != "" {
			if rmErr := os.Remove(r.FilePath); rmErr != nil && !os.IsNotExist(rmErr) {
				logger.Error("InvalidateBootstrapSnapshots remove %s: %v", r.FilePath, rmErr)
			}
		}
	}
	if len(rows) > 0 {
		logger.Info("restore epoch bump: invalidated %d bootstrap snapshots", len(rows))
	}
}
