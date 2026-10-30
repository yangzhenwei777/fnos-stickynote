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

// folder_encrypt.go —— v2.3.0 文件夹访问锁 API。
//
// PUT    /api/folders/:id/encrypt  设置密码并锁定
// DELETE /api/folders/:id/encrypt  校验密码并永久解除锁定
// POST   /api/folders/:id/unlock   校验密码并签发本会话解锁令牌
//
// 与单篇笔记加密一致：只做密码校验与展示过滤，不改变笔记正文。

// folderUnlockGuard 文件夹密码防爆破（维度：文件夹 + IP，独立于登录/笔记 guard）。
var folderUnlockGuard = &loginGuard{fails: make(map[string]*failRecord)}

func folderEncryptError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrFolderNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "文件夹不存在"})
	case errors.Is(err, model.ErrFolderAlreadyEncrypted):
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件夹已加密，请先解密"})
	case errors.Is(err, model.ErrFolderNotEncrypted):
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件夹未加密"})
	case errors.Is(err, app.ErrWrongPassword):
		c.JSON(http.StatusForbidden, gin.H{"error": "密码错误"})
	case errors.Is(err, app.ErrPasswordMismatch),
		errors.Is(err, app.ErrPasswordTooLong),
		errors.Is(err, app.ErrPasswordInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
	}
}

func parseFolderID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的文件夹ID"})
		return 0, false
	}
	return id, true
}

func EncryptFolder(c *gin.Context) {
	id, ok := parseFolderID(c)
	if !ok {
		return
	}
	var req encryptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	userID, _ := c.Get("userID")
	version, err := app.NewServerFolderService().Encrypt(id, userID.(int64), req.Password, req.ConfirmPassword)
	if err != nil {
		folderEncryptError(c, err)
		return
	}
	logger.AuditEvent("folder_encrypt", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("folder_id=%d", id))
	c.JSON(http.StatusOK, gin.H{"message": "加密成功", "is_encrypted": true, "version": version})
}

func DecryptFolder(c *gin.Context) {
	id, ok := parseFolderID(c)
	if !ok {
		return
	}
	guardKey := fmt.Sprintf("folder:%d", id)
	if locked, until := folderUnlockGuard.locked(guardKey, c.ClientIP()); locked {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("密码尝试次数过多", until)})
		return
	}
	var req decryptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	userID, _ := c.Get("userID")
	version, err := app.NewServerFolderService().Decrypt(id, userID.(int64), req.Password)
	if err != nil {
		if errors.Is(err, app.ErrWrongPassword) {
			folderUnlockGuard.fail(guardKey, c.ClientIP())
		}
		folderEncryptError(c, err)
		return
	}
	folderUnlockGuard.success(guardKey, c.ClientIP())
	logger.AuditEvent("folder_decrypt", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("folder_id=%d", id))
	c.JSON(http.StatusOK, gin.H{"message": "解密成功", "is_encrypted": false, "version": version})
}

func UnlockFolder(c *gin.Context) {
	id, ok := parseFolderID(c)
	if !ok {
		return
	}
	guardKey := fmt.Sprintf("folder:%d", id)
	if locked, until := folderUnlockGuard.locked(guardKey, c.ClientIP()); locked {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("密码尝试次数过多", until)})
		return
	}
	var req decryptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	userID, _ := c.Get("userID")
	hash, _, err := app.NewServerFolderService().Unlock(id, userID.(int64), req.Password)
	if err != nil {
		if errors.Is(err, app.ErrWrongPassword) {
			folderUnlockGuard.fail(guardKey, c.ClientIP())
		}
		folderEncryptError(c, err)
		return
	}
	folderUnlockGuard.success(guardKey, c.ClientIP())
	token, exp, err := issueFolderGrant(c, id, hash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发解锁凭证失败"})
		return
	}
	logger.AuditEvent("folder_unlock", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("folder_id=%d", id))
	c.JSON(http.StatusOK, gin.H{"unlock_token": token, "expires_at": exp.Unix()})
}
