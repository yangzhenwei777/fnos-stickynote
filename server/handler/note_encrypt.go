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

// note_encrypt.go — 加密相关 handler（v21.54 P2：从 note.go 拆分）。
// P2 批次1·刀2（§129 门禁）：写路径（Encrypt/Decrypt）委托 app Facade——
// server 模式事务内 CAS + version++ + UPDATE 事件同提交（§60）；VerifyEncrypt 为只读
// 校验（不产事件），保留防爆破守卫（IP 维度）在 handler 侧。

// encryptError 加密域错误统一映射（errors.Is 判类，禁止匹配文案）。
func encryptError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, app.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在"})
	case errors.Is(err, model.ErrAlreadyEncrypted):
		c.JSON(http.StatusBadRequest, gin.H{"error": "笔记已加密，请先解密"})
	case errors.Is(err, model.ErrNotEncrypted):
		c.JSON(http.StatusBadRequest, gin.H{"error": "笔记未加密"})
	case errors.Is(err, app.ErrWrongPassword):
		c.JSON(http.StatusForbidden, gin.H{"error": "密码错误"})
	case errors.Is(err, app.ErrPasswordMismatch),
		errors.Is(err, app.ErrPasswordTooLong),
		errors.Is(err, app.ErrPasswordInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, app.ErrCorruptPayload):
		c.JSON(http.StatusInternalServerError, gin.H{"error": "解密失败，数据可能已损坏"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
	}
}

func EncryptNote(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}

	var req encryptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// §60：加密推进服务端 version——响应携带新版本供客户端同步乐观锁基点。
	newVersion, err := app.NewServerNoteService(0).EncryptNote(id, userID.(int64), req.Password, req.ConfirmPassword)
	if err != nil {
		encryptError(c, err)
		return
	}

	logger.AuditEvent("note_encrypt", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("note_id=%d", id))
	c.JSON(http.StatusOK, gin.H{"message": "加密成功", "is_encrypted": true, "version": newVersion})
}

// DecryptNote 移除笔记加密（v20.0）。需要验证原密码。
// DELETE /api/notes/:id/encrypt

func DecryptNote(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}

	var req decryptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	plaintext, err := app.NewServerNoteService(0).DecryptNote(id, userID.(int64), req.Password)
	if err != nil {
		encryptError(c, err)
		return
	}

	logger.AuditEvent("note_decrypt", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("note_id=%d", id))
	c.JSON(http.StatusOK, gin.H{"message": "解密成功", "is_encrypted": false, "content": plaintext})
}

// VerifyEncrypt 验证加密笔记密码，返回解密后的完整内容（v20.0）。
// POST /api/notes/:id/verify-encrypt

func VerifyEncrypt(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}

	guardKey := fmt.Sprintf("%d|%s", id, c.ClientIP())
	if locked, until := encryptGuard.locked(guardKey, c.ClientIP()); locked {
		logger.AuditEvent("note_encrypt_verify", c.GetString("username"), c.ClientIP(), "blocked", fmt.Sprintf("note_id=%d locked until %s", id, until.Format("15:04:05")))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": lockRetryMessage("密码尝试次数过多", until)})
		return
	}

	var req verifyEncryptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	note, err := app.NewServerNoteService(0).VerifyEncrypt(id, userID.(int64), req.Password)
	if err != nil {
		if errors.Is(err, app.ErrWrongPassword) {
			encryptGuard.fail(guardKey, c.ClientIP())
			c.JSON(http.StatusForbidden, gin.H{"error": "密码错误"})
			return
		}
		if errors.Is(err, app.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在或未加密"})
			return
		}
		encryptError(c, err)
		return
	}

	encryptGuard.success(guardKey, c.ClientIP())

	logger.AuditEvent("note_encrypt_verify", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("note_id=%d", id))
	c.JSON(http.StatusOK, note)
}

// ---- v20.7 批量操作 ----

type batchRequest struct {
	// v21.90 P1（M-07 审计核对）：补 max=200——无上限的 ids 数组可塞海量 int64，
	// 逼近 SQLITE_MAX_VARIABLE_NUMBER 或拖垮 MaxOpenConns=2。
	IDs []int64 `json:"ids" binding:"required,min=1,max=200"`
}
