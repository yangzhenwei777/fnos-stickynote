package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"stickynote/app"
	"stickynote/model"

	"github.com/gin-gonic/gin"
)

func historyNote(c *gin.Context) (*model.Note, bool) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return nil, false
	}
	note, err := model.GetNote(id, userID.(int64))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在"})
		return nil, false
	}
	if locked, lerr := model.FolderLockedForNote(id, userID.(int64), folderGrants(c)); lerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取笔记失败"})
		return nil, false
	} else if locked {
		respondFolderLocked(c)
		return nil, false
	}
	return note, true
}

func ListNoteHistory(c *gin.Context) {
	note, ok := historyNote(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	resp, err := model.ListNoteHistory(note.UserID, note.ID, page, pageSize)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取历史版本失败"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

func GetNoteHistory(c *gin.Context) {
	note, ok := historyNote(c)
	if !ok {
		return
	}
	hid, err := strconv.ParseInt(c.Param("history_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的历史版本ID"})
		return
	}
	h, err := model.GetNoteHistory(note.UserID, note.ID, hid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "历史版本不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取历史版本失败"})
		return
	}
	c.JSON(http.StatusOK, h)
}

type restoreHistoryRequest struct {
	Version int64 `json:"version"`
}

func RestoreNoteHistory(c *gin.Context) {
	note, ok := historyNote(c)
	if !ok {
		return
	}
	if note.IsEncrypted {
		c.JSON(http.StatusBadRequest, gin.H{"error": "加密笔记暂不支持版本历史"})
		return
	}
	hid, err := strconv.ParseInt(c.Param("history_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的历史版本ID"})
		return
	}
	var req restoreHistoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
		return
	}
	updated, err := app.NewServerNoteService(0).RestoreNoteHistory(note.ID, hid, note.UserID, req.Version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "历史版本不存在"})
			return
		}
		if errors.Is(err, model.ErrVersionConflict) {
			remote := int64(0)
			if n, gerr := model.GetNote(note.ID, note.UserID); gerr == nil {
				remote = n.Version
			}
			c.JSON(http.StatusConflict, gin.H{"error": "笔记已在其他设备修改，请刷新后重试", "remote_version": remote})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "恢复历史版本失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已恢复历史版本", "note": updated, "version": updated.Version})
}
