package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"stickynote/logger"
	"stickynote/model"
)

type updateConfigRequest struct {
	Value string `json:"value" binding:"required"`
}

func ListConfigs(c *gin.Context) {
	configs, err := model.GetAllConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取配置列表失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"configs": configs})
}

func UpdateConfig(c *gin.Context) {
	key := c.Param("key")
	if !isConfigKeyAllowed(key) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不允许修改该配置项"})
		return
	}
	var req updateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// v21.68 P2-06：保存前校验配置值范围，避免管理后台写入无效状态
	if err := validateConfigValue(key, req.Value); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := model.SetConfig(key, req.Value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新配置失败"})
		return
	}

	logger.AuditEvent("config_update", c.GetString("username"), c.ClientIP(), "success", key+" = "+req.Value)
	model.LogAdminAction(c.GetInt64("userID"), c.GetString("username"), "修改配置", key, "", c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "配置更新成功"})
}

func GetPublicConfig(c *gin.Context) {
	configs, err := model.GetPublicConfigs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取配置失败"})
		return
	}
	// v21.107 P2-8：configs 表未落该键时补默认 0（永久保留），前端无需再猜
	if _, ok := configs["trash_retention_days"]; !ok {
		configs["trash_retention_days"] = "0"
	}
	c.JSON(http.StatusOK, configs)
}

// isConfigKeyAllowed 检查配置 key 是否在白名单内（v20.7）。
// v21.68 P2-05：移除 allow_external_images（外链图片功能已废弃，产品方向为永久关闭）。
func isConfigKeyAllowed(key string) bool {
	allowed := map[string]bool{
		"cors_origins":            true,
		"max_notes_per_user":      true,
		"max_storage_mb_per_user": true,
		"max_note_content_kb":     true,
		"trash_retention_days":    true,
		"allow_register":          true, // 注册开关（v20.8 曾误写 register_enabled 幽灵键导致开关失效）
		"site_title":              true, // F-6（v1.0.134 审计）：应用标题；本就是 /public-config 下发的展示型配置，无敏感面
	}
	return allowed[key]
}

// validateConfigValue 校验可编辑配置的值范围（v21.68 P2-06）。
func validateConfigValue(key, value string) error {
	v := strings.TrimSpace(value)
	switch key {
	case "max_notes_per_user":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1000000 {
			return fmt.Errorf("max_notes_per_user 必须是 0~1000000 的整数（0=不限）")
		}
	case "max_storage_mb_per_user":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1048576 {
			return fmt.Errorf("max_storage_mb_per_user 必须是 0~1048576 的整数（0=不限）")
		}
	case "max_note_content_kb":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 102400 {
			return fmt.Errorf("max_note_content_kb 必须是 0~102400 的整数（0=不限）")
		}
	case "trash_retention_days":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 36500 {
			return fmt.Errorf("trash_retention_days 必须是 0~36500 的整数（0=永久保留）")
		}
	case "allow_register":
		if v != "true" && v != "false" {
			return fmt.Errorf("allow_register 必须是 true 或 false")
		}
	case "site_title":
		// F-6：应用标题校验。空标题会让页头/登录页标题塌陷；超长破坏布局；
		// 控制字符可能造成 DOM 注入或显示异常——均 fail-closed 拒绝。
		if n := len([]rune(v)); n == 0 || n > 50 {
			return fmt.Errorf("应用标题长度必须为 1~50 个字符")
		}
		for _, r := range v {
			if unicode.IsControl(r) {
				return fmt.Errorf("应用标题不能包含控制字符")
			}
		}
	}
	return nil
}
