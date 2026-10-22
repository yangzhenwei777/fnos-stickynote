package middleware

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"stickynote/model"
)

// UploadBodyLimit /upload 接口专用 body 上限（v21.107 P1-13）：
// 历史上 /upload 挂在 16MB 小接口组，而业务上限 UPLOAD_MAX_MB 默认 30MB——
// 16~30MB 的合法图片在传输层被 MaxBytesReader 直接掐断（v1.0.144 操作逻辑审查实测），
// 「文件超过大小限制（30MB）」的业务提示永不可达。网关限额取 UPLOAD_MAX_MB + 4MB
// multipart 编码余量；下限保持 16MB（不低于小接口档位，行为只放宽不收紧）。
func UploadBodyLimit() int64 {
	mb := 30
	if v := strings.TrimSpace(os.Getenv("UPLOAD_MAX_MB")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			mb = n
		}
	}
	limit := int64(mb+4) << 20
	if min := int64(16 << 20); limit < min {
		limit = min
	}
	return limit
}

func UploadBodySize() gin.HandlerFunc {
	return MaxBodySize(UploadBodyLimit())
}

// MaxBodySize 限制请求体大小（v21.52 P3 审计修复）。
// 原实现：CreateNote 等接口先 ShouldBindJSON 完整解析再检查内容大小，
// 导入接口 io.ReadAll 无上限——攻击者可发送超大 body 造成内存/CPU/磁盘耗尽。
// 本中间件在 gin 解析前用 http.MaxBytesReader 硬性限制请求体，超限返回 413。
func MaxBodySize(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

// ---- 动态请求体上限（v21.55 P2：跟随前台【单篇笔记上限】配置）----
// 需求：管理员在管理后台改 max_note_content_kb 后，技术层上限同步变化，无需改代码。
// 设计：
//   - 读取 configs.max_note_content_kb（KB），X=0（不限）→ 默认 100MB 兜底（与业务硬上限一致）
//   - 有效 X → 上限 = max(X×1.2, X+1MB)（JSON 转义余量，防存满内容被误拦）
//   - 下限 16MB（不低于小接口档位）、上限 128MB（v21.90 N-04：由 256MB 收敛，
//     1 核/低内存 NAS 上多并发 100MB+ body 会放大 DoS 面；业务默认上限仍为 100MB）
//   - 5 秒缓存（与 cors.go 一致），前台改配置 ≤5s 生效
const (
	dynamicBodyMin      = 16 << 20        // 16MB 下限
	dynamicBodyMax      = 128 << 20       // 128MB 封顶（v21.90 收敛）
	dynamicBodyDefault  = 100 << 20       // X=0 时兜底 100MB（对齐业务硬上限）
	dynamicConfigTTL    = 5 * time.Second // 配置缓存 TTL
	dynamicJSONOverhead = 1 << 20         // JSON 转义余量 1MB
)

var dynamicBodyCache struct {
	mu      sync.RWMutex
	expires time.Time
	limit   int64
}

// DynamicMaxBodyLimit 计算当前动态请求体上限（跟随 max_note_content_kb 配置）。
func DynamicMaxBodyLimit() int64 {
	dynamicBodyCache.mu.RLock()
	if time.Now().Before(dynamicBodyCache.expires) {
		l := dynamicBodyCache.limit
		dynamicBodyCache.mu.RUnlock()
		return l
	}
	dynamicBodyCache.mu.RUnlock()

	dynamicBodyCache.mu.Lock()
	defer dynamicBodyCache.mu.Unlock()
	if time.Now().Before(dynamicBodyCache.expires) { // double-check
		return dynamicBodyCache.limit
	}

	limit := int64(dynamicBodyDefault)
	if v, err := model.GetConfig("max_note_content_kb"); err == nil {
		if n, perr := strconv.Atoi(strings.TrimSpace(v)); perr == nil && n > 0 {
			x := int64(n) * 1024 // KB → bytes
			// 余量：max(1.2x, x+1MB)，覆盖 JSON 转义与字段开销
			r := x + x/5
			if x+dynamicJSONOverhead > r {
				r = x + dynamicJSONOverhead
			}
			limit = r
		}
	}
	// clamp 到 [16MB, 128MB]
	if limit < dynamicBodyMin {
		limit = dynamicBodyMin
	}
	if limit > dynamicBodyMax {
		limit = dynamicBodyMax
	}
	dynamicBodyCache.limit = limit
	dynamicBodyCache.expires = time.Now().Add(dynamicConfigTTL)
	return limit
}

// DynamicMaxBodySize 动态请求体上限中间件：每次请求计算当前配置对应上限。
// 用于大 body 路由（笔记保存/导入），小接口仍用固定 MaxBodySize(16MB)。
func DynamicMaxBodySize() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := DynamicMaxBodyLimit()
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
