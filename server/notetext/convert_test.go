// notetext 包单测：锁定「文件 → (标题, 消毒 HTML)」这一条**双宿主共用**管线的对外契约。
//
// 为什么必须单独测：本包是服务端在线导入（handler.ImportFile）与桌面离线导入
// （desktop.LocalService.ImportNote）的**唯一**转换实现。包内函数只有薄封装被
// handler 既有测试覆盖，而 Convert / SizeLimitForConfig 是本轮新增的对外契约，
// 没有测试就会成为"改了没人拦"的盲区。
package notetext

import (
	"errors"
	"strings"
	"testing"
)

func TestAllowedExtAndExtOf(t *testing.T) {
	for _, c := range []struct {
		file string
		want bool
	}{
		{"a.md", true}, {"A.MD", true}, {"a.txt", true},
		{"a.html", true}, {"a.htm", true}, {"a.HTML", true},
		{"a.pdf", false}, {"a.md.bak", false}, {"noext", false}, {"", false},
	} {
		if got := AllowedExt(ExtOf(c.file)); got != c.want {
			t.Errorf("AllowedExt(ExtOf(%q)) = %v, want %v", c.file, got, c.want)
		}
	}
}

func TestConvertUnsupportedExt(t *testing.T) {
	if _, _, err := Convert("x.pdf", []byte("hi")); !errors.Is(err, ErrUnsupportedExt) {
		t.Fatalf("err = %v, want ErrUnsupportedExt", err)
	}
}

func TestConvertMarkdownTitleAndSanitize(t *testing.T) {
	title, content, err := Convert("我的笔记.md", []byte("# 主标题\n\n正文 **粗体**\n"))
	if err != nil {
		t.Fatalf("Convert err = %v", err)
	}
	if title != "主标题" {
		t.Errorf("title = %q, want 主标题", title)
	}
	if !strings.Contains(content, "<strong>粗体</strong>") {
		t.Errorf("content 缺粗体渲染: %q", content)
	}
}

func TestConvertTitleFallbackToFilename(t *testing.T) {
	title, _, err := Convert("无题.md", []byte("没有一级标题的正文"))
	if err != nil {
		t.Fatalf("Convert err = %v", err)
	}
	if title != "无题" {
		t.Errorf("title = %q, want 无题", title)
	}
}

func TestConvertTitleControlCharsStrippedAndTruncated(t *testing.T) {
	// 标题含 NUL 与换行：NUL 剥除、换行折叠为空格（v21.105 口径）
	title, _, err := Convert("t.md", []byte("# A\x00B\nC\n"))
	if err != nil {
		t.Fatalf("Convert err = %v", err)
	}
	if strings.ContainsRune(title, 0) {
		t.Errorf("title 仍含 NUL: %q", title)
	}

	// 超 200 字符截断（MaxTitleRunes）
	long := "# " + strings.Repeat("字", 300) + "\n"
	title, _, err = Convert("t.md", []byte(long))
	if err != nil {
		t.Fatalf("Convert err = %v", err)
	}
	if n := len([]rune(title)); n != MaxTitleRunes {
		t.Errorf("title runes = %d, want %d", n, MaxTitleRunes)
	}
}

func TestConvertEmptyTitleFallsBack(t *testing.T) {
	// 全空白内容 + 无扩展名的隐形标题 → 兜底标题
	title, _, err := Convert("  .md", []byte("   "))
	if err != nil {
		t.Fatalf("Convert err = %v", err)
	}
	if strings.TrimSpace(title) == "" {
		t.Errorf("title 不应为空，got %q", title)
	}
}

func TestConvertHTMLSanitized(t *testing.T) {
	_, content, err := Convert("x.html", []byte(`<p>安全</p><script>alert(1)</script>`))
	if err != nil {
		t.Fatalf("Convert err = %v", err)
	}
	if strings.Contains(content, "<script") || strings.Contains(content, "alert(1)") {
		t.Errorf("消毒未生效: %q", content)
	}
	if !strings.Contains(content, "安全") {
		t.Errorf("正文丢失: %q", content)
	}
}

func TestConvertTxtEscapesTags(t *testing.T) {
	_, content, err := Convert("x.txt", []byte("第一行\n第二行 <script>alert(1)</script>"))
	if err != nil {
		t.Fatalf("Convert err = %v", err)
	}
	if strings.Contains(content, "<script") {
		t.Errorf("txt 分支未转义: %q", content)
	}
	if !strings.Contains(content, "<br>") {
		t.Errorf("txt 分支应换行分段: %q", content)
	}
}

func TestConvertMarkdownTaskListLoop(t *testing.T) {
	// 导出↔导入任务清单回环：v3 序列化形态（ul[data-type=taskList] + li[data-type=taskItem]）
	_, content, err := Convert("t.md", []byte("- [x] 已完成\n- [ ] 未完成\n"))
	if err != nil {
		t.Fatalf("Convert err = %v", err)
	}
	if !strings.Contains(content, `data-type="taskList"`) {
		t.Errorf("缺 taskList 标记: %q", content)
	}
	if !strings.Contains(content, `data-type="taskItem"`) {
		t.Errorf("缺 taskItem 标记: %q", content)
	}
	if !strings.Contains(content, `data-checked="true"`) {
		t.Errorf("缺勾选态: %q", content)
	}
}

func TestSizeLimitForConfig(t *testing.T) {
	cases := []struct {
		kb   int
		want int64
	}{
		{0, 100 << 20},           // 0=不限 → 100MB 兜底
		{-1, 100 << 20},          // 负数按不限处理
		{1024, 16 << 20},         // 1MB 低于下限 → 抬到 16MB
		{2048, 16 << 20},         // 2MB 同样抬到下限
		{64 * 1024, 64 << 20},    // 区间内直通
		{1024 * 1024, 128 << 20}, // 1GB 超上限 → 压到 128MB
	}
	for _, c := range cases {
		if got := SizeLimitForConfig(c.kb); got != c.want {
			t.Errorf("SizeLimitForConfig(%d) = %d, want %d", c.kb, got, c.want)
		}
	}
}
