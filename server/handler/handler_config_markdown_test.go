package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"stickynote/model"
)

// ============================================================================
// config.go / markdown.go handler 单测（v21.77 P1：补 handler 覆盖率）
// 覆盖：配置列表/更新（白名单、非法值）、公开配置、markdown 导入纯函数、导入上限
// ============================================================================

func TestConfigHandlers(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()

	t.Run("ListConfigs 200", func(t *testing.T) {
		c, w := newNoteContext("GET", "/api/configs", "")
		ListConfigs(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "configs") {
			t.Errorf("应含 configs 字段: %s", w.Body.String())
		}
	})
	t.Run("GetPublicConfig 200", func(t *testing.T) {
		model.SetConfig("allow_register", "true")
		c, w := newNoteContext("GET", "/api/public-config", "")
		GetPublicConfig(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "allow_register") {
			t.Errorf("应含 allow_register: %s", w.Body.String())
		}
		// v21.107 P2-8：回收站保留天数对普通用户公开（未配置时默认 "0"=永久保留）
		if !strings.Contains(w.Body.String(), "trash_retention_days") {
			t.Errorf("应含 trash_retention_days: %s", w.Body.String())
		}
	})
	t.Run("UpdateConfig 白名单成功", func(t *testing.T) {
		c, w := newNoteContext("PUT", "/api/config/max_notes_per_user", `{"value":"500"}`)
		c.Params = gin.Params{{Key: "key", Value: "max_notes_per_user"}}
		c.Set("userID", int64(1))
		c.Set("username", "admin")
		UpdateConfig(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("UpdateConfig 非白名单 400", func(t *testing.T) {
		c, w := newNoteContext("PUT", "/api/config/evil_key", `{"value":"x"}`)
		c.Params = gin.Params{{Key: "key", Value: "evil_key"}}
		UpdateConfig(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应 400, got %d", w.Code)
		}
	})
	t.Run("UpdateConfig 非法值 400", func(t *testing.T) {
		c, w := newNoteContext("PUT", "/api/config/max_notes_per_user", `{"value":"-5"}`)
		c.Params = gin.Params{{Key: "key", Value: "max_notes_per_user"}}
		UpdateConfig(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法值应 400, got %d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("UpdateConfig site_title 成功", func(t *testing.T) {
		c, w := newNoteContext("PUT", "/api/config/site_title", `{"value":"我的便签"}`)
		c.Params = gin.Params{{Key: "key", Value: "site_title"}}
		c.Set("userID", int64(1))
		c.Set("username", "admin")
		UpdateConfig(c)
		if w.Code != http.StatusOK {
			t.Fatalf("site_title 应 200, got %d body=%s", w.Code, w.Body.String())
		}
		v, _ := model.GetConfig("site_title")
		if v != "我的便签" {
			t.Errorf("site_title 应已保存: %q", v)
		}
	})
	t.Run("UpdateConfig site_title 空值 400", func(t *testing.T) {
		c, w := newNoteContext("PUT", "/api/config/site_title", `{"value":""}`)
		c.Params = gin.Params{{Key: "key", Value: "site_title"}}
		c.Set("userID", int64(1))
		c.Set("username", "admin")
		UpdateConfig(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("site_title 空值应 400, got %d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("UpdateConfig site_title 超长 400", func(t *testing.T) {
		long := "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十"
		c, w := newNoteContext("PUT", "/api/config/site_title", `{"value":"`+long+`"}`)
		c.Params = gin.Params{{Key: "key", Value: "site_title"}}
		c.Set("userID", int64(1))
		c.Set("username", "admin")
		UpdateConfig(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("site_title 超长应 400, got %d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestIsConfigKeyAllowed(t *testing.T) {
	allowed := []string{"cors_origins", "max_notes_per_user", "max_storage_mb_per_user", "max_note_content_kb", "trash_retention_days", "allow_register", "site_title"}
	blocked := []string{"evil", "register_enabled", "allow_external_images", ""}
	for _, k := range allowed {
		if !isConfigKeyAllowed(k) {
			t.Errorf("%q 应在白名单内", k)
		}
	}
	for _, k := range blocked {
		if isConfigKeyAllowed(k) {
			t.Errorf("%q 不应在白名单内", k)
		}
	}
}

func TestValidateConfigValue(t *testing.T) {
	valid := map[string]string{
		"max_notes_per_user":      "0",
		"max_storage_mb_per_user": "1024",
		"max_note_content_kb":     "100",
		"trash_retention_days":    "30",
		"allow_register":          "true",
		"site_title":              "我的便签",
	}
	invalid := map[string]string{
		"max_notes_per_user":      "-1",
		"max_storage_mb_per_user": "abc",
		"max_note_content_kb":     "2000000",
		"trash_retention_days":    "-3",
		"allow_register":          "yes",
		"site_title":              "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十", // 60 字，超过 50 字上限
	}
	// 空标题单独校验（map 不允许重复键，此处显式断言 site_title 空值/纯空白必须拒绝）
	if err := validateConfigValue("site_title", ""); err == nil {
		t.Error("site_title 空值应被拒绝")
	}
	if err := validateConfigValue("site_title", "   "); err == nil {
		t.Error("site_title 纯空白应被拒绝")
	}
	for k, v := range valid {
		if err := validateConfigValue(k, v); err != nil {
			t.Errorf("%s=%q 应合法: %v", k, v, err)
		}
	}
	for k, v := range invalid {
		if err := validateConfigValue(k, v); err == nil {
			t.Errorf("%s=%q 应非法", k, v)
		}
	}
	// 未列出的 key 直接放行（白名单在 UpdateConfig 层拦）
	if err := validateConfigValue("other", "x"); err != nil {
		t.Errorf("未枚举 key 应放行: %v", err)
	}
}

func TestMarkdownHelpers(t *testing.T) {
	t.Run("extractTitle 取首个 # 标题", func(t *testing.T) {
		got := extractTitle("# 主标题\n正文\n## 副标题", "任意名.md")
		if got != "主标题" {
			t.Errorf("应取主标题, got %q", got)
		}
	})
	t.Run("extractTitle 无标题回退文件名", func(t *testing.T) {
		got := extractTitle("没有标题的正文", "我的笔记.md")
		if got != "我的笔记" {
			t.Errorf("应回退文件名, got %q", got)
		}
	})
	t.Run("txtToHTML 逐行成段（v2.2.1 每行一 p）", func(t *testing.T) {
		got := txtToHTML("第一行\n第二行\n\n新段落 <script>alert(1)</script>")
		if !strings.Contains(got, "<p>第一行</p>") || !strings.Contains(got, "<p>第二行</p>") {
			t.Errorf("应逐行独立成段: %s", got)
		}
		if !strings.Contains(got, "<p></p>") {
			t.Errorf("空行应保留空段落: %s", got)
		}
		if strings.Contains(got, "<script>") {
			t.Error("txt 导入应转义脚本标签")
		}
	})
	t.Run("txtToHTML 空文本", func(t *testing.T) {
		got := txtToHTML("   ")
		if got != "<p></p>" {
			t.Errorf("空文本应 <p></p>, got %q", got)
		}
	})
	t.Run("escapeHTML", func(t *testing.T) {
		got := escapeHTML(`<a href="x">&y</a>`)
		if got != `&lt;a href="x"&gt;&amp;y&lt;/a&gt;` {
			t.Errorf("转义不符: %s", got)
		}
	})
	t.Run("sanitizeHTML 剥离脚本", func(t *testing.T) {
		got := sanitizeHTML(`<p>安全</p><script>alert(1)</script>`)
		if strings.Contains(got, "<script>") {
			t.Error("script 应被剥离")
		}
		if !strings.Contains(got, "安全") {
			t.Error("正常内容应保留")
		}
	})
	t.Run("mdToHTML 标题与粗体", func(t *testing.T) {
		got := mdToHTML("## 二级标题\n\n**加粗**")
		if !strings.Contains(got, "<h2") || !strings.Contains(got, "<strong>加粗</strong>") {
			t.Errorf("markdown 渲染不符: %s", got)
		}
	})
}

func TestImportSizeLimit(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()

	t.Run("默认 100MB", func(t *testing.T) {
		if got := importSizeLimit(); got != 100<<20 {
			t.Errorf("默认应 100MB, got %d", got)
		}
	})
	t.Run("小于下限钳制 16MB", func(t *testing.T) {
		model.SetConfig("max_note_content_kb", "1")
		if got := importSizeLimit(); got != 16<<20 {
			t.Errorf("1KB 应钳制到 16MB, got %d", got)
		}
	})
	t.Run("超过上限钳制 128MB", func(t *testing.T) {
		model.SetConfig("max_note_content_kb", "300000")
		if got := importSizeLimit(); got != 128<<20 {
			t.Errorf("300000KB 应钳制到 128MB, got %d", got)
		}
	})
	t.Run("getMaxImportKB 读配置", func(t *testing.T) {
		model.SetConfig("max_note_content_kb", "42")
		if got := getMaxImportKB(); got != 42 {
			t.Errorf("应读 42, got %d", got)
		}
	})
}
