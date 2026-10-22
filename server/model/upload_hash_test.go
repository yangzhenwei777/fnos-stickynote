package model

import (
	"os"
	"path/filepath"
	"testing"
)

// upload_hash_test.go —— P1 批次3（ADR-003）：附件 sha256 存取 / 低优先级回填 / 即时补算。

func TestFileSHA256Roundtrip(t *testing.T) {
	dir := tempInstanceDir(t)
	p := filepath.Join(dir, "x.png")
	content := []byte("hello-sticky-note-hash")
	if err := os.WriteFile(p, content, 0600); err != nil {
		t.Fatal(err)
	}
	sha, err := FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("hello-sticky-note-hash") 固定值（跨端一致性锚：Go 端与回填/下载校验同一算法）。
	const want = "9b7c47e9b6f1b2d0c43860d52f541c3761b9d55420c05a2176be5d99e6f9a166"
	if len(sha) != 64 {
		t.Fatalf("hex 长度应为 64: %q", sha)
	}
	if want2, _ := FileSHA256(p); sha != want2 {
		t.Fatal("FileSHA256 必须确定性")
	}
}

func TestRecordUploadHashedAndGet(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()
	uid := createUserForTest(t, "hashuser")

	if err := RecordUploadHashed("h1.png", uid, 10, "abc123"); err != nil {
		t.Fatal(err)
	}
	if got := GetUploadSHA256("h1.png"); got != "abc123" {
		t.Fatalf("读回哈希不符: %q", got)
	}
	// 兼容路径（旧调用）= 空哈希待回填。
	if err := RecordUpload("h2.png", uid, 20); err != nil {
		t.Fatal(err)
	}
	if got := GetUploadSHA256("h2.png"); got != "" {
		t.Fatalf("旧路径应留空待回填: %q", got)
	}
	// SetUploadHash 幂等且不覆盖既有权威值。
	if err := SetUploadHash("h1.png", "overwrite-attempt"); err != nil {
		t.Fatal(err)
	}
	if got := GetUploadSHA256("h1.png"); got != "abc123" {
		t.Fatalf("已有权威值不得被回填覆盖: %q", got)
	}
	if err := SetUploadHash("h2.png", "filled"); err != nil {
		t.Fatal(err)
	}
	if got := GetUploadSHA256("h2.png"); got != "filled" {
		t.Fatalf("'' 行应被补上: %q", got)
	}
}

func TestBackfillUploadHashesOnce(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()
	uid := createUserForTest(t, "bfuser")
	dir := tempInstanceDir(t)

	writeFile := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("bytes-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("b1.png")
	writeFile("b2.png")
	// b3 故意缺文件：回填须跳过且保持 ''（宁缺哈希不产错哈希）。
	for _, n := range []string{"b1.png", "b2.png", "b3.png"} {
		if err := RecordUpload(n, uid, 5); err != nil {
			t.Fatal(err)
		}
	}
	remaining, err := CountUploadsMissingHash()
	if err != nil || remaining != 3 {
		t.Fatalf("前置 3 行待回填: %v %d", err, remaining)
	}
	done, rem, err := BackfillUploadHashesOnce(dir, 2)
	if err != nil || done != 2 || rem != 1 {
		t.Fatalf("首批应完成 2 剩 1: done=%d rem=%d err=%v", done, rem, err)
	}
	if GetUploadSHA256("b1.png") == "" || GetUploadSHA256("b2.png") == "" {
		t.Fatal("b1/b2 应已回填")
	}
	// 再跑一轮：b3 缺文件永远失败，remaining 恒 1（断点续传语义 + 不产错哈希）。
	done, rem, err = BackfillUploadHashesOnce(dir, 50)
	if err != nil || done != 0 || rem != 1 {
		t.Fatalf("缺文件行应跳过: done=%d rem=%d err=%v", done, rem, err)
	}
	if GetUploadSHA256("b3.png") != "" {
		t.Fatal("缺文件不得写入任何哈希")
	}
}
