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

type createNoteRequest struct {
	Title    string `json:"title" binding:"required,max=200"`
	Content  string `json:"content"`
	Tags     string `json:"tags"`
	FolderID *int64 `json:"folder_id"` // v21.86：归属文件夹（null/0 = 默认"我的笔记"）
}

type updateNoteRequest struct {
	Title    string `json:"title" binding:"required,max=200"`
	Content  string `json:"content"`
	Tags     string `json:"tags"`
	Password string `json:"password"` // v20.0：加密笔记编辑时携带密码以支持保存后重加密
	Version  int64  `json:"version"`  // v21.72 P2-08：并发编辑乐观锁版本号
}

func ListNotes(c *gin.Context) {
	userID, _ := c.Get("userID")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	search := c.Query("q")
	tag := c.Query("tag")
	sortBy := c.DefaultQuery("sort_by", "updated_at")

	// v21.86 文件夹过滤：不传 = 全部；default/0 = 默认"我的笔记"；正整数 = 指定文件夹
	// （归属不合法时 folder 条件与 user_id 条件取交集为空列表，无越权泄露，无需额外查询）
	var folderID *int64
	if fv := c.Query("folder_id"); fv != "" {
		if fv == "default" {
			v := int64(0)
			folderID = &v
		} else if n, err := strconv.ParseInt(fv, 10, 64); err == nil && n >= 0 {
			folderID = &n
		}
	}

	result, err := model.ListNotes(userID.(int64), page, pageSize, search, tag, sortBy, folderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取笔记列表失败"})
		return
	}

	c.JSON(http.StatusOK, result)
}

// checkContentLimit 单篇笔记大小上限校验。软上限：max_note_content_kb（默认 0=不限）。硬上限：100MB。
func checkContentLimit(c *gin.Context, content string) bool {
	const hardLimitMB = 100
	hardLimitBytes := hardLimitMB * 1024 * 1024

	// 硬上限（v20.0.1）
	if len(content) > hardLimitBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("笔记内容过大，单篇上限 %dMB", hardLimitMB)})
		return false
	}

	// 软上限（管理员可配）
	limitKB := 0
	if v, err := model.GetConfig("max_note_content_kb"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			limitKB = n
		}
	}
	if limitKB > 0 && len(content) > limitKB*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("笔记内容超过大小限制（%dKB）", limitKB)})
		return false
	}
	return true
}

func CreateNote(c *gin.Context) {
	var req createNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能为空"})
		return
	}
	// v21.75：纯空格标题也拒绝（binding:required 只拦空串，不拦全空格）
	if strings.TrimSpace(req.Title) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能为空"})
		return
	}
	if !checkContentLimit(c, req.Content) {
		return
	}

	userID, _ := c.Get("userID")

	// 每用户笔记数配额（v18.4 落地）：configs.max_notes_per_user 此前只在管理后台
	// 可配却从不生效。只计正常笔记，回收站不占配额；配置缺失/非法时回退默认 1000。
	// v21.58 P1：配额检查移入 model.CreateNote 事务（COUNT+INSERT 原子），消除并发绕过竞态。
	limit := 1000
	if v, err := model.GetConfig("max_notes_per_user"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			limit = n // v21.58 F-04：0 表示不限，传入 model 后由事务内 maxNotes>0 判断
		}
	}

	// v21.86：文件夹归属归一化——null 或 0 视为默认文件夹（传 nil 给 model）
	folderID := req.FolderID
	if folderID != nil && *folderID == 0 {
		folderID = nil
	}

	// P2 批次1（§129 门禁）：写路径委托 app Facade——事务内同产 ChangeLog CREATE 事件。
	note, err := app.NewServerNoteService(limit).CreateNote(userID.(int64), sanitizeNoteTitle(req.Title), sanitizeNoteContent(req.Content), req.Tags, folderID)
	if err != nil {
		if errors.Is(err, model.ErrQuotaExceeded) {
			c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("已达到笔记数量上限（%d 篇），请先清理不需要的笔记", limit)})
			return
		}
		// v21.86：非法文件夹（不存在/不属于当前用户）
		if errors.Is(err, model.ErrInvalidFolder) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "文件夹不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建笔记失败"})
		return
	}

	c.JSON(http.StatusCreated, note)
}

func GetNote(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}

	note, err := model.GetNote(id, userID.(int64))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在"})
		return
	}

	c.JSON(http.StatusOK, note)
}

func UpdateNote(c *gin.Context) {
	var req updateNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能为空"})
		return
	}
	// v21.75：纯空格标题也拒绝（binding:required 只拦空串，不拦全空格）
	if strings.TrimSpace(req.Title) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能为空"})
		return
	}

	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}
	if !checkContentLimit(c, req.Content) {
		return
	}

	// 加密笔记编辑（v20.0）：验证密码 → 解密 → 更新 → 重加密 → 落库。
	// 不验证密码的加密笔记直接拒绝（前端应先要求密码）。
	note, _ := model.GetNote(id, userID.(int64))
	if note != nil && note.IsEncrypted {
		if req.Password == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "加密笔记编辑需要密码"})
			return
		}
		// P2 批次1·刀2（§129 门禁）：委托 app Facade——事务内 CAS + UPDATE 事件同提交（§60）。
		_, err := app.NewServerNoteService(0).UpdateEncrypted(id, userID.(int64), sanitizeNoteTitle(req.Title), req.Tags, sanitizeNoteContent(req.Content), req.Password, req.Version)
		if err != nil {
			if errors.Is(err, model.ErrVersionConflict) { // v21.72 P2-08
				remote := int64(0)
				if n, gerr := model.GetNote(id, userID.(int64)); gerr == nil {
					remote = n.Version
				}
				c.JSON(http.StatusConflict, gin.H{"error": "笔记已在其他设备修改，请刷新后重试", "remote_version": remote})
				return
			}
			encryptError(c, err)
			return
		}
		// 返回更新后的笔记内容（解密态，前端直接展示）
		updated, _ := model.GetNote(id, userID.(int64))
		if updated != nil {
			updated.Content = sanitizeNoteContent(req.Content)
			updated.IsEncrypted = true
		}
		c.JSON(http.StatusOK, updated)
		return
	}

	// P2 批次1：普通更新委托 app Facade（事务内 CAS + UPDATE 事件同提交）。
	updated, err := app.NewServerNoteService(0).UpdateNote(id, userID.(int64), sanitizeNoteTitle(req.Title), sanitizeNoteContent(req.Content), req.Tags, req.Version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在或已被删除"})
			return
		}
		if errors.Is(err, model.ErrVersionConflict) { // v21.72 P2-08
			// §41 push 契约对齐：409 响应体带 remote_version，前端可据此刷新冲突基点
			remote := int64(0)
			if n, gerr := model.GetNote(id, userID.(int64)); gerr == nil {
				remote = n.Version
			}
			c.JSON(http.StatusConflict, gin.H{"error": "笔记已在其他设备修改，请刷新后重试", "remote_version": remote})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新笔记失败"})
		return
	}

	// v21.72 P2-08：返回更新后的 version，前端据此同步本地乐观锁版本号，避免下次保存误报冲突
	// v21.150 E7：增补 updated_at（列表排序键唯一时间源，前端不再用客户端时钟写键）
	c.JSON(http.StatusOK, gin.H{"message": "更新成功", "version": updated.Version, "updated_at": updated.UpdatedAt})
}

func DeleteNote(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}

	// v21.67 P2-01：移除 handler 层的旧 content 正则缓存——DeleteNote 内部已
	// 统一用 note_uploads 计算 trash_upload_bytes，避免两套逻辑算出的值互相覆盖。
	// P2 批次1（§129 门禁）：委托 app Facade——事务内 version++ + DELETE 墓碑事件（§58）。
	if err := app.NewServerNoteService(0).DeleteNote(id, userID.(int64)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在或已被删除"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除笔记失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}

func GetAllTags(c *gin.Context) {
	userID, _ := c.Get("userID")
	tags, err := model.GetAllTags(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取标签失败"})
		return
	}
	if tags == nil {
		tags = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"tags": tags})
}

// ListTrash 回收站列表（已软删除的笔记，按删除时间倒序；v18.7 起分页）。
func BatchDeleteNotes(c *gin.Context) {
	userID, _ := c.Get("userID")
	var req batchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供要删除的笔记 ID 列表"})
		return
	}

	// P2 批次1（§129 门禁）：委托 app Facade——单事务逐行 version++ + DELETE 墓碑事件
	// （SOURCE-FACT #6：消除原逐行独立事务的部分删除中间态，对齐 EmptyTrash v21.95 模式）。
	deleted, err := app.NewServerNoteService(0).BatchDeleteNotes(userID.(int64), req.IDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量删除失败"})
		return
	}
	logger.AuditEvent("note_batch_delete", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("count=%d total=%d", deleted, len(req.IDs)))
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已删除 %d 篇笔记", deleted), "deleted": deleted, "total": len(req.IDs)})
}

// BatchExportNotes 批量导出指定笔记为 zip。
