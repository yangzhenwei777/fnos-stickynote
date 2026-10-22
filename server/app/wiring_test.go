package app

import (
	"os"
	"path/filepath"
	"testing"

	"stickynote/database"
)

// ---- Folder ----

func TestFolderServiceLifecycle(t *testing.T) {
	setupDB(t)
	fs := NewFolderService()

	f, err := fs.Create(1, "桌面夹")
	if err != nil || f == nil || f.ID == nil {
		t.Fatalf("CreateFolder: %v %+v", err, f)
	}
	list, err := fs.List(1)
	if err != nil || len(list) < 2 { // 虚拟默认 + 自定义
		t.Fatalf("ListFolders 应含默认+自定义: %v len=%d", err, len(list))
	}
	if err := fs.Rename(*f.ID, 1, "改名夹"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Move(*f.ID, 1, "bogus-direction"); err == nil {
		t.Fatal("非法方向应报错")
	}
	// 非空守卫：夹内有笔记 → FOLDER_NOT_EMPTY（SOURCE-FACT #15）
	svc := NewNoteService(100)
	n, err := svc.CreateNote(1, "在夹笔记", "x", "", f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete(*f.ID, 1); err == nil || ErrorCodeFor(err) != "FOLDER_NOT_EMPTY" {
		t.Fatalf("非空删除应 FOLDER_NOT_EMPTY，得 %v", err)
	}
	if _, err := svc.MoveNoteToFolder(n.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete(*f.ID, 1); err != nil {
		t.Fatalf("清空后删除应成功: %v", err)
	}
}

// ---- 加密往返（与 handler 语义逐条对齐） ----

func TestNoteCryptoRoundtrip(t *testing.T) {
	setupDB(t)
	svc := NewNoteService(100)
	n, err := svc.CreateNote(1, "secret", "<p>顶层明文</p>", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.EncryptNote(n.ID, 1, "pw123", "mismatch"); err == nil {
		t.Fatal("双入口口令不一致应失败")
	}
	if _, err := svc.EncryptNote(n.ID, 1, "pw123", "pw123"); err != nil {
		t.Fatal(err)
	}
	// 加密后 content 清空（§29 依据）；重复加密被拦截。
	if got, _ := svc.GetNote(n.ID, 1); got.Content != "" {
		t.Fatalf("加密后 content 应为空串，得 %q", got.Content)
	}
	if _, err := svc.EncryptNote(n.ID, 1, "pw", "pw"); ErrorCodeFor(err) != "ALREADY_ENCRYPTED" {
		t.Fatalf("重复加密应 ALREADY_ENCRYPTED，得 %v", err)
	}
	// 错口令 → WRONG_PASSWORD；对口令 → 内存态明文回显（不落库）。
	if _, err := svc.VerifyEncrypt(n.ID, 1, "nope"); ErrorCodeFor(err) != "WRONG_PASSWORD" {
		t.Fatalf("错口令应 WRONG_PASSWORD，得 %v", err)
	}
	view, err := svc.VerifyEncrypt(n.ID, 1, "pw123")
	if err != nil || view.Content != "<p>顶层明文</p>" || !view.IsEncrypted {
		t.Fatalf("Verify 回显不符: %v %+v", err, view)
	}
	// 加密态更新：CAS 正确版本 + 口令验证 → version+1，标题更新，仍密文存储。
	up, err := svc.UpdateEncrypted(n.ID, 1, "secret2", "", "<p>改后明文</p>", "pw123", n.Version)
	if err != nil {
		t.Fatalf("UpdateEncrypted: %v", err)
	}
	if up.Version != n.Version+1 || up.Content != "" {
		t.Fatalf("加密更新语义不符: %+v", up)
	}
	// 解密还原：明文回写 content，加密标记清除。
	plain, err := svc.DecryptNote(n.ID, 1, "pw123")
	if err != nil || plain != "<p>改后明文</p>" {
		t.Fatalf("Decrypt: %v %q", err, plain)
	}
	if got, _ := svc.GetNote(n.ID, 1); got.IsEncrypted || got.Content != "<p>改后明文</p>" {
		t.Fatalf("解密后状态不符: %+v", got)
	}
}

// ---- Upload 校验链 ----

func TestUploadService(t *testing.T) {
	setupDB(t)
	dir := t.TempDir()
	up := NewUploadService(dir)

	validPng := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0xD, 0, 0, 0, 0} // 头 16B：magic 即嗅探为 image/png
	name, url, err := up.SaveBytes(1, "照片.png", append(append([]byte{}, validPng...), make([]byte, 64)...))
	if err != nil || name == "" || url != "/uploads/"+name {
		t.Fatalf("合法 PNG 应成功: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, name)); serr != nil {
		t.Fatalf("文件应落盘: %v", serr)
	}
	// 归属表有行（staged 由 model.RecordUpload 落库）
	var cnt int
	if qerr := database.DB.QueryRow("SELECT COUNT(*) FROM uploads WHERE filename = ?", name).Scan(&cnt); qerr != nil || cnt != 1 {
		t.Fatalf("uploads 归属行缺失: cnt=%d err=%v", cnt, qerr)
	}

	if _, _, err := up.SaveBytes(1, "evil.exe", []byte("MZ...")); ErrorCodeFor(err) != "FILE_TYPE_REJECTED" {
		t.Fatalf("非白名单扩展名应 FILE_TYPE_REJECTED，得 %v", err)
	}
	if _, _, err := up.SaveBytes(1, "fake.png", []byte("plain text definitely not png content here")); ErrorCodeFor(err) != "FILE_TYPE_REJECTED" {
		t.Fatalf("内容与扩展名不符应 FILE_TYPE_REJECTED，得 %v", err)
	}
	if _, _, err := up.ReadStored("../notepad.db"); err == nil {
		t.Fatal("路径穿越必须拒绝")
	}
}
