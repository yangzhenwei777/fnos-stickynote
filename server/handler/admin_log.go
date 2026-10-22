package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"stickynote/model"
)

// GetAdminLogs 返回管理员操作日志（分页 + 可选筛选）。
func GetAdminLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	action := c.Query("action")
	username := c.Query("username")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	logs, total, err := model.ListAdminLogs(page, pageSize, action, username, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询日志失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":      logs,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
