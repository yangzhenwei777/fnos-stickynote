package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"stickynote/app"
	"stickynote/logger"
	"stickynote/model"

	"github.com/gin-gonic/gin"
)

// v21.86 文件夹 API。数据模型见 model/folder.go：
// "我的笔记"为虚拟默认文件夹（不入库），自定义文件夹存 note_folders 表。

type createFolderRequest struct {
	Name string `json:"name" binding:"required,max=64"`
}

type updateFolderRequest struct {
	Name     string `json:"name"`     // 重命名（与 position 二选一或同时）
	Position string `json:"position"` // up / down / top（移动排序）
}

// ListFolders GET /api/folders — 默认项在前 + 自定义项按 sort_order
func ListFolders(c *gin.Context) {
	userID, _ := c.Get("userID")
	folders, err := model.ListFolders(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取文件夹失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"folders": folders})
}

// CreateFolder POST /api/folders
func CreateFolder(c *gin.Context) {
	var req createFolderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件夹名称不能为空"})
		return
	}
	userID, _ := c.Get("userID")
	// P1 批次2（§129 门禁）：委托 app Facade——事务内 CREATE 事件同提交（§18）。
	folder, err := app.NewServerFolderService().Create(userID.(int64), req.Name)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrFolderNameExists):
			c.JSON(http.StatusConflict, gin.H{"error": "文件夹名称已存在"})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}
	logger.AuditEvent("folder_create", c.GetString("username"), c.ClientIP(), "success", "name="+folder.Name)
	c.JSON(http.StatusCreated, folder)
}

// UpdateFolder PUT /api/folders/:id — 重命名 / 移动排序
func UpdateFolder(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的文件夹ID"})
		return
	}
	var req updateFolderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}
	userID, _ := c.Get("userID")

	if req.Name != "" {
		// P1 批次2：委托 Facade——事务内 version++ + FOLDER UPDATE 事件（§18）。
		if err := app.NewServerFolderService().Rename(id, userID.(int64), req.Name); err != nil {
			switch {
			case errors.Is(err, model.ErrFolderNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "文件夹不存在"})
			case errors.Is(err, model.ErrFolderNameExists):
				c.JSON(http.StatusConflict, gin.H{"error": "文件夹名称已存在"})
			default:
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			}
			return
		}
		logger.AuditEvent("folder_rename", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("id=%d name=%s", id, req.Name))
	}

	if req.Position != "" {
		// P1 批次2：委托 Facade——变化行逐 folder version++，整次移动产单条 FOLDER_ORDER 事件
		// （§9/§55 单一 seq 整批原子；次序无变化 = 零事件幂等）。
		if err := app.NewServerFolderService().Move(id, userID.(int64), req.Position); err != nil {
			if errors.Is(err, model.ErrFolderNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "文件夹不存在"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logger.AuditEvent("folder_move", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("id=%d pos=%s", id, req.Position))
	}

	c.JSON(http.StatusOK, gin.H{"message": "更新成功"})
}

// DeleteFolder DELETE /api/folders/:id
// 非空文件夹返回 409 + code=FOLDER_NOT_EMPTY（前端弹窗只显示"确定"按钮）；
// 空文件夹删除时连带清理回收站软删笔记的悬空引用。
func DeleteFolder(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的文件夹ID"})
		return
	}
	userID, _ := c.Get("userID")
	// P1 批次2：委托 Facade——FOLDER DELETE 墓碑 + 回收站受影响笔记逐行 UPDATE 事件（§56/§143）。
	if err := app.NewServerFolderService().Delete(id, userID.(int64)); err != nil {
		switch {
		case errors.Is(err, model.ErrFolderNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "文件夹不存在"})
		case errors.Is(err, model.ErrFolderNotEmpty):
			c.JSON(http.StatusConflict, gin.H{"error": "文件夹内还有笔记，无法删除。", "code": "FOLDER_NOT_EMPTY"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "删除文件夹失败"})
		}
		return
	}
	logger.AuditEvent("folder_delete", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("id=%d", id))
	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}
