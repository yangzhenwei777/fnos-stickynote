package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// CacheControl 按路径为静态资源下发缓存策略：
//   - /assets/*  vite 构建产物（文件名带内容 hash，内容永不变化）→ 一年强缓存
//   - / 与 /index.html → no-cache（每次重验证，升级后立即拿到新资源引用，
//     根治"升级后旧缓存引用已删 hash 资源导致白屏"）
//   - /uploads/* 用户上传图片（uuid 文件名，内容不变）→ 30 天缓存
//
// /api 下的接口不下发缓存头，保持既有行为不变。
func CacheControl() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		switch {
		case strings.HasPrefix(p, "/assets/"):
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		case p == "/" || p == "/index.html":
			c.Header("Cache-Control", "no-cache")
		case strings.HasPrefix(p, "/uploads/"):
			c.Header("Cache-Control", "public, max-age=2592000")
		}
		c.Next()
	}
}
