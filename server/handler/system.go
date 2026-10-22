package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"stickynote/database"
	"stickynote/model"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

var startTime = time.Now()

func GetVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":    Version,
		"build_time": BuildTime,
		"git_commit": GitCommit,
	})
}

func Health(c *gin.Context) {
	// v1.0.168 切片 3：在线全量恢复的安全窗口内，DB 可能被 Close/重开，直接 Ping 有 nil/关闭
	// 句柄风险——显式短路为 503，语义上"正在维护"，也不误导外部监控判死。
	if database.IsRestoring() || database.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "restoring"})
		return
	}
	// 数据库不可达即不健康，供 cmd/main healthcheck 与外部监控判断
	if err := database.DB.Ping(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unhealthy",
			"error":  "database unreachable",
		})
		return
	}
	// v21.90 P2（审计 L-04）：health 只回 status——原响应含 version/uptime，
	// 未认证端点泄露版本与运行时长；版本信息由 /api/version（需登录）提供。
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
	})
}

// AdminUploadsHashStatus 附件 sha256 回填进度（P1 批次3，管理端）。
func AdminUploadsHashStatus(c *gin.Context) {
	pending, err := model.CountUploadsMissingHash()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取回填进度失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"pending": pending})
}
