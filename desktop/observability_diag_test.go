// observability_diag_test.go —— v21.198 用户实机 0 字节故障的三处回归钉。
//
// 实机症状（2026-10-09）：连续 3 个诊断包 **0 字节**，第 4 个才有 1387 字节；
// 且包内 logs/app-2026-10-09.log **存在但大小 0**，而 runtime-status.json 正常写出。
// 三处根因（本文件逐条钉住）：
//
//	① io.MultiWriter 遇首个 writer 失败即return → GUI 无控制台时文件日志全丢；
//	② os.Create 立即建文件 + defer zw.Close() → 进程中途终止留 0 字节包；
//	③ pinnedRuntimeVersion 判Count(".")==2 永不匹配 `133.0.3065.92.manifest`（4 个点）。
package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- ① 落盘 writer：文件侧必须独立于 stdout 成活 ----

// failingWriter 模拟 GUI 程序 stdout 失效（写必报错）。
type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLogFileWriterSurvivesBrokenStdout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	w := &logFileWriter{file: f, std: failingWriter{}}
	if _, err := w.Write([]byte("第一行：文件必须收到\n")); err != nil {
		t.Fatalf("Write 不应因 stdout 失败而返回错误: %v", err)
	}
	// 再写一条，模拟 stdout 正常后的行为
	w2 := &logFileWriter{file: f, std: io.Discard}
	_, _ = w2.Write([]byte("第二行\n"))

	b, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("read back: %v", rerr)
	}
	if len(b) == 0 {
		t.Fatal("文件日志为 0 字节 —— io.MultiWriter 的致命缺陷未被修复")
	}
	s := string(b)
	if !strings.Contains(s, "第一行") || !strings.Contains(s, "第二行") {
		t.Fatalf("文件内容缺失: %q", s)
	}
}

// 反向对照：证明 io.MultiWriter 在同样条件下确实写不出文件内容
// （钉住「为什么不能用 MultiWriter」这个决策依据，防止有人改回去）。
func TestIoMultiWriterWouldSwallowFileOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	mw := io.MultiWriter(failingWriter{}, f)
	_, _ = mw.Write([]byte("应被吞掉\n"))
	b, _ := os.ReadFile(path)
	if len(b) != 0 {
		t.Skip("当前 Go 版本的 MultiWriter 行为已变，反向对照不适用")
	}
	// 到达这里说明 MultiWriter 确实把文件侧一起吞掉了 —— 即实机 0 字节日志的成因。
	t.Log("已复现：MultiWriter 因stdout 失败导致文件侧 0 字节（实机症状同源）")
}

func TestSetupFileLoggingSelfCheck(t *testing.T) {
	dir := t.TempDir()
	closer, st, err := setupFileLogging(dir)
	if err != nil {
		t.Fatalf("setupFileLogging: %v", err)
	}
	defer closer()
	if !st.Enabled {
		t.Fatalf("落盘自检应通过，size=%d err=%s", st.Size, st.Err)
	}
	if st.Path == "" {
		t.Fatal("自检路径为空")
	}
	b, _ := os.ReadFile(st.Path)
	if len(b) == 0 {
		t.Fatal("日志文件实际为空")
	}
	if !strings.Contains(string(b), "file logging enabled") {
		t.Fatalf("首条自检日志缺失: %q", string(b))
	}
}

// ---- ② 诊断包原子写：绝不留 0 字节 zip ----

func TestExportDiagnosticsWritesNonEmptyZipAtomically(t *testing.T) {
	dir := t.TempDir()
	ls := NewLocalService(dir)

	// 造一个非空日志，确保包里一定有内容
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		t.Fatal(err)
	}
	logBody := "[diag-test] 现场日志\n"
	if err := os.WriteFile(filepath.Join(logDir, "app-2026-10-09.log"), []byte(logBody), 0600); err != nil {
		t.Fatal(err)
	}

	abs, err := ls.ExportDiagnostics()
	if err != nil {
		t.Fatalf("ExportDiagnostics: %v", err)
	}
	fi, serr := os.Stat(abs)
	if serr != nil {
		t.Fatalf("stat: %v", serr)
	}
	if fi.Size() == 0 {
		t.Fatal("导出包为 0 字节 —— 原子写未生效（实机症状同源）")
	}
	// 磁盘上不得残留 .tmp
	if ents, _ := os.ReadDir(filepath.Join(dir, "diagnostics")); len(ents) != 1 {
		t.Fatalf("diagnostics 目录应只有 1 个 zip（含 .tmp 则说明未清理）: %d 项", len(ents))
	}
	// 包内必须含 summary.txt 与日志，且日志内容非空
	zr, zerr := zip.OpenReader(abs)
	if zerr != nil {
		t.Fatalf("zip 不可打开（结构损坏）: %v", zerr)
	}
	defer zr.Close()
	gotLog := false
	gotSummary := false
	summaryHasLogSelfCheck := false
	for _, f := range zr.File {
		switch f.Name {
		case "summary.txt":
			gotSummary = true
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if strings.Contains(string(b), "log.enabled=") {
				summaryHasLogSelfCheck = true
			}
		case "logs/app-2026-10-09.log":
			gotLog = true
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if len(b) == 0 {
				t.Fatal("包内日志条目为 0 字节")
			}
		}
	}
	if !gotSummary {
		t.Fatal("包内缺 summary.txt")
	}
	if !gotLog {
		t.Fatal("包内缺 logs/app-*.log")
	}
	if !summaryHasLogSelfCheck {
		t.Fatal("summary.txt 缺落盘自检字段 log.enabled= —— 让日志失效可观测")
	}
}

// ---- ③ 钉版版本号解析 ----

func TestPinnedRuntimeVersionParsesRealManifestName(t *testing.T) {
	dir := t.TempDir()
	// 实机真实文件名：133.0.3065.92.manifest 有 **4 个点**
	if err := os.WriteFile(filepath.Join(dir, "133.0.3065.92.manifest"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := pinnedRuntimeVersion(dir); got != "133.0.3065.92" {
		t.Fatalf("版本号解析失败：got %q want 133.0.3065.92", got)
	}
}

func TestPinnedRuntimeVersionIgnoresNonVersionManifest(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ExtensionInstallForced.manifest"), []byte("x"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "133.0.3065.92.manifest"), []byte("x"), 0600)
	if got := pinnedRuntimeVersion(dir); got != "133.0.3065.92" {
		t.Fatalf("应跳过非版本名 manifest，got %q", got)
	}
}

func TestLooksLikeWebView2Version(t *testing.T) {
	cases := map[string]bool{
		"133.0.3065.92":   true,
		"120.0.2210.91":   true,
		"1.2.3.4":         true,
		"133.0.3065":      false,
		"133.0.3065.92.1": false,
		"133.0.3065.a2":   false,
		"0133.0.3065.92":  false,
		"":                false,
	}
	for v, want := range cases {
		if got := looksLikeWebView2Version(v); got != want {
			t.Errorf("looksLikeWebView2Version(%q) = %v, want %v", v, got, want)
		}
	}
}
