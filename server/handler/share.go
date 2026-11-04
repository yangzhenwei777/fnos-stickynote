package handler

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"

	"stickynote/logger"
	"stickynote/model"

	"github.com/gin-gonic/gin"
)

func shareNote(c *gin.Context) (*model.Note, bool) {
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

type createShareRequest struct {
	ExpiresInDays int `json:"expires_in_days"`
}

func CreateNoteShare(c *gin.Context) {
	note, ok := shareNote(c)
	if !ok {
		return
	}
	if note.IsEncrypted {
		c.JSON(http.StatusBadRequest, gin.H{"error": "加密笔记不支持分享链接"})
		return
	}
	var req createShareRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
		return
	}
	share, token, err := model.CreateNoteShare(note.UserID, note.ID, req.ExpiresInDays)
	if err != nil {
		if errors.Is(err, model.ErrShareDisabled) {
			c.JSON(http.StatusForbidden, gin.H{"error": "管理员已关闭分享链接功能"})
			return
		}
		if errors.Is(err, model.ErrShareEncrypted) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "加密笔记不支持分享链接"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建分享链接失败"})
		return
	}
	logger.AuditEvent("share_create", strconv.FormatInt(note.UserID, 10), c.ClientIP(), "success",
		"note="+strconv.FormatInt(note.ID, 10)+" share="+strconv.FormatInt(share.ID, 10))
	c.JSON(http.StatusCreated, gin.H{
		"id":         share.ID,
		"token":      token,
		"path":       "/share?t=" + token,
		"expires_at": share.ExpiresAt,
		"created_at": share.CreatedAt,
	})
}

func ListNoteShares(c *gin.Context) {
	note, ok := shareNote(c)
	if !ok {
		return
	}
	items, err := model.ListNoteShares(note.UserID, note.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取分享链接失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"shares": items})
}

func RevokeNoteShare(c *gin.Context) {
	note, ok := shareNote(c)
	if !ok {
		return
	}
	shareID, err := strconv.ParseInt(c.Param("share_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的分享链接ID"})
		return
	}
	if err := model.RevokeNoteShare(note.UserID, note.ID, shareID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "分享链接不存在或已撤销"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "撤销分享链接失败"})
		return
	}
	logger.AuditEvent("share_revoke", strconv.FormatInt(note.UserID, 10), c.ClientIP(), "success",
		"note="+strconv.FormatInt(note.ID, 10)+" share="+strconv.FormatInt(shareID, 10))
	c.JSON(http.StatusOK, gin.H{"message": "已撤销"})
}

// GetSharedNote 公开只读端点：凭 token 访问，不需要登录。
func GetSharedNote(c *gin.Context) {
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.Header("Cache-Control", "no-store")
	note, err := model.ResolveShareToken(c.Param("token"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分享链接不存在、已过期或已撤销"})
		return
	}
	c.JSON(http.StatusOK, note)
}
