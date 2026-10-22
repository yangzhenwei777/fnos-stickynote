// v21.196 桌面端可观测性回归钉（O1 文件日志 / O3 runtime 状态标记 / O2 诊断包）。
//
// 为什么要有这组测试：195 白屏事故的排障困难有一半来自「GUI 程序日志只进 stdout，
// 用户侧拿不到」。这组测试把「日志真的落盘、过期日志真的被清、runtime 降级真的
// 被标记、诊断包真的能导出且不含笔记正文」钉成可回归的断言。

package main

import (
	"archive/zip"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupFileLoggingWritesFileAndKeepsStdout(t *testing.T) {
	dir := t.TempDir()
	closeLog, st, err := setupFileLogging(dir) // v21.198：新增落盘自检返回值
	if err != nil {
		t.Fatalf("setupFileLogging: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("落盘自检应通过，size=%d err=%s", st.Size, st.Err)
	}
	log.Printf("observability-test-marker")
	closeLog()

	ents, err := os.ReadDir(filepath.Join(dir, "logs"))
	if err != nil || len(ents) != 1 {
		t.Fatalf("logs dir: %v entries=%d", err, len(ents))
	}
	if !strings.HasPrefix(ents[0].Name(), "app-") || !strings.HasSuffix(ents[0].Name(), ".log") {
		t.Fatalf("unexpected log name: %s", ents[0].Name())
	}
	b, err := os.ReadFile(filepath.Join(dir, "logs", ents[0].Name()))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(b), "observability-test-marker") {
		t.Fatalf("log file missing marker, content=%q", string(b))
	}
}

func TestPruneOldLogsKeepsRecentDropsExpired(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	old := "app-" + time.Now().AddDate(0, 0, -30).Format("2006-01-02") + ".log"
	keep := "app-" + time.Now().Format("2006-01-02") + ".log"
	junk := "notes-2020-01-01.log"
	mk(old)
	mk(keep)
	mk(junk)

	pruneOldLogs(dir, logKeepDays)

	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Fatalf("过期日志未被清理: %s", old)
	}
	if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
		t.Fatalf("当日日志被误删: %s", keep)
	}
	if _, err := os.Stat(filepath.Join(dir, junk)); err != nil {
		t.Fatalf("非 app- 前缀文件被误删: %s", junk)
	}
}

func TestRuntimeStatusRoundTripAndDegradedFlag(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readRuntimeStatus(dir); ok {
		t.Fatal("空目录不应读到 runtime 状态")
	}
	st := RuntimeStatus{Source: "system", Degraded: true, Error: "webview2-runtime.tar.xz 缺失"}
	writeRuntimeStatus(dir, st)

	got, ok := readRuntimeStatus(dir)
	if !ok {
		t.Fatal("写入后读不到 runtime 状态")
	}
	if !got.Degraded || got.Error == "" || got.At == "" {
		t.Fatalf("降级信息丢失: %+v", got)
	}

	// 坏JSON 不得 panic，按「未知」处理
	if err := os.WriteFile(filepath.Join(dir, runtimeStatusFile), []byte("{broken"), 0600); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	if _, ok := readRuntimeStatus(dir); ok {
		t.Fatal("坏 JSON 应读为不存在")
	}
}

func TestExportDiagnosticsProducesZipWithoutNoteContent(t *testing.T) {
	dir := t.TempDir()
	// 造最小现场：日志 + runtime 状态 + 代际清单
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "app-2026-10-08.log"), []byte("hello-log"), 0600); err != nil {
		t.Fatal(err)
	}
	writeRuntimeStatus(dir, RuntimeStatus{Pinned: true, Source: "pinned", Version: "133.0.3065.92"})
	if err := os.MkdirAll(filepath.Join(dir, "generations", "g-000001"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generations", "g-000001", "manifest.json"), []byte(`{"generation":"g-000001"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// 笔记正文文件即使存在也不得进包（隐私）
	if err := os.WriteFile(filepath.Join(dir, "secret-note.md"), []byte("TOP-SECRET-CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}

	ls := &LocalService{dir: dir}
	out, err := ls.ExportDiagnostics()
	if err != nil {
		t.Fatalf("ExportDiagnostics: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("诊断包不存在: %v", err)
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"summary.txt", "logs/app-2026-10-08.log", "profile/runtime/" + runtimeStatusFile} {
		if !names[want] {
			t.Fatalf("诊断包缺少 %s（实际: %v）", want, keys(names))
		}
	}
	for n := range names {
		if strings.Contains(n, "secret-note") {
			t.Fatalf("诊断包泄露笔记正文: %s", n)
		}
	}
	// summary 必含版本与 runtime 来源
	rc, err := zr.File[findEntry(zr, "summary.txt")].Open()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	sum := string(b)
	for _, want := range []string{"version=", "runtime.pinned=true", "source=pinned", "webview2.last_version="} {
		if !strings.Contains(sum, want) {
			t.Fatalf("summary 缺少 %s：\n%s", want, sum)
		}
	}
}

func findEntry(zr *zip.ReadCloser, name string) int {
	for i, f := range zr.File {
		if f.Name == name {
			return i
		}
	}
	return -1
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
