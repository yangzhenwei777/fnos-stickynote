package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"stickynote/database"
)

// RestorationGate 在线全量恢复的安全窗口门禁（v1.0.168 切片 3）。
//
// 规则：非恢复期——登记在途请求（供恢复前"排空"），照常放行；
// 恢复期——除 /health（由 Health 自己回 503 restoring，前端据此轮询等待）外，
// 一律 503 快速失败，杜绝任何请求在 Close~Reopen 窗口内触碰已被换掉的 DB。
//
// 注意：白名单只放 /health，且它不计数（不查库）。恢复进度本身走内存态，前端以 /health
// 为准判断"何时恢复完成"，避免把带鉴权（需查库）的接口塞进白名单。
func RestorationGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if database.IsRestoring() {
			if c.Request.URL.Path == "/health" {
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":  "系统正在恢复数据，请稍后重试",
				"status": "restoring",
			})
			return
		}
		database.EnterRequest()
		defer database.ExitRequest()
		c.Next()
	}
}
