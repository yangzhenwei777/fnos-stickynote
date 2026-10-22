// payload_endpoint.go —— GET /api/sync/payload/:sha256（§34/§35，P1-b 2026-10-15）。
//
// §34：blob 禁止静态 HTTP 暴露，读取只经本端点（JWT 由 auth 路由组保证）+ 流式回传；
// §35：授权 = 该 sha256 仍被当前 user 的有效 sync_changes 引用，否则 404——
// 不能只因为知道 hash 就读取（hash 保密性不是授权依据）。
package sync

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"stickynote/changelog"
)

func PayloadBlob(c *gin.Context) {
	userID, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}
	uid := userID.(int64)
	ref := c.Param("sha256")
	// §34 形状校验：64 位小写 hex，天然拒绝路径穿越与非 sha256 形状。
	// 校验失败与"无引用"同响应（404），不向探测者泄露区分度。
	if !changelog.ValidPayloadRef(ref) {
		c.JSON(http.StatusNotFound, gin.H{"error": "payload not found"})
		return
	}
	// §35 授权：非本用户有效引用 → 404。
	ok2, err := changelog.PayloadReferenced(db(), uid, ref)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "payload auth check failed"})
		return
	}
	if !ok2 {
		c.JSON(http.StatusNotFound, gin.H{"error": "payload not found"})
		return
	}
	f, size, err := changelog.OpenPayload(ref)
	if err != nil {
		// 引用在库、文件缺失（GC 窗口/人为删件）——客户端按失败重试/RESYNC 兜底。
		c.JSON(http.StatusNotFound, gin.H{"error": "payload not found"})
		return
	}
	defer f.Close()
	c.Header("Content-Type", "application/octet-stream")
	c.Header("X-Payload-Sha256", ref)
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Status(http.StatusOK)
	// 写一半失败只能断连（响应头已发出）；客户端 sha256 校验会拦住截断件
	// （对齐附件通道"下载校验自愈"语义）。
	_, _ = io.Copy(c.Writer, f)
}
