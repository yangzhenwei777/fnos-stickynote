package model

import (
	"path/filepath"
	"sync"
	"testing"

	"stickynote/database"
)

// initTestDB 在临时目录初始化一套全新 SQLite（含全部 ensureSchema 补丁）。
// 各测试函数独立调用，互不共享数据库。
// 注意 appVersion 必须传合法语义版本号（upgrade_v1_0_0 的基表按版本闸口执行，
// 传 "test" 这类非版本串会跳过建表）；用足大的版本确保全部迁移落地。
func initTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(tempInstanceDir(t), "test.db")
	if err := database.Init(dbPath, "99.0.0"); err != nil {
		t.Fatalf("database.Init 失败: %v", err)
	}
	t.Cleanup(func() { database.DB.Close() }) // 释放文件句柄，TempDir 才能清理
}

// 首用户注册竞态（v18.7）：并发 10 个首注册应只有 1 个 admin
func TestRegisterUserRace(t *testing.T) {
	initTestDB(t)

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = RegisterUser(
				string(rune('a'+i))+"user", "Passw0rd!2026", "q", "a")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个注册失败: %v", i, err)
		}
	}

	var admins int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM users WHERE role = 'admin'").Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Errorf("并发首注册应仅产生 1 个 admin，实际 %d", admins)
	}
	var total int
	database.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&total)
	if total != n {
		t.Errorf("注册总数应为 %d，实际 %d", n, total)
	}
}

// 回收站可视化口径（v18.8，优-10）：条数 + 回收站笔记引用图片的占用字节
func TestGetTrashStats(t *testing.T) {
	initTestDB(t)

	u, err := RegisterUser("trashuser", "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatal(err)
	}

	// 初始：无回收站笔记 → 全零
	count, bytes, err := GetTrashStats(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || bytes != 0 {
		t.Errorf("空回收站应为 0/0，实际 %d/%d", count, bytes)
	}

	// 两条上传记录：一条被回收站笔记引用，一条未被引用（不计入）
	if err := RecordUpload("ref.png", u.ID, 1000); err != nil {
		t.Fatal(err)
	}
	if err := RecordUpload("unref.png", u.ID, 5000); err != nil {
		t.Fatal(err)
	}

	// 正常笔记（未删）引用了 ref.png —— 不应计入回收站统计
	if _, err := CreateNote(u.ID, "正常", `<img src="/uploads/ref.png" />`, "", 0); err != nil {
		t.Fatal(err)
	}
	count, bytes, _ = GetTrashStats(u.ID)
	if count != 0 || bytes != 0 {
		t.Errorf("正常笔记的引用不计入回收站统计，实际 %d/%d", count, bytes)
	}

	// 建一条带图笔记并软删 → 计入
	n, err := CreateNote(u.ID, "带图", `<p>x</p><img src="/uploads/ref.png" />`, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteNote(n.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	count, bytes, err = GetTrashStats(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("回收站条数应为 1，实际 %d", count)
	}
	if bytes != 1000 {
		t.Errorf("占用应只含被引用的 ref.png(1000)，不含 unref.png，实际 %d", bytes)
	}

	// 彻底删除 → 回落
	if err := PermanentDeleteNote(n.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	count, bytes, _ = GetTrashStats(u.ID)
	if count != 0 || bytes != 0 {
		t.Errorf("彻底删除后应回落 0/0，实际 %d/%d", count, bytes)
	}
}
