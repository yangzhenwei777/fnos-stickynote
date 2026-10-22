package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	"stickynote/database"
	"stickynote/logger"
	"stickynote/model"
)

type createUserRequest struct {
	Username string `json:"username" binding:"required,min=3,max=50"`
	Password string `json:"password" binding:"required,min=6"` // v21.96：8 → 6，与注册路径统一（见 minPasswordLen）
	Role     string `json:"role" binding:"omitempty,oneof=admin user"`
}

type updateUserRequest struct {
	Role     string `json:"role" binding:"omitempty,oneof=admin user"`
	Password string `json:"password" binding:"omitempty,min=6"` // v21.96：8 → 6
}

func ListUsers(c *gin.Context) {
	users, err := model.ListUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取用户列表失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

func CreateUser(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		bindError(c, err)
		return
	}

	// 检查是否已有管理员
	adminCount := 0
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM users WHERE role = 'admin'").Scan(&adminCount); err != nil {
		logger.Error("CreateUser: count admin: %v", err) // v21.58 F-16
		c.JSON(http.StatusInternalServerError, gin.H{"error": "检查管理员失败"})
		return
	}
	if adminCount > 0 && req.Role == "admin" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "已存在管理员，无法创建新管理员"})
		return
	}

	// 默认角色为普通用户
	if req.Role == "" {
		req.Role = "user"
	}

	user, err := model.CreateUser(req.Username, req.Password, "", "", req.Role)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
		return
	}

	// v21.96：弱密码放行，强度分级随响应回传供前端提示（不拦截）。
	// 采用「user 字段平铺 + password_strength」而非嵌套进 "user" 键，
	// 是为了不破坏既有调用方对 data.id / data.username 的顶层读取。
	strength := passwordStrengthLevel(req.Password)
	resp := gin.H{"password_strength": strength}
	if raw, mErr := json.Marshal(user); mErr == nil {
		var fields map[string]interface{}
		if uErr := json.Unmarshal(raw, &fields); uErr == nil {
			for k, v := range fields {
				resp[k] = v
			}
		} else { // 序列化兜底：极端情况下退化为嵌套结构，保证接口不 500
			resp["user"] = user
		}
	} else {
		resp["user"] = user
	}

	logger.AuditEvent("user_create", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("created %s (role: %s) pwd_strength: %s", req.Username, req.Role, strength))
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "创建用户", req.Username, "角色="+req.Role, c.ClientIP())
	c.JSON(http.StatusCreated, resp)
}

func UpdateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户ID"})
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// 获取当前用户信息
	currentUserID, _ := c.Get("userID")
	isCurrentUser := id == currentUserID.(int64)
	currentUser, _ := model.GetUserByID(currentUserID.(int64))

	// 如果是当前登录管理员尝试将自己改为普通用户，拒绝
	if isCurrentUser && currentUser != nil && currentUser.Role == "admin" && req.Role == "user" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能将自己降为普通用户"})
		return
	}

	// 检查是否已有管理员，防止通过编辑将唯一管理员改为普通用户
	if req.Role == "user" {
		adminCount := 0
		if err := database.DB.QueryRow("SELECT COUNT(*) FROM users WHERE role = 'admin'").Scan(&adminCount); err != nil {
			logger.Error("UpdateUser: count admin: %v", err) // v21.58 F-16
			c.JSON(http.StatusInternalServerError, gin.H{"error": "检查管理员失败"})
			return
		}
		user, _ := model.GetUserByID(id)
		if adminCount == 1 && user != nil && user.Role == "admin" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不能移除唯一管理员的角色"})
			return
		}
	}

	// 唯一管理员约束（v18.4 堵上）：与 CreateUser 保持一致——此前 CreateUser
	// 拒绝创建第二个 admin，但 UpdateUser 可直接提升任意用户为 admin 绕过限制。
	if req.Role == "admin" {
		adminCount := 0
		if err := database.DB.QueryRow("SELECT COUNT(*) FROM users WHERE role = 'admin'").Scan(&adminCount); err != nil {
			logger.Error("UpdateUser: count admin: %v", err) // v21.58 F-16
			c.JSON(http.StatusInternalServerError, gin.H{"error": "检查管理员失败"})
			return
		}
		user, _ := model.GetUserByID(id)
		if adminCount > 0 && (user == nil || user.Role != "admin") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "已存在管理员，无法将用户提升为管理员"})
			return
		}
	}

	// v21.66 P2：角色与密码在同一事务内更新，消除「角色已改、密码失败」的半成功状态
	if req.Role != "" || req.Password != "" {
		if err := model.UpdateUserTx(id, req.Role, req.Password); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "更新用户失败"})
			return
		}
	}

	logger.AuditEvent("user_update", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("target id=%d", id))
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "修改用户", strconv.FormatInt(id, 10), "修改角色", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "更新成功"})
}

func DeleteUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户ID"})
		return
	}

	currentUserID, _ := c.Get("userID")
	if id == currentUserID.(int64) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除自己"})
		return
	}

	// 唯一管理员删除兜底（v18.8，P1-3 冗余保险）：唯一 admin 约束 + 禁删自己
	// 在语义上已使"删光最后一个 admin"不可达，此处显式检查是防御未来改动
	// 破坏该不变量的冗余保险——不替代也不改变既有约束。
	target, _ := model.GetUserByID(id)
	if target != nil && target.Role == "admin" {
		adminCount := 0
		if err := database.DB.QueryRow("SELECT COUNT(*) FROM users WHERE role = 'admin'").Scan(&adminCount); err != nil {
			logger.Error("DeleteUser: count admin: %v", err) // v21.58 F-16
			c.JSON(http.StatusInternalServerError, gin.H{"error": "检查管理员失败"})
			return
		}
		if adminCount <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除唯一管理员"})
			return
		}
	}

	// 先取该用户全部上传文件清单（v18.5）：用户删除后 uploads 记录随 CASCADE 清除，
	// 届时将无从得知哪些文件属于他——必须在删除前取出。
	// v21.68 P1-01：拿不到完整清单就不删用户，防止「用户已删、文件残留且不入清理队列」。
	uploadFiles, err := model.ListUploadsByUser(id)
	if err != nil {
		logger.Error("DeleteUser: list uploads failed for user %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取用户附件失败，未执行删除"})
		return
	}

	if err := model.DeleteUser(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除用户失败"})
		return
	}

	// 删账号=删干净（用户确认语义）：该账号的上传文件无条件物理删除，
	// 不做跨用户引用保护。文件删除失败不阻断请求（用户已删），但记日志并入队兜底。
	cleaned := 0
	for _, name := range uploadFiles {
		if err := os.Remove(filepath.Join(uploadDir, name)); err != nil {
			logger.Error("DeleteUser: remove upload failed: %s: %v", name, err)
			// v21.67 P1-03：删除失败入队，由 CleanPendingUploads 兜底重试；
			// 配合 ServeUpload fail-closed（owner==0 拒绝），残留文件不可被访问。
			if qerr := model.QueueUploadCleanup(name, id); qerr != nil {
				logger.Error("DeleteUser: queue cleanup failed for %s: %v", name, qerr)
			}
			continue
		}
		cleaned++
	}

	// v21.70 P3-01：删除用户后回收其上传锁，避免 map 项永久累积
	releaseUserUploadLock(id)

	logger.AuditEvent("user_delete", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("target id=%d, uploads cleaned: %d", id, cleaned))
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "删除用户", strconv.FormatInt(id, 10), fmt.Sprintf("清理 %d 文件", cleaned), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "删除成功", "uploads_cleaned": cleaned})
}

// ResetPassword 管理员重置用户密码（v20.4）。
func ResetPassword(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户ID"})
		return
	}

	adminID, _ := c.Get("userID")
	if adminID.(int64) == id {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能重置自己的密码，请使用个人中心修改"})
		return
	}

	var req struct {
		Password string `json:"password" binding:"required,min=6"` // v21.96：8 → 6
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		bindError(c, err)
		return
	}

	if err := model.AdminResetPassword(id, req.Password); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	strength := passwordStrengthLevel(req.Password) // v21.96：仅提示与审计留痕
	logger.AuditEvent("admin_reset_password", c.GetString("username"), c.ClientIP(), "success", fmt.Sprintf("target id=%d pwd_strength: %s", id, strength))
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "重置密码", strconv.FormatInt(id, 10), "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"message":           "密码重置成功",
		"password_strength": strength,
	})
}

// ToggleUserStatus 管理员切换用户启用/禁用（v20.4）。
func ToggleUserStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户ID"})
		return
	}

	adminID, _ := c.Get("userID")
	if adminID.(int64) == id {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能禁用自己"})
		return
	}

	var req struct {
		Status string `json:"status" binding:"required,oneof=active disabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：status 只能为 active 或 disabled"})
		return
	}

	if err := model.UpdateUserStatus(id, req.Status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	logger.AuditEvent(
		"admin_toggle_status",
		c.GetString("username"),
		c.ClientIP(),
		"success",
		fmt.Sprintf("target id=%d, status=%s", id, req.Status),
	)
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "切换状态", strconv.FormatInt(id, 10), req.Status, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "状态更新成功", "status": req.Status})
}
