package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// bindError 处理请求体绑定/校验错误（v21.25）：
// - 密码字段长度不足（min tag）→ 友好提示"密码至少 N 位"（N 取自 tag 实际值）
// - 其他参数错误 → 统一"参数错误"，不透传 validator 原始英文（避免泄露字段细节）
func bindError(c *gin.Context, err error) {
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		for _, fe := range verrs {
			if fe.Tag() == "min" && (fe.Field() == "Password" || fe.Field() == "NewPassword") {
				// v21.96 修复：此前文案硬编码为「密码至少 12 位」，与实际 tag 脱钩——
				// 管理员创建/重置用户路径历史 tag 为 min=8，用户输 9 位时后端按 8 位规则
				// 拒绝、却提示「至少 12 位」，规则与提示不一致（用户按提示改到 12 位才通过，
				// 而实际上 8 位就够）。改为读取 tag 参数，使规则与提示永远同源。
				c.JSON(http.StatusBadRequest, gin.H{"error": "密码至少 " + fe.Param() + " 位"})
				return
			}
		}
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
}
