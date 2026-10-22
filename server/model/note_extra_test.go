package model

import (
	"strings"
	"testing"

	"stickynote/database"
)

// ============================================================================
// note.go 补充单测（v21.77 P1：补 model 覆盖率 32%→50%）
// 覆盖：标签、导出列表、加密屏蔽/清除/内容读取/计数、批量查询、版本冲突
// ============================================================================

func TestSplitTags(t *testing.T) {
	t.Run("空串", func(t *testing.T) {
		if got := splitTags(""); got != nil {
			t.Errorf("空串应 nil, got %v", got)
		}
	})
	t.Run("多标签去空白", func(t *testing.T) {
		got := splitTags(" 工作, 生活 ,, 学习 ")
		if len(got) != 3 || got[0] != "工作" || got[1] != "生活" || got[2] != "学习" {
			t.Errorf("切分不符: %v", got)
		}
	})
	t.Run("单标签", func(t *testing.T) {
		got := splitTags("工作")
		if len(got) != 1 || got[0] != "工作" {
			t.Errorf("单标签不符: %v", got)
		}
	})
}

func TestGetAllTags(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_tag")
	uidB := createUserForTest(t, "user_tag_b")

	// 用户 A 两篇笔记带标签
	if _, err := CreateNote(uid, "n1", "<p>x</p>", "工作, 生活", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateNote(uid, "n2", "<p>y</p>", "工作", 0); err != nil {
		t.Fatal(err)
	}
	// 用户 B 一篇，标签不应串到 A
	if _, err := CreateNote(uidB, "b1", "<p>z</p>", "别人的标签", 0); err != nil {
		t.Fatal(err)
	}

	tags, err := GetAllTags(uid)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(tags, ",")
	if !strings.Contains(got, "工作") || !strings.Contains(got, "生活") {
		t.Errorf("应含 工作/生活, got %v", tags)
	}
	if strings.Contains(got, "别人的标签") {
		t.Errorf("标签不应跨用户串扰: %v", tags)
	}
}

func TestListAllNotesForExport(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_exp")
	if _, err := CreateNote(uid, "exp1", "<p>a</p>", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateNote(uid, "exp2", "<p>b</p>", "", 0); err != nil {
		t.Fatal(err)
	}
	// 回收站笔记不导出
	n3, _ := CreateNote(uid, "exp3", "<p>c</p>", "", 0)
	if err := DeleteNote(n3.ID, uid); err != nil {
		t.Fatal(err)
	}

	notes, err := ListAllNotesForExport(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Errorf("应导出 2 篇（回收站排除）, got %d", len(notes))
	}
}

func TestMaskEncryptedNote(t *testing.T) {
	t.Run("加密笔记屏蔽明文", func(t *testing.T) {
		n := &Note{IsEncrypted: true, Content: "<p>机密</p>"}
		MaskEncryptedNote(n)
		if n.Content != "" {
			t.Error("加密笔记 content 应被清空")
		}
	})
	t.Run("普通笔记不受影响", func(t *testing.T) {
		n := &Note{IsEncrypted: false, Content: "<p>普通</p>"}
		MaskEncryptedNote(n)
		if n.Content != "<p>普通</p>" {
			t.Error("普通笔记 content 不应被清空")
		}
	})
}

func TestSetClearEncrypted(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_enc2")
	nid := createEncryptedNoteForTest(t, uid, "加密往返")

	// 已加密：content 空、密文与哈希在库
	n, err := GetNote(nid, uid)
	if err != nil {
		t.Fatal(err)
	}
	if !n.IsEncrypted || n.Content != "" {
		t.Errorf("应加密且 content 为空: %+v", n)
	}
	hash, cipher, err := GetEncryptedContent(nid, uid)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || cipher == "" {
		t.Error("应取到哈希与密文")
	}

	// 解密（ClearEncrypted）后恢复明文
	if err := ClearEncrypted(nid, uid, "<p>明文恢复</p>", "明文恢复"); err != nil {
		t.Fatal(err)
	}
	n2, _ := GetNote(nid, uid)
	if n2.IsEncrypted || n2.Content != "<p>明文恢复</p>" {
		t.Errorf("解密后应明文: %+v", n2)
	}
}

func TestCountEncryptedNotes(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_cnt")
	createEncryptedNoteForTest(t, uid, "密1")
	createEncryptedNoteForTest(t, uid, "密2")
	if _, err := CreateNote(uid, "普通", "<p>p</p>", "", 0); err != nil {
		t.Fatal(err)
	}

	var cnt int
	CountEncryptedNotes(uid, &cnt)
	if cnt != 2 {
		t.Errorf("加密笔记应 2, got %d", cnt)
	}
}

func TestListNotesByIDs(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_ids")
	n1, _ := CreateNote(uid, "id1", "<p>a</p>", "", 0)
	n2, _ := CreateNote(uid, "id2", "<p>b</p>", "", 0)
	uidB := createUserForTest(t, "user_ids_b")
	nB, _ := CreateNote(uidB, "b-note", "<p>c</p>", "", 0)

	t.Run("空列表返回 nil", func(t *testing.T) {
		got, err := ListNotesByIDs(nil, uid)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Errorf("空列表应 nil, got %v", got)
		}
	})
	t.Run("命中本人 ID 且过滤他人", func(t *testing.T) {
		notes, err := ListNotesByIDs([]int64{n1.ID, n2.ID, nB.ID}, uid)
		if err != nil {
			t.Fatal(err)
		}
		if len(notes) != 2 {
			t.Errorf("应只返回本人 2 篇, got %d", len(notes))
		}
	})
}

// TestUpdateEncryptedNote_VersionConflict 加密笔记版本冲突（v21.72 P2-08）。
func TestUpdateEncryptedNote_VersionConflict(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_enc_vc")
	nid := createEncryptedNoteForTest(t, uid, "冲突测试")

	if err := UpdateEncryptedNote(nid, uid, "t", "", "h", "c", "<p>p</p>", 1); err != nil {
		t.Fatalf("正确 version=1 应成功: %v", err)
	}
	if err := UpdateEncryptedNote(nid, uid, "t2", "", "h2", "c2", "<p>q</p>", 1); err != ErrVersionConflict {
		t.Errorf("旧 version 应返回 ErrVersionConflict, got %v", err)
	}
	if err := UpdateEncryptedNote(99999, uid, "t", "", "h", "c", "<p>r</p>", 1); err == nil {
		t.Error("笔记不存在应报错")
	}
}

// TestGetAllTags_DeletedNoteExcluded 回收站笔记的标签不出现（GetAllTags 过滤 deleted_at）。
func TestGetAllTags_DeletedNoteExcluded(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_tag_del")
	n, err := CreateNote(uid, "待删", "<p>x</p>", "临时标签", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteNote(n.ID, uid); err != nil {
		t.Fatal(err)
	}
	tags, err := GetAllTags(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 0 {
		t.Errorf("回收站笔记标签不应出现: %v", tags)
	}
	_ = database.DB
}
