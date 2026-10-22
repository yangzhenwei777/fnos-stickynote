package model

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// v21.87 笔记移动/复制到文件夹单测（纪律 4.2：正常/边界/错误三态）。
// 复用 note_quota_test 的 DB setup 与 note_encrypt_test 的建用户助手。

func setupMoveDB(t *testing.T) func() {
	return setupQuotaDB(t)
}

// TestMoveNoteToFolder 正常路径：默认→自定义→默认，folder_id 往返正确；
// 移动不动 version / updated_at（设计决策：不触发乐观锁、不顶"最近修改"）。
func TestMoveNoteToFolder(t *testing.T) {
	cleanup := setupMoveDB(t)
	defer cleanup()

	uid := createUserForTest(t, "move_user")
	f, _ := CreateFolder(uid, "目标")
	fid := *f.ID

	note, err := CreateNoteInFolder(uid, "标题", "<p>内容</p>", "a,b", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 默认 → 自定义
	if err := MoveNoteToFolder(note.ID, uid, &fid); err != nil {
		t.Fatal(err)
	}
	got, _ := GetNote(note.ID, uid)
	if got.FolderID == nil || *got.FolderID != fid {
		t.Fatalf("移动后 folder_id 应指向自定义文件夹 got %v", got.FolderID)
	}

	// version / updated_at 不变（与移动前 DB 读回值比较——内存 time.Now() 与
	// SQLite CURRENT_TIMESTAMP 时区/精度天然不同，不能跨存储比较）
	before := got
	if err := MoveNoteToFolder(note.ID, uid, nil); err != nil { // 再移一次（自定义→默认）
		t.Fatal(err)
	}
	got2, _ := GetNote(note.ID, uid)
	if got2.FolderID != nil {
		t.Fatalf("移回默认后 folder_id 应 nil got %v", got2.FolderID)
	}
	if got2.Version != before.Version {
		t.Fatalf("移动不应改 version, want %d got %d", before.Version, got2.Version)
	}
	if !got2.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("移动不应改 updated_at, want %v got %v", before.UpdatedAt, got2.UpdatedAt)
	}
}

// TestMoveNoteErrors 错误路径：笔记不存在 / 文件夹不存在 / 越权文件夹。
func TestMoveNoteErrors(t *testing.T) {
	cleanup := setupMoveDB(t)
	defer cleanup()

	uid1 := createUserForTest(t, "move_owner")
	uid2 := createUserForTest(t, "move_attacker")

	f, _ := CreateFolder(uid1, "私有")
	fid := *f.ID

	// 笔记不存在
	if err := MoveNoteToFolder(99999, uid1, &fid); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("笔记不存在应 ErrNoRows got %v", err)
	}
	// 目标文件夹不存在
	bad := int64(88888)
	if err := MoveNoteToFolder(1, uid1, &bad); !errors.Is(err, ErrInvalidFolder) {
		t.Fatalf("非法文件夹应 ErrInvalidFolder got %v", err)
	}
	// 越权移到他人文件夹
	note, _ := CreateNoteInFolder(uid2, "t", "<p>x</p>", "", 0, nil)
	if err := MoveNoteToFolder(note.ID, uid2, &fid); !errors.Is(err, ErrInvalidFolder) {
		t.Fatalf("越权文件夹应 ErrInvalidFolder got %v", err)
	}
}

// TestCopyNoteToFolder 正常路径：字段完整复制 + 副本标题 + version=1 + 源笔记不动。
func TestCopyNoteToFolder(t *testing.T) {
	cleanup := setupMoveDB(t)
	defer cleanup()

	uid := createUserForTest(t, "copy_user")
	f, _ := CreateFolder(uid, "副本去向")
	fid := *f.ID

	src, err := CreateNoteInFolder(uid, "原笔记", "<p>内容<img src=\"/uploads/a.png\"></p>", "x,y", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	cp, err := CopyNoteToFolder(src.ID, uid, &fid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID == src.ID {
		t.Fatal("副本应是新笔记")
	}
	if cp.Title != "原笔记 (副本)" {
		t.Fatalf("副本标题应带后缀 got %q", cp.Title)
	}
	if cp.Version != 1 {
		t.Fatalf("新笔记 version 应 1 got %d", cp.Version)
	}
	if cp.FolderID == nil || *cp.FolderID != fid {
		t.Fatalf("副本应归属目标文件夹 got %v", cp.FolderID)
	}
	if cp.Content != src.Content || cp.Tags != src.Tags {
		t.Fatal("副本 content/tags 应与源一致")
	}

	// 源笔记不动（仍 1 篇在默认 + 1 篇在目标）
	got, _ := GetNote(src.ID, uid)
	if got.FolderID != nil {
		t.Fatalf("源笔记应仍在默认文件夹 got %v", got.FolderID)
	}

	// 复制到默认（nil）
	cp2, err := CopyNoteToFolder(src.ID, uid, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cp2.FolderID != nil {
		t.Fatalf("复制到默认应 folder_id=nil got %v", cp2.FolderID)
	}
}

// TestCopyNoteQuota 配额：达上限后复制被拒（源创建时 maxNotes 未超，复制时超）。
func TestCopyNoteQuota(t *testing.T) {
	cleanup := setupMoveDB(t)
	defer cleanup()

	uid := createUserForTest(t, "copy_quota_user")
	src, _ := CreateNoteInFolder(uid, "t", "<p>x</p>", "", 0, nil)

	if _, err := CopyNoteToFolder(src.ID, uid, nil, 1); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("配额满应 ErrQuotaExceeded got %v", err)
	}
	// 0 = 不限
	if _, err := CopyNoteToFolder(src.ID, uid, nil, 0); err != nil {
		t.Fatalf("不限配额应成功 got %v", err)
	}
}

// TestCopyNoteErrors 错误路径：源笔记不存在 / 非法（越权）文件夹。
func TestCopyNoteErrors(t *testing.T) {
	cleanup := setupMoveDB(t)
	defer cleanup()

	uid1 := createUserForTest(t, "copy_owner")
	uid2 := createUserForTest(t, "copy_attacker")

	f, _ := CreateFolder(uid1, "私有")
	fid := *f.ID

	if _, err := CopyNoteToFolder(99999, uid1, nil, 0); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("源不存在应 ErrNoRows got %v", err)
	}
	note, _ := CreateNoteInFolder(uid2, "t", "<p>x</p>", "", 0, nil)
	if _, err := CopyNoteToFolder(note.ID, uid2, &fid, 0); !errors.Is(err, ErrInvalidFolder) {
		t.Fatalf("越权文件夹应 ErrInvalidFolder got %v", err)
	}
}

// TestCopyTitleTruncation 边界：超长标题 + " (副本)" 后按 rune 截断到 200。
func TestCopyTitleTruncation(t *testing.T) {
	long := strings.Repeat("字", 199) // 199 + " (副本)"(5 字符) = 204 > 200
	got := copyTitle(long)
	if rn := len([]rune(got)); rn != 200 {
		t.Fatalf("截断后应 200 rune got %d", rn)
	}
	// 空标题兜底
	if got := copyTitle(""); got != "无标题 (副本)" {
		t.Fatalf("空标题应兜底 got %q", got)
	}
}
