package notetext

// export_zip_test.go — v2.2.2 R6 共享 zip 导出层回归钉。
// 桌面批量/全部导出与 Web 端共用本实现，命名清洗/重名去重/加密跳过必须锁死。

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"stickynote/model"
)

func TestSanitizeExportFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"正常标题", "正常标题"},
		{"a/b:c*d?e\"f<g>h|i", "a_b_c_d_e_f_g_h_i"},
		{"  前后空格. ", "前后空格"},
		{"", "未命名"},
		{"...", "未命名"},
		{strings.Repeat("长", 100), strings.Repeat("长", 80)},
	}
	for _, c := range cases {
		if got := SanitizeExportFilename(c.in); got != c.want {
			t.Errorf("SanitizeExportFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDedupeExportNameCaseInsensitive(t *testing.T) {
	used := map[string]bool{}
	if got := DedupeExportName(used, "Note"); got != "Note" {
		t.Fatalf("首个应为 Note，得 %q", got)
	}
	if got := DedupeExportName(used, "note"); got != "note(2)" {
		t.Fatalf("大小写不敏感重名应加序号，得 %q", got)
	}
	if got := DedupeExportName(used, "NOTE"); got != "NOTE(3)" {
		t.Fatalf("第三个应加序号 3，得 %q", got)
	}
}

func TestWriteNotesZIPSkipsEncryptedAndDedupes(t *testing.T) {
	notes := []model.Note{
		{ID: 1, Title: "标题", Content: "<p>第一篇</p>"},
		{ID: 2, Title: "标题", Content: "<p>第二篇</p>"},
		{ID: 3, Title: "加密件", IsEncrypted: true, Content: ""},
	}
	var buf bytes.Buffer
	written, skipped, err := WriteNotesZIP(&buf, notes, nil, true)
	if err != nil {
		t.Fatalf("WriteNotesZIP: %v", err)
	}
	if written != 2 || skipped != 1 {
		t.Fatalf("written/skipped = %d/%d, want 2/1", written, skipped)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip 不可读: %v", err)
	}
	names := make(map[string]bool)
	contents := make(map[string]string)
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		contents[f.Name] = string(b)
	}
	for _, want := range []string{"标题.md", "标题(2).md"} {
		if !names[want] {
			t.Errorf("缺少条目 %q（实际: %v）", want, names)
		}
	}
	if names["加密件.md"] {
		t.Error("加密笔记不应进入 zip")
	}
	if !strings.Contains(contents["标题.md"], "第一篇") || !strings.Contains(contents["标题(2).md"], "第二篇") {
		t.Errorf("条目内容错位: %v", contents)
	}
}

func TestWriteNotesZIPEmpty(t *testing.T) {
	var buf bytes.Buffer
	written, skipped, err := WriteNotesZIP(&buf, nil, nil, true)
	if err != nil || written != 0 || skipped != 0 {
		t.Fatalf("空列表应产出空 zip: written=%d skipped=%d err=%v", written, skipped, err)
	}
	if _, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
		t.Fatalf("空 zip 不可读: %v", err)
	}
}
