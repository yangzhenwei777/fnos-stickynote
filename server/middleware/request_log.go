package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"stickynote/logger"
)

// RequestLog 请求日志中间件（v19.3 优-12）：记录 method/path/status/duration/client_ip。
// 不记录请求体/响应体（用户确认：笔记内容为隐私数据）。
func RequestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		dur := time.Since(start)
		logger.InfoS("request completed",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", dur.Milliseconds(),
			"ip", c.ClientIP(),
			"req_id", c.GetString("requestID"),
		)
	}
}
