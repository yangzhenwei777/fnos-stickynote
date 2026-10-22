package model

import (
	"testing"

	"stickynote/database"
)

// ============================================================================
// upload.go 单测（v21.77 P1：补 model 覆盖率）
// 覆盖：上传记录查询、计数、配额汇总、归属校验、删除失败清理队列
// ============================================================================

func TestUploadQueries(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	uid := createUserForTest(t, "user_up")
	uidB := createUserForTest(t, "user_up_b")

	if err := RecordUpload("a.png", uid, 100); err != nil {
		t.Fatal(err)
	}
	if err := RecordUpload("b.jpg", uid, 250); err != nil {
		t.Fatal(err)
	}
	if err := RecordUpload("c.gif", uidB, 50); err != nil {
		t.Fatal(err)
	}

	t.Run("ListUploadsByUser 只含本人", func(t *testing.T) {
		names, err := ListUploadsByUser(uid)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 2 {
			t.Fatalf("应 2 条, got %d (%v)", len(names), names)
		}
	})

	t.Run("CountUploadsByUser", func(t *testing.T) {
		cnt, err := CountUploadsByUser(uid)
		if err != nil {
			t.Fatal(err)
		}
		if cnt != 2 {
			t.Errorf("应 2, got %d", cnt)
		}
	})

	t.Run("SumUploadSizeByUser", func(t *testing.T) {
		total, err := SumUploadSizeByUser(uid)
		if err != nil {
			t.Fatal(err)
		}
		if total != 350 {
			t.Errorf("应 350, got %d", total)
		}
		// 无记录用户应为 0 且不报错
		zero, err := SumUploadSizeByUser(99999)
		if err != nil {
			t.Fatal(err)
		}
		if zero != 0 {
			t.Errorf("无记录应 0, got %d", zero)
		}
	})

	t.Run("GetUploadOwner 命中", func(t *testing.T) {
		owner, err := GetUploadOwner("a.png")
		if err != nil {
			t.Fatal(err)
		}
		if owner != uid {
			t.Errorf("owner 应 %d, got %d", uid, owner)
		}
	})
	t.Run("GetUploadOwner 无记录返回 0", func(t *testing.T) {
		owner, err := GetUploadOwner("ghost.png")
		if err != nil {
			t.Fatal(err)
		}
		if owner != 0 {
			t.Errorf("无记录应 0, got %d", owner)
		}
	})

	t.Run("QueueUploadCleanup 幂等入队", func(t *testing.T) {
		if err := QueueUploadCleanup("orphan.png", uid); err != nil {
			t.Fatal(err)
		}
		if err := QueueUploadCleanup("orphan.png", uid); err != nil {
			t.Fatal(err)
		}
		var cnt int
		if err := database.DB.QueryRow("SELECT COUNT(*) FROM pending_upload_cleanup WHERE filename='orphan.png'").Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 1 {
			t.Errorf("INSERT OR IGNORE 应保持 1 条, got %d", cnt)
		}
	})
}
