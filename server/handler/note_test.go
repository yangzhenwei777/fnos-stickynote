package handler

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"stickynote/database"
	"stickynote/model"
	"stickynote/notetext"
)

// tempInstanceDir 测试专用临时实例目录：替代 t.TempDir() 的"一次性强删"。
//
// 背景（v1.0.175 R0 CI 观察，TestImportSizeLimit）：Ubuntu runner 上 t.TempDir 自动清理
// 偶发 `RemoveAll ...: directory not empty`——modernc sqlite 连接池的最后一名清理者
// （GC finalizer 路径）释放句柄与 WAL 附属文件回收同 RemoveAll 遍历存在毫秒级竞态。
// 处置与 restore 包 renameWithRetry 同款：有界重试（≤3s）吸收瞬态 ENOTEMPTY；
// 重试耗尽仍残留则 t.Errorf——不掩盖真正的句柄泄漏。
// 注册顺序：本函数须在"关库 cleanup"之前调用（LIFO 下 remove 最后执行）。
func tempInstanceDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sntest-"+strings.ReplaceAll(t.Name(), "/", "_")+"-")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() {
		var last error
		for i := 0; i < 60; i++ {
			if last = os.RemoveAll(dir); last == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("temp dir cleanup failed after 3s (lingering handle?): %v", last)
	})
	return dir
}

func setupNoteDB(t *testing.T) func() {
	t.Helper()
	if err := database.Init(filepath.Join(tempInstanceDir(t), "test.db"), "1.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	return func() {
		database.DB.Close()
		database.DB = nil
	}
}

// ============================================================================
// 纯函数单测 — 正常/边界/异常三态
// ============================================================================

func TestBuildNoteMD(t *testing.T) {
	t.Run("完整笔记", func(t *testing.T) {
		n := &model.Note{
			Title:   "测试标题",
			Content: "<p>正文内容</p><ul><li>项目1</li></ul>",
			Tags:    "标签A, 标签B, 标签C",
		}
		got := buildNoteMD(n, nil)
		if !strings.HasPrefix(got, "# ") {
			t.Errorf("应 '# ' 开头: %q", got[:60])
		}
		if !strings.Contains(got, "**标签：** 标签A, 标签B, 标签C") {
			t.Errorf("应含标签行: %s", got)
		}
	})

	t.Run("空笔记", func(t *testing.T) {
		n := &model.Note{Title: "", Content: "", Tags: ""}
		got := buildNoteMD(n, nil)
		if got == "" {
			t.Log("空笔记 MD 为空（预期内）")
		}
	})

	t.Run("无标签", func(t *testing.T) {
		n := &model.Note{Title: "无标签笔记", Content: "<p>test</p>", Tags: ""}
		got := buildNoteMD(n, nil)
		if !strings.Contains(got, "# 无标签笔记") {
			t.Errorf("应含标题: %s", got)
		}
	})
}

func TestSanitizeExportFilename(t *testing.T) {
	t.Run("正常中文", func(t *testing.T) {
		got := notetext.SanitizeExportFilename("我的笔记")
		if got != "我的笔记" {
			t.Errorf("正常中文应不变: %q", got)
		}
	})

	t.Run("特殊字符清理", func(t *testing.T) {
		got := notetext.SanitizeExportFilename("笔/记:斜*杠?问号")
		for _, c := range []string{"/", ":", "*", "?"} {
			if strings.Contains(got, c) {
				t.Errorf("应清理 %q, got %q", c, got)
			}
		}
	})

	t.Run("超长截断", func(t *testing.T) {
		long := strings.Repeat("A", 300) + ".md"
		got := notetext.SanitizeExportFilename(long)
		if len(got) > 201 { // 200 + ".md" > 201 means exceeded
			t.Errorf("超长应截断: %d chars", len(got))
		}
	})
}

func TestCheckContentLimit(t *testing.T) {
	cleanup := setupNoteDB(t)
	t.Cleanup(cleanup)

	// insert config values for the test
	_, _ = database.DB.Exec(`INSERT OR REPLACE INTO configs (key, value) VALUES ('max_note_content_kb', '0')`)
	_, _ = database.DB.Exec(`INSERT OR REPLACE INTO configs (key, value) VALUES ('max_notes_per_user', '1000')`)

	t.Run("不限=0放行", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/api/notes", nil)
		c.Set("contentLimitKB", int64(0))
		big := "<p>" + strings.Repeat("x", 100000) + "</p>"
		if !checkContentLimit(c, big) {
			t.Error("0 应不限内容")
		}
	})

	t.Run("小内容放行", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/api/notes", nil)
		c.Set("contentLimitKB", int64(10))
		if !checkContentLimit(c, "<p>hello world</p>") {
			t.Error("10KB 上限下小内容应放行")
		}
	})

	t.Run("超上限拒绝", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/api/notes", nil)
		// Set max_note_content_kb = 1 in DB
		database.DB.Exec(`INSERT OR REPLACE INTO configs (key, value) VALUES ('max_note_content_kb', '1')`)
		big := "<p>" + strings.Repeat("x", 2000) + "</p>"
		if checkContentLimit(c, big) {
			t.Error("超 1KB 应拒绝")
		}
		// Reset
		database.DB.Exec(`INSERT OR REPLACE INTO configs (key, value) VALUES ('max_note_content_kb', '0')`)
	})
}

func TestDedupeExportName(t *testing.T) {
	used := make(map[string]bool)

	t.Run("无冲突", func(t *testing.T) {
		got := notetext.DedupeExportName(used, "量子计算机原理笔记")
		if got != "量子计算机原理笔记" {
			t.Errorf("无冲突应不变: %q", got)
		}
	})

	t.Run("一次冲突", func(t *testing.T) {
		got := notetext.DedupeExportName(used, "量子计算机原理笔记")
		if got != "量子计算机原理笔记(2)" {
			t.Errorf("首次冲突 i=2: %q", got)
		}
	})

	t.Run("二次冲突", func(t *testing.T) {
		got := notetext.DedupeExportName(used, "量子计算机原理笔记")
		if got != "量子计算机原理笔记(3)" {
			t.Errorf("二次冲突 i=3: %q", got)
		}
	})
}
