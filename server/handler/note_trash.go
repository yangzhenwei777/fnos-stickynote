package handler

import (
	"net/http"
	"strconv"

	"stickynote/app"
	"stickynote/database"
	"stickynote/logger"
	"stickynote/model"

	"github.com/gin-gonic/gin"
)

// note_trash.go — 回收站相关 handler（v21.54 P2：从 note.go 拆分，纯移动不改逻辑）

func ListTrash(c *gin.Context) {
	userID, _ := c.Get("userID")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	result, err := model.ListTrashedNotes(userID.(int64), page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取回收站失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// TrashStats 回收站可视化（v18.8，优-10）：条数 + 回收站笔记引用图片的占用字节数。

func TrashStats(c *gin.Context) {
	userID, _ := c.Get("userID")
	count, uploadBytes, err := model.GetTrashStats(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取回收站统计失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": count, "upload_bytes": uploadBytes})
}

// RestoreNote 从回收站恢复笔记。

func RestoreNote(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}
	// P2 批次1（§129 门禁）：委托 app Facade——事务内 version++ + RESTORE 事件（§59）。
	if err := app.NewServerNoteService(0).RestoreNote(id, userID.(int64)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在或不在回收站中"})
		return
	}
	// v21.107 P2-9：恢复响应携带去向文件夹名（原文件夹已删时模型层已把 folder_id 置 NULL=我的笔记），
	// 前端提示「已恢复到「X」」——此前用户停留在别的文件夹视图时以为笔记丢了。
	folder := "我的笔记"
	if name, ferr := model.GetNoteFolderName(id); ferr == nil && name != "" {
		folder = name
	}
	c.JSON(http.StatusOK, gin.H{"message": "已恢复", "folder": folder})
}

// PermanentDeleteNote 彻底删除回收站中的笔记（不可恢复）。

func PermanentDeleteNote(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}

	// v21.58 F-02: 删除前读取附件清单（note_uploads 随笔记级联删除，删后不可再查）。
	// 此前用 model.GetNote 读 content，但 GetNote 只查 deleted_at IS NULL，
	// 回收站笔记查不到，导致永久删除从不清理附件——本表方案同时修复该缺陷。
	// v21.69 P1-01：读取失败即停止删除（fail-closed），防止「笔记已删、附件关系级联消失、
	// 物理文件永久残留且失去归属」。
	filenames, err := database.ListNoteUploads(id)
	if err != nil {
		logger.Error("PermanentDeleteNote: list attachments failed for note %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取附件关系失败，未执行删除"})
		return
	}

	if err := app.NewServerNoteService(0).PermanentDelete(id, userID.(int64)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在或不在回收站中"})
		return
	}

	if len(filenames) > 0 && uploadDir != "" {
		if cleaned := database.DeleteOrphanUploadFiles(uploadDir, filenames, userID.(int64)); cleaned > 0 {
			logger.Info("PermanentDelete: cleaned %d orphan upload file(s) for note %d", cleaned, id)
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "已彻底删除"})
}

// EmptyTrash 清空当前用户回收站中的所有笔记（永久删除）。

func EmptyTrash(c *gin.Context) {
	userID, _ := c.Get("userID")
	// E7：委托 app Facade——同事务内**删除前**为每条产 PURGE 事件（客户端据此物理清理
	// 本地行），随后批量物理删。此前直调 model 不发事件 → 客户端永留僵尸行。
	deletedCount, uploadNames, err := app.NewServerNoteService(0).EmptyTrash(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清空回收站失败"})
		return
	}
	cleaned := 0
	if len(uploadNames) > 0 && uploadDir != "" {
		cleaned = database.DeleteOrphanUploadFiles(uploadDir, uploadNames, userID.(int64))
	}
	if cleaned > 0 {
		logger.Info("EmptyTrash: cleaned %d orphan upload file(s) from %d notes", cleaned, deletedCount)
	}
	c.JSON(http.StatusOK, gin.H{"message": "回收站已清空", "deleted": deletedCount})
}

// ExportNote 导出单篇笔记为 Markdown。加密笔记需提供密码（?password=）。
