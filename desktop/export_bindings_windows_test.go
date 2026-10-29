//go:build windows

package main

// export_bindings_windows_test.go — 桌面导出绑定回归钉（v2.2.2 R2 桌面同步补齐）：
// 桌面端「下载」此前 /notes/:id/export 未接线（LocalTransport 501）+ window.open 在
// Wails 壳内弹空窗，功能整体不可用。绑定 ExportNoteMD 必须与 Web 端 handler.ExportNote
// 同语义：非加密直出 / 加密 bcrypt 验口令后解密直出，且列宽信息原样保留在导出
// Markdown 中（列宽表格走原生 HTML 块）。
// v2.2.2 R4（方案 A）：列宽按**百分比**导出（未拖列按已拖列均值补齐 → 180/180 =
// 50%/50%），表格 table-layout:fixed + width:100%，保证 md 在任意查看器窗口下与
// 预览/长图同比例；px 列宽会重新引入容器宽度依赖，必须不残留。

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestExportNoteMDPlainKeepsColWidth(t *testing.T) {
	ls, _ := bootWithLogin(t)
	content := `<p>表格</p><table><colgroup><col style="width: 180px;"><col style="min-width: 25px;"></colgroup><tbody><tr><th colwidth="180"><p>A</p></th><th><p>B</p></th></tr><tr><td colwidth="180"><p>1</p></td><td><p>2</p></td></tr></tbody></table>`
	n, err := ls.CreateNote("列宽导出", content, "", nil)
	if err != nil || n == nil {
		t.Fatalf("create: %v", err)
	}
	md, err := ls.ExportNoteMD(n.ID, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, want := range []string{"# 列宽导出", "<colgroup>", "table-layout: fixed; width: 100%", `<col style="width: 50%"/>`, `colwidth="180"`} {
		if !strings.Contains(md, want) {
			t.Errorf("导出应保留 %q\n输出: %s", want, md)
		}
	}
	if strings.Contains(md, "width: 180px") {
		t.Errorf("不应残留 px 列宽（会重新引入容器宽度依赖）\n输出: %s", md)
	}
	if strings.Contains(md, "| A |") {
		t.Errorf("带列宽表格不应退化为 pipe 表\n输出: %s", md)
	}
}

func TestExportNoteMDEncrypted(t *testing.T) {
	ls, _ := bootWithLogin(t)
	n, err := ls.CreateNote("加密导出", "<p>secret-body</p>", "", nil)
	if err != nil || n == nil {
		t.Fatalf("create: %v", err)
	}
	if err := ls.EncryptNote(n.ID, "pw123456", "pw123456"); err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// 反向对照：错口令必须拒绝
	if md, err := ls.ExportNoteMD(n.ID, "wrong-pass"); err == nil {
		t.Fatalf("错口令应拒绝，却得到: %s", md)
	} else if !strings.Contains(err.Error(), "密码错误") {
		t.Fatalf("错口令文案应可上屏: %v", err)
	}
	// 空口令 fail-closed
	if _, err := ls.ExportNoteMD(n.ID, ""); err == nil {
		t.Fatal("加密笔记空口令应拒绝")
	}
	// 正口令：明文出、密文不出
	md, err := ls.ExportNoteMD(n.ID, "pw123456")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(md, "secret-body") {
		t.Errorf("正口令应导出明文\n输出: %s", md)
	}
}

func TestExportNoteMDNotFound(t *testing.T) {
	ls, _ := bootWithLogin(t)
	if _, err := ls.ExportNoteMD(999999, ""); err == nil || !strings.Contains(err.Error(), "笔记不存在") {
		t.Fatalf("不存在的笔记应报「笔记不存在」，得: %v", err)
	}
}

// ---- v2.2.2 R6：桌面「导出全部 / 批量导出」绑定 ----

// stubSaveDialog 注入固定保存路径（生产为 Wails 原生另存为）。
func stubSaveDialog(t *testing.T, path string, called *bool) {
	t.Helper()
	old := saveFileDialog
	saveFileDialog = func(context.Context, wailsRuntime.SaveDialogOptions) (string, error) {
		if called != nil {
			*called = true
		}
		return path, nil
	}
	t.Cleanup(func() { saveFileDialog = old })
}

func readZipEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("zip 不可读: %v", err)
	}
	defer zr.Close()
	out := make(map[string]string)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestExportNotesZipAll(t *testing.T) {
	ls, _ := bootWithLogin(t)
	ls.ctx = context.Background()
	for _, title := range []string{"甲", "乙"} {
		if _, err := ls.CreateNote(title, "<p>"+title+"正文</p>", "", nil); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
	}
	out := filepath.Join(t.TempDir(), "all.zip")
	stubSaveDialog(t, out, nil)

	res, err := ls.ExportNotesZip(nil, true)
	if err != nil {
		t.Fatalf("ExportNotesZip: %v", err)
	}
	if res["cancelled"] == true {
		t.Fatal("不应返回 cancelled")
	}
	if got := res["exported"]; got != 2 {
		t.Fatalf("exported = %v, want 2", got)
	}
	entries := readZipEntries(t, out)
	if len(entries) != 2 {
		t.Fatalf("zip 条目 = %d, want 2: %v", len(entries), entries)
	}
	for _, name := range []string{"甲.md", "乙.md"} {
		if _, ok := entries[name]; !ok {
			t.Errorf("缺少条目 %q（实际: %v）", name, entries)
		}
	}
}

func TestExportNotesZipBatchSkipsEncrypted(t *testing.T) {
	ls, _ := bootWithLogin(t)
	ls.ctx = context.Background()
	n1, _ := ls.CreateNote("普通一", "<p>一</p>", "", nil)
	n2, _ := ls.CreateNote("普通二", "<p>二</p>", "", nil)
	n3, _ := ls.CreateNote("加密件", "<p>秘密</p>", "", nil)
	if err := ls.EncryptNote(n3.ID, "pw123456", "pw123456"); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	out := filepath.Join(t.TempDir(), "batch.zip")
	stubSaveDialog(t, out, nil)

	res, err := ls.ExportNotesZip([]int64{n1.ID, n2.ID, n3.ID}, false)
	if err != nil {
		t.Fatalf("ExportNotesZip: %v", err)
	}
	if got := res["exported"]; got != 2 {
		t.Fatalf("exported = %v, want 2", got)
	}
	if got := res["skipped"]; got != 1 {
		t.Fatalf("skipped = %v, want 1", got)
	}
	entries := readZipEntries(t, out)
	if _, ok := entries["加密件.md"]; ok {
		t.Error("加密笔记不应进入 zip")
	}
	if len(entries) != 2 {
		t.Fatalf("zip 条目 = %d, want 2: %v", len(entries), entries)
	}
}

func TestExportNotesZipCancelled(t *testing.T) {
	ls, _ := bootWithLogin(t)
	ls.ctx = context.Background()
	if _, err := ls.CreateNote("甲", "<p>正文</p>", "", nil); err != nil {
		t.Fatal(err)
	}
	stubSaveDialog(t, "", nil)
	res, err := ls.ExportNotesZip(nil, true)
	if err != nil {
		t.Fatalf("取消不应报错: %v", err)
	}
	if res["cancelled"] != true {
		t.Fatalf("应返回 cancelled=true，得 %v", res)
	}
}

func TestExportNotesZipNoNotes(t *testing.T) {
	ls, _ := bootWithLogin(t)
	ls.ctx = context.Background()
	var dialogCalled bool
	stubSaveDialog(t, filepath.Join(t.TempDir(), "none.zip"), &dialogCalled)
	if _, err := ls.ExportNotesZip(nil, true); err == nil || !strings.Contains(err.Error(), "没有找到可导出的笔记") {
		t.Fatalf("空库应报「没有找到可导出的笔记」，得: %v", err)
	}
	if dialogCalled {
		t.Error("无笔记时不应弹保存对话框")
	}
}

func TestExportNotesZipOverwritesExistingTarget(t *testing.T) {
	ls, _ := bootWithLogin(t)
	ls.ctx = context.Background()
	if _, err := ls.CreateNote("甲", "<p>正文</p>", "", nil); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "exists.zip")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubSaveDialog(t, out, nil)
	if _, err := ls.ExportNotesZip(nil, true); err != nil {
		t.Fatalf("覆盖已存在目标应成功: %v", err)
	}
	if entries := readZipEntries(t, out); len(entries) != 1 {
		t.Fatalf("覆盖后应为有效 zip（条目 1），实际: %v", entries)
	}
}
