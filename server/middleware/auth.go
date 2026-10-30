package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"stickynote/auth"
	"stickynote/logger"
	"stickynote/model"
)

func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString := extractToken(c)
		if tokenString == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			c.Abort()
			return
		}

		claims, err := auth.ParseToken(tokenString)
		if err != nil || claims.Type != "access" {
			// refresh token 不能用于业务 API（Type 校验），过期/伪造一并拒绝
			logger.Info("Authentication failed from %s", c.ClientIP())
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期"})
			c.Abort()
			return
		}

		// token_version 比对（v18.6）：登出/改密/重置密码会 BumpTokenVersion——
		// 此前 access token 在过期前（最长 2h）仍可用，v18.2 的登出双保险在
		// access 层留了豁口。每次请求一次单行查询（NAS 规模零压力）；
		// 不一致即 401，话术与过期一致不泄露细节。
		// 附带收益：role 改取库内实时值，角色变更即刻生效（此前要等 token 过期）。
		user, err := model.GetUserByID(claims.UserID)
		if err != nil || user.TokenVersion != claims.TokenVersion {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期"})
			c.Abort()
			return
		}
		if user.Status == "disabled" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期"})
			c.Abort()
			return
		}

		c.Set("userID", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", user.Role)
		c.Set("tokenVersion", claims.TokenVersion)
		c.Next()
	}
}

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			logger.Error("Access denied: non-admin user %s attempted admin action", c.GetString("username"))
			c.JSON(http.StatusForbidden, gin.H{"error": "需要管理员权限"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func extractToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	if cookie, err := c.Cookie("access_token"); err == nil {
		return cookie
	}

	return ""
}
