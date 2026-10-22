package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"stickynote/model"
)

// GetQuota 返回当前用户的用量与上限（v19.1 配额可视化）。
// 数据全部来自已有 model 层：CountActiveNotes / SumUploadSizeByUser / GetConfig。
func GetQuota(c *gin.Context) {
	userID, _ := c.Get("userID")

	notesCount, err := model.CountActiveNotes(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取笔记统计失败"})
		return
	}

	notesLimit := 1000
	if v, err := model.GetConfig("max_notes_per_user"); err == nil {
		if n, e := strconv.Atoi(v); e == nil && n >= 0 {
			notesLimit = n // v21.58 F-04：0 表示不限，如实返回 0（而非默认 1000）
		}
	}

	storageBytes, err := model.SumUploadSizeByUser(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取存储统计失败"})
		return
	}

	storageLimitMB := 1024
	if v, err := model.GetConfig("max_storage_mb_per_user"); err == nil {
		if n, e := strconv.Atoi(v); e == nil && n >= 0 {
			storageLimitMB = n // v21.58 F-04：0 表示不限，如实返回 0
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"notes_count":      notesCount,
		"notes_limit":      notesLimit,
		"storage_bytes":    storageBytes,
		"storage_limit_mb": storageLimitMB,
	})
}
