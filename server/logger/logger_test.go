package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 结构化审计日志（v18.9，优-6）：JSON Lines 固定字段
func TestAuditEvent(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)

	AuditEvent("login", "alice", "127.0.0.1", "success", "")
	AuditEvent("login", "mallory", "10.0.0.9", "failed", "bad credentials")

	today := time.Now().Format("2006-01-02")
	data, err := os.ReadFile(filepath.Join(dir, "audit-"+today+".log"))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("应写入 2 行，实际 %d", len(lines))
	}
	for i, line := range lines {
		// 行格式: "2006-01-02 15:04:05 [AUDIT] {json}"
		idx := strings.Index(line, "[AUDIT] ")
		if idx < 0 {
			t.Fatalf("第 %d 行缺 [AUDIT] 前缀: %q", i, line)
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line[idx+8:]), &e); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v", i, err)
		}
		if e.Time == "" || e.Action != "login" || e.Actor == "" || e.IP == "" || e.Result == "" {
			t.Errorf("第 %d 行固定字段不完整: %+v", i, e)
		}
	}
}
