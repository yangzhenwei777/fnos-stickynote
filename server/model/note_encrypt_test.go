package model

import (
	"path/filepath"
	"strings"
	"testing"

	"stickynote/database"
)

// model 层加密笔记事务测试（v21.54 P0：加密笔记更新单事务——v21.52 P1 回归点）
// 覆盖：正常更新（密文+标记+标签同事务）、回滚后无明文残留、笔记不存在 ErrNoRows、标签同步。

func setupEncDB(t *testing.T) func() {
	t.Helper()
	if err := database.Init(filepath.Join(tempInstanceDir(t), "test.db"), "99.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	return func() {
		database.DB.Close()
		database.DB = nil
	}
}

func createEncryptedNoteForTest(t *testing.T, userID int64, title string) int64 {
	t.Helper()
	note, err := CreateNote(userID, title, "<p>明文初始</p>", "tagA", 0)
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	if err := SetEncrypted(note.ID, userID, "hash123", "cipher_old"); err != nil {
		t.Fatalf("set encrypted: %v", err)
	}
	return note.ID
}

// createUserForTest 建一个测试用户（满足 notes.user_id 外键约束）。
func createUserForTest(t *testing.T, username string) int64 {
	t.Helper()
	u, err := RegisterUser(username, "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatalf("register user: %v", err)
	}
	return u.ID
}

// TestUpdateEncryptedNote_Commit 正常更新：密文+标记+标签同事务落库，无明文残留。
func TestUpdateEncryptedNote_Commit(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_commit")
	nid := createEncryptedNoteForTest(t, uid, "加密笔记")

	err := UpdateEncryptedNote(nid, uid, "新标题", "tagA,tagB", "hash456", "cipher_new", "<p>新内容</p>", 1)
	if err != nil {
		t.Fatalf("update encrypted note: %v", err)
	}

	note, err := GetNote(nid, uid)
	if err != nil {
		t.Fatalf("get note: %v", err)
	}
	// 密文与标记一致、明文清空
	if !note.IsEncrypted {
		t.Error("is_encrypted 应为 true")
	}
	if note.EncryptedContent != "cipher_new" {
		t.Errorf("密文未更新: got %q", note.EncryptedContent)
	}
	if note.EncryptPasswordHash != "hash456" {
		t.Errorf("密码哈希未更新: got %q", note.EncryptPasswordHash)
	}
	if note.Content != "" {
		t.Errorf("明文应清空（不以明文持久化），got %q", note.Content)
	}
	if note.Title != "新标题" {
		t.Errorf("标题未更新: got %q", note.Title)
	}
}

// TestUpdateEncryptedNote_RollbackNoPlaintext 中间失败回滚后：不残留明文、不残留半更新状态。
// 通过传入不存在的笔记触发（UPDATE 0 行 → ErrNoRows → 事务回滚）。
func TestUpdateEncryptedNote_RollbackNoPlaintext(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_rollback")
	nid := createEncryptedNoteForTest(t, uid, "加密笔记")

	// 用错误 userID 触发 0 行命中 → ErrNoRows，事务应回滚
	err := UpdateEncryptedNote(nid, 999, "不该生效", "tagZ", "hashZ", "cipherZ", "<p>x</p>", 1)
	if err == nil {
		t.Fatal("错误 userID 应返回 ErrNoRows")
	}

	// 原数据必须保持完整（无明文写入、无半更新）
	note, err := GetNote(nid, uid)
	if err != nil {
		t.Fatalf("get note: %v", err)
	}
	if !note.IsEncrypted {
		t.Error("回滚后 is_encrypted 应为 true")
	}
	if note.EncryptedContent != "cipher_old" {
		t.Errorf("回滚后密文应保持原值: got %q", note.EncryptedContent)
	}
	if note.Content != "" {
		t.Errorf("回滚后不应有明文: got %q", note.Content)
	}
	if note.Title == "不该生效" {
		t.Error("回滚后标题不应被修改")
	}
}

// TestUpdateEncryptedNote_TagsSynced 标签同步：更新后 note_tags 关联表反映新标签。
func TestUpdateEncryptedNote_TagsSynced(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_tags")
	nid := createEncryptedNoteForTest(t, uid, "加密笔记")
	if err := UpdateEncryptedNote(nid, uid, "标题", "新标签1,新标签2", "hash", "cipher", "<p>x</p>", 1); err != nil {
		t.Fatalf("update: %v", err)
	}

	var cnt int
	database.DB.QueryRow("SELECT COUNT(*) FROM note_tags WHERE note_id = ?", nid).Scan(&cnt)
	if cnt != 2 {
		t.Errorf("标签关联应为 2 条，got %d", cnt)
	}
	var tags string
	database.DB.QueryRow("SELECT group_concat(tag) FROM note_tags WHERE note_id = ? ORDER BY tag", nid).Scan(&tags)
	if !strings.Contains(tags, "新标签1") || !strings.Contains(tags, "新标签2") {
		t.Errorf("标签内容错误: %q", tags)
	}
}

// TestSetEncrypted_ClearsPlaintext SetEncrypted 清空明文（加密路径核心不变量）。
func TestSetEncrypted_ClearsPlaintext(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_setenc")
	note, err := CreateNote(uid, "t", "<p>明文</p>", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetEncrypted(note.ID, uid, "hash", "cipher"); err != nil {
		t.Fatal(err)
	}
	var content string
	database.DB.QueryRow("SELECT content FROM notes WHERE id = ?", note.ID).Scan(&content)
	if content != "" {
		t.Errorf("SetEncrypted 后明文应清空，got %q", content)
	}
}
