package handler

import "github.com/gin-gonic/gin"

// 测试包初始化：gin.SetMode 修改 gin 全局变量，测试用例里的 newNoteContext 等
// helper 每次调用都会 SetMode —— 在 TestRefreshRotation_ConcurrentSameToken
// 等并发场景下触发 DATA RACE（v1.0.134 潜伏，CI Linux race 检测下暴露）。
// 集中在此一次性 SetMode；后续测试文件里的 SetMode 调用全部移除。
func init() {
	gin.SetMode(gin.TestMode)
}
