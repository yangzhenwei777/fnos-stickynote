package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// v2.1.2：启动兜底内联脚本的 CSP hash 必须与 web/index.html 的真实内容一致。
//
// 为什么必须有这条：hash 是"内容指纹"，脚本一改而常量没跟着改，浏览器会
// **静默拒绝**执行内联兜底——正是本次事故里"没有任何提示"的同型故障。
// 用测试把它钉死，让漂移在门禁处就红。
func TestBootFallbackHashMatchesInlineScript(t *testing.T) {
	const rel = "../../web/index.html"
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Skipf("跳过：未找到 %s（仅在完整仓库/CI 全仓检出时校验）: %v", rel, err)
	}
	src := string(raw)

	re := regexp.MustCompile(`(?s)<script id="sn-boot-inline">(.*?)</script>`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("web/index.html 中找不到 #sn-boot-inline 内联脚本——基址自举/启动兜底被删了？")
	}
	sum := sha256.Sum256([]byte(m[1]))
	want := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	if want != BootFallbackHash {
		t.Fatalf("CSP hash 漂移：\n  index.html 实际 = %s\n  secure.go 常量 = %s\n"+
			"改过内联脚本就必须同步 BootFallbackHash 与 index.html 的 meta，否则浏览器会静默拒绝执行兜底",
			want, BootFallbackHash)
	}

	// meta 里也要有同一个 hash，否则开发态/无 header 场景下兜底同样不执行
	if !strings.Contains(src, "'"+BootFallbackHash+"'") {
		t.Fatalf("web/index.html 的 CSP meta 未包含 %s —— meta 与 header 口径必须一致", BootFallbackHash)
	}
}

// v2.1.2：frame-ancestors 不得再出现在响应头里。
//
// 依据：web/index.html 明确写着"刻意不包含 frame-ancestors，设置后 fnOS 桌面图标
// 将无法打开"；而 'self' 在跨端口（5666 桌面 / 13389 应用）时不同源 ⇒ 会被拦截。
// 这条断言是防止未来审计又单方面加回来的反向护栏。
func TestSecureHeadersNoFrameAncestors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecureHeaders())
	r.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	r.ServeHTTP(w, req)

	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("缺少 Content-Security-Policy 响应头")
	}
	if strings.Contains(csp, "frame-ancestors") {
		t.Fatalf("frame-ancestors 又回来了（会让 fnOS iframe 包装的客户端整页被拦）：%s", csp)
	}
	if !strings.Contains(csp, BootFallbackHash) {
		t.Fatalf("CSP 响应头缺少启动兜底 hash %s：%s", BootFallbackHash, csp)
	}
	// 基线防护仍在
	for _, want := range []string{"object-src 'none'", "base-uri 'self'", "default-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP 丢失基线指令 %q：%s", want, csp)
		}
	}

	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
}
