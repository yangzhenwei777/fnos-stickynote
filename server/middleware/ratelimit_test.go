package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func setupRLTest() (*gin.Engine, func()) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	l := RateLimit(20, 40)
	r.Use(l)
	r.GET("/api/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	r.GET("/health", func(c *gin.Context) {
		c.String(http.StatusOK, "healthy")
	})
	r.GET("/api/version", func(c *gin.Context) {
		c.String(http.StatusOK, "1.0.0")
	})
	return r, func() {}
}

func doRLReq(r *gin.Engine, path string, ip string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = ip + ":12345"
	r.ServeHTTP(w, req)
	return w.Code
}

// TestRateLimitNormal 正常速率不受限
func TestRateLimitNormal(t *testing.T) {
	r, _ := setupRLTest()
	for i := 0; i < 10; i++ {
		code := doRLReq(r, "/api/test", "192.0.2.32")
		if code != http.StatusOK {
			t.Errorf("正常请求 #%d 应 200, got %d", i, code)
		}
	}
}

// TestRateLimitBurst 桶容量范围内全部放行
func TestRateLimitBurst(t *testing.T) {
	r, _ := setupRLTest()
	failures := 0
	for i := 0; i < 40; i++ {
		if doRLReq(r, "/api/test", "192.0.2.30") != http.StatusOK {
			failures++
		}
	}
	if failures > 0 {
		t.Errorf("burst=40 内应有 0 失败, got %d", failures)
	}
}

// TestRateLimitExceed 超限返回 429
func TestRateLimitExceed(t *testing.T) {
	r, _ := setupRLTest()
	for i := 0; i < 41; i++ {
		_ = doRLReq(r, "/api/test", "192.0.2.33")
	}
	// 第 41 个请求桶已空，应 429
	code := doRLReq(r, "/api/test", "192.0.2.33")
	if code != http.StatusTooManyRequests {
		t.Errorf("超限应 429, got %d", code)
	}
}

// TestRateLimitIndependentIPs 不同 IP 各自独立计数
func TestRateLimitIndependentIPs(t *testing.T) {
	r, _ := setupRLTest()
	// IP-A 耗尽
	for i := 0; i < 41; i++ {
		doRLReq(r, "/api/test", "192.0.2.37")
	}
	// IP-B 应不受影响
	code := doRLReq(r, "/api/test", "192.0.2.31")
	if code != http.StatusOK {
		t.Errorf("不同 IP 应独立, got %d", code)
	}
}

// TestRateLimitHealthExempt /health 豁免
func TestRateLimitHealthExempt(t *testing.T) {
	r, _ := setupRLTest()
	for i := 0; i < 100; i++ {
		code := doRLReq(r, "/health", "192.0.2.34")
		if code != http.StatusOK {
			t.Errorf("/health 应豁免限流, request#%d got %d", i, code)
		}
	}
}

// TestRateLimitVersionExempt /api/version 豁免
func TestRateLimitVersionExempt(t *testing.T) {
	r, _ := setupRLTest()
	for i := 0; i < 100; i++ {
		code := doRLReq(r, "/api/version", "192.0.2.35")
		if code != http.StatusOK {
			t.Errorf("/api/version 应豁免限流, request#%d got %d", i, code)
		}
	}
}

// TestRateLimitCleanup 清理回收不活跃 IP（测试低层 limiter，避免 goroutine 泄漏）
func TestRateLimitCleanup(t *testing.T) {
	now := time.Now()
	l := newLimiter(20, 40)
	l.now = func() time.Time { return now }

	// 建立 IP 并耗尽
	for i := 0; i < 41; i++ {
		l.allow("192.0.2.36")
	}
	if l.allow("192.0.2.36") {
		t.Error("桶耗尽应 false")
	}

	// 推进时间超过清理 idle（模拟 3 分钟后，桶已重新补满）
	now = now.Add(4 * time.Minute)
	// 清理
	l.mu.Lock()
	for ip, v := range l.visitors {
		if v.lastSeen.Before(now.Add(-3 * time.Minute)) {
			delete(l.visitors, ip)
		}
	}
	l.mu.Unlock()

	// IP 已被清理，下次访问重新以满桶开始
	if len(l.visitors) != 0 {
		t.Errorf("清理后 visitors 应为 0, got %d", len(l.visitors))
	}
}
