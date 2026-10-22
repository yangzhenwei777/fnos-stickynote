package middleware

import (
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimit 全局 API 限流中间件（B4，外网暴露面加固）：
// 每 IP 令牌桶——rate 张/秒匀速补充，桶容量 burst，耗尽返回 429。
// 正常用户单页操作峰值 <5 req/s，参数富余 4 倍以上；
// 恶意脚本被压制在 rate 次/秒，NAS 可轻松承受。
//
// 环境变量 NOTEPAD_DISABLE_RATE_LIMIT=1 可整体关闭（测试/CI 用）：
// 冒烟脚本高密度连续请求会打满单 IP 配额造成 429 级联，测试实例关闭后
// 仅保留 loginGuard 等特性级限流的专项测试（v21.19）。
//
// 与 loginGuard 关系：本中间件是粗粒度第一道闸门（所有 /api），
// loginGuard 是细粒度第二道闸门（登录类 5 次/5 分钟），互不影响。
//
// 内存安全：后台 goroutine 每 3 分钟回收不活跃 IP 条目，
// 正常使用下内存占用恒定 <1MB；重启清零可接受
// （loginGuard 的防爆破持久化在独立表 login_fails，不受影响）。
func RateLimit(rate float64, burst int) gin.HandlerFunc {
	if os.Getenv("NOTEPAD_DISABLE_RATE_LIMIT") == "1" {
		// v21.20（审计复核）：显式告警防生产误设——该开关仅供 CI/冒烟高频测试，
		// 若在生产环境被误设将关闭全局限流第一道防线（loginGuard 不受影响仍生效）。
		log.Println("[WARN] NOTEPAD_DISABLE_RATE_LIMIT=1: 全局限流已禁用，仅限 CI/冒烟环境使用！生产环境请勿设置！")
		return func(c *gin.Context) { c.Next() }
	}
	l := newLimiter(rate, burst)
	go l.cleanupLoop(3 * time.Minute)
	return func(c *gin.Context) {
		// 健康检查与版本查询豁免：监控系统需要随时可达
		if c.Request.URL.Path == "/health" || c.Request.URL.Path == "/api/version" {
			c.Next()
			return
		}
		if !l.allow(c.ClientIP()) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests,
				gin.H{"error": "请求过于频繁，请稍后再试"})
			return
		}
		c.Next()
	}
}

type visitor struct {
	tokens   float64
	lastSeen time.Time
}

type limiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     float64 // 每秒补充令牌数
	burst    float64 // 桶容量
	now      func() time.Time
}

func newLimiter(rate float64, burst int) *limiter {
	return &limiter{
		visitors: make(map[string]*visitor),
		rate:     rate,
		burst:    float64(burst),
		now:      time.Now,
	}
}

// allow 取走一张通行证：桶里有则放行并扣减，没有则拒绝。
func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	v, ok := l.visitors[ip]
	if !ok {
		v = &visitor{tokens: l.burst, lastSeen: l.now()}
		l.visitors[ip] = v
	}

	// 按时间差匀速补充令牌，封顶桶容量
	elapsed := l.now().Sub(v.lastSeen).Seconds()
	v.tokens += elapsed * l.rate
	if v.tokens > l.burst {
		v.tokens = l.burst
	}
	v.lastSeen = l.now()

	if v.tokens < 1 {
		return false
	}
	v.tokens--
	return true
}

// cleanupLoop 定期回收 idle 超时未访问的 IP 条目，防止内存膨胀。
func (l *limiter) cleanupLoop(idle time.Duration) {
	ticker := time.NewTicker(idle)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		cutoff := time.Now().Add(-idle)
		for ip, v := range l.visitors {
			if v.lastSeen.Before(cutoff) {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}
