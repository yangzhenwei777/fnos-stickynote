package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stickynote/changelog"
	"stickynote/database"
	"stickynote/model"
)

// TestNotePayloadHashGuarantee —— ADR-003 R4 协议保证位：桌面收到的任何 attachment_ref
// sha256 非空——BuildNotePayload 对 ” 行经 HashProvider 即时补算；未接线时不阻断事件。
func TestNotePayloadHashGuarantee(t *testing.T) {
	setupDB(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pic.png"), []byte("png-bytes-123"), 0600); err != nil {
		t.Fatal(err)
	}
	// uploads 归属行以旧路径创建（sha=''，模拟历史数据）。
	if err := model.RecordUpload("pic.png", 1, 11); err != nil {
		t.Fatal(err)
	}
	svc := NewServerNoteService(100)

	// 未接线 provider：事件照常产出（哈希留空由回填兜底），不得因缺哈希而失败。
	changelog.HashProvider = nil
	n1, err := svc.CreateNote(1, "带图1", `<p><img src="/uploads/pic.png"></p>`, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	evs := changeEvents(t, 1)
	if len(evs) != 1 || !strings.Contains(evs[0].Payload, `"sha256":""`) {
		t.Fatalf("未接线时应产出空哈希事件: %+v", evs)
	}

	// 接线后：CREATE 事件 attachment_refs.sha256 非空（只读即时补算；落库由回填收敛）。
	changelog.HashProvider = func(filename string) (string, error) {
		return model.FileSHA256(filepath.Join(dir, filename))
	}
	defer func() { changelog.HashProvider = nil }()
	if _, err := svc.CreateNote(1, "带图2", `<p><img src="/uploads/pic.png"></p>`, "", nil); err != nil {
		t.Fatal(err)
	}
	evs = changeEvents(t, 1)
	last := evs[len(evs)-1]
	if strings.Contains(last.Payload, `"sha256":""`) {
		t.Fatalf("接线后事件必须携带非空哈希: %s", last.Payload)
	}
	if !strings.Contains(last.Payload, "6dbb3cf1") && !strings.Contains(last.Payload, `"sha256":"`) {
		t.Fatalf("payload 缺 sha256 字段: %s", last.Payload)
	}
	// 即时补算只保证事件载荷非空，不写库（写库=回填任务的职责，事务纪律）。
	if got := model.GetUploadSHA256("pic.png"); got != "" {
		t.Fatalf("provider 不应写库（回填收敛职责分离）: %q", got)
	}
	_ = n1
	_ = database.DB
}
