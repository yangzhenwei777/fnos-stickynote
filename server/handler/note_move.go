package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"stickynote/app"
	"stickynote/logger"
	"stickynote/model"

	"github.com/gin-gonic/gin"
)

// v21.87 笔记移动/复制到文件夹（用户需求：笔记菜单新增【移动到】【复制到】）。
// handler/note.go 已 294 行，本组接口独立成文件避免继续膨胀（纪律 5.1）。

// noteFolderRequest 移动/复制到文件夹的请求体。
type noteFolderRequest struct {
	FolderID *int64 `json:"folder_id"` // null/0 = 默认"我的笔记"（与 v21.86 CreateNote 归一化口径一致）
}

// normalizeNoteFolderID 归一化：null 或 0 → nil（默认"我的笔记"）。
func normalizeNoteFolderID(p *int64) *int64 {
	if p != nil && *p == 0 {
		return nil
	}
	return p
}

// parseNoteID 从路由参数解析笔记 ID。
func parseNoteID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return 0, false
	}
	return id, true
}

// noteFolderError 统一错误映射：文件夹非法→400、笔记不存在→404、配额超限→403。
func noteFolderError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, model.ErrInvalidFolder):
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件夹不存在"})
	case errors.Is(err, sql.ErrNoRows):
		c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在"})
	case errors.Is(err, model.ErrQuotaExceeded):
		c.JSON(http.StatusForbidden, gin.H{"error": "已达到笔记数量上限，无法复制"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": action + "失败"})
	}
}

// MoveNoteToFolder PUT /notes/:id/folder — 把笔记移动到目标文件夹。
func MoveNoteToFolder(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, ok := parseNoteID(c)
	if !ok {
		return
	}
	var req noteFolderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	folderID := normalizeNoteFolderID(req.FolderID)

	// P2 批次1（§129 门禁）：委托 app Facade——事务内 version++（§57）+ UPDATE 事件同提交。
	// 响应携带新 version：移动会推进服务端版本，持有该笔记编辑态的客户端须同步乐观锁基点。
	newVersion, err := app.NewServerNoteService(0).MoveNoteToFolder(id, userID.(int64), folderID)
	if err != nil {
		noteFolderError(c, err, "移动")
		return
	}
	logger.AuditEvent("note_move", c.GetString("username"), c.ClientIP(), "success",
		fmt.Sprintf("note=%d folder=%v", id, folderID))
	c.JSON(http.StatusOK, gin.H{"message": "移动成功", "version": newVersion})
}

// CopyNoteToFolder POST /notes/:id/copy — 复制笔记到目标文件夹（新建副本）。
func CopyNoteToFolder(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, ok := parseNoteID(c)
	if !ok {
		return
	}
	var req noteFolderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	folderID := normalizeNoteFolderID(req.FolderID)

	// 配额（与 CreateNote 同口径：配置缺失/非法回退 1000，0=不限）
	limit := 1000
	if v, err := model.GetConfig("max_notes_per_user"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			limit = n
		}
	}

	// P2 批次1（§129 门禁）：委托 app Facade——新副本带独立 sync_id + CREATE 事件同提交。
	note, err := app.NewServerNoteService(limit).CopyNoteToFolder(id, userID.(int64), folderID)
	if err != nil {
		noteFolderError(c, err, "复制")
		return
	}
	logger.AuditEvent("note_copy", c.GetString("username"), c.ClientIP(), "success",
		fmt.Sprintf("note=%d new_note=%d folder=%v", id, note.ID, folderID))
	c.JSON(http.StatusCreated, note)
}
