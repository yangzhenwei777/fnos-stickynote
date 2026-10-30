package app

import (
	"errors"
	"testing"

	"stickynote/model"
)

// v2.3.0 文件夹访问锁服务：bcrypt 校验、version 推进、与笔记加密正交。
func TestFolderEncryptDecryptUnlock(t *testing.T) {
	setupDB(t)
	fs := NewServerFolderService()
	f, err := fs.Create(1, "私密文件夹")
	if err != nil || f.ID == nil {
		t.Fatalf("create folder: %v", err)
	}
	fid := *f.ID

	ns := NewServerNoteService(0)
	if _, err := ns.CreateNote(1, "秘密笔记", "<p>x</p>", "t", &fid); err != nil {
		t.Fatalf("create note: %v", err)
	}

	// 空密码 / 不一致 / 超长
	if _, err := fs.Encrypt(fid, 1, "", ""); !errors.Is(err, ErrPasswordInvalid) {
		t.Fatalf("空密码应 ErrPasswordInvalid, got %v", err)
	}
	if _, err := fs.Encrypt(fid, 1, "pw123456", "other"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("不一致应 ErrPasswordMismatch, got %v", err)
	}

	v, err := fs.Encrypt(fid, 1, "pw123456", "pw123456")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if v != 2 {
		t.Fatalf("加密应推进 version 到 2, got %d", v)
	}
	name, enc, hash, _, err := model.GetFolderEncryptInfo(fid, 1)
	if err != nil || !enc || hash == "" || hash == "pw123456" {
		t.Fatalf("锁定态/哈希不符: name=%s enc=%v hash=%q err=%v", name, enc, hash, err)
	}

	// 未解锁列表不可见；授权后可见
	res, err := model.ListNotes(1, 1, 20, "", "", "updated_at", nil)
	if err != nil || res.Total != 0 {
		t.Fatalf("未解锁应 0 条: %d err=%v", res.Total, err)
	}
	res, _ = model.ListNotes(1, 1, 20, "", "", "updated_at", nil, map[int64]struct{}{fid: {}})
	if res.Total != 1 {
		t.Fatalf("授权后应 1 条, got %d", res.Total)
	}

	// 解锁校验
	if _, _, err := fs.Unlock(fid, 1, "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("错误密码应 ErrWrongPassword, got %v", err)
	}
	h, _, err := fs.Unlock(fid, 1, "pw123456")
	if err != nil || h == "" {
		t.Fatalf("正确密码解锁失败: %v", err)
	}

	// 重复加密
	if _, err := fs.Encrypt(fid, 1, "pw123456", "pw123456"); !errors.Is(err, model.ErrFolderAlreadyEncrypted) {
		t.Fatalf("重复加密应 ErrFolderAlreadyEncrypted, got %v", err)
	}

	// 解密
	if _, err := fs.Decrypt(fid, 1, "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("解密错误密码应 ErrWrongPassword, got %v", err)
	}
	v, err = fs.Decrypt(fid, 1, "pw123456")
	if err != nil || v != 3 {
		t.Fatalf("解密应到 v3: v=%d err=%v", v, err)
	}
	_, enc, hash, _, _ = model.GetFolderEncryptInfo(fid, 1)
	if enc || hash != "" {
		t.Fatalf("解密后应清空: enc=%v hash=%q", enc, hash)
	}
	res, _ = model.ListNotes(1, 1, 20, "", "", "updated_at", nil)
	if res.Total != 1 {
		t.Fatalf("解密后笔记应恢复可见, got %d", res.Total)
	}
}
