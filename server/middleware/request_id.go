package middleware

import (
	"regexp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// 合法的 X-Request-ID 白名单（v21.52 P11 审计修复）：
// 仅允许字母数字与 ._- 短横，长度 1-64。防止 CR/LF 注入、超长值与控制字符
// 污染响应头与日志（此前直接透传客户端输入）。
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID 为每个请求注入唯一 trace ID（P3-15）。
// 优先复用上游传入的 X-Request-ID 头（反代/网关场景），
// 没有或非法则生成新的 UUID；同时写入响应头 X-Request-ID 便于客户端定位。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-ID")
		if reqID == "" || !validRequestID.MatchString(reqID) {
			reqID = uuid.New().String()[:8] // 8 位短 ID，日志可读且唯一性够用
		}
		c.Set("requestID", reqID)
		c.Header("X-Request-ID", reqID)
		c.Next()
	}
}
