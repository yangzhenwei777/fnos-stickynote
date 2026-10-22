package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"stickynote/model"
)

// ============================================================================
// note.go / note_trash.go / quota.go handler 单测（v21.77 P1：补 handler 覆盖率）
// 覆盖：列表、详情、删除、标签、批量删除、回收站全流程、配额
// ============================================================================

func TestListNotesHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("nl", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "列表1", "<p>a</p>", "x", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "列表2", "<p>b</p>", "y", 1000); err != nil {
		t.Fatal(err)
	}

	c, w := newNoteContext("GET", "/api/notes", "")
	ListNotes(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "列表1") {
		t.Errorf("应含笔记: %s", w.Body.String())
	}
}

func TestGetNoteHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("gn", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	n, err := model.CreateNote(1, "详情", "<p>内容</p>", "", 1000)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("存在 200", func(t *testing.T) {
		c, w := newNoteContext("GET", "/api/notes/1", "")
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		GetNote(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d", w.Code)
		}
	})
	t.Run("不存在 404", func(t *testing.T) {
		c, w := newNoteContext("GET", "/api/notes/9999", "")
		c.Params = gin.Params{{Key: "id", Value: "9999"}}
		GetNote(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("应 404, got %d", w.Code)
		}
	})
	t.Run("非法 id 400", func(t *testing.T) {
		c, w := newNoteContext("GET", "/api/notes/abc", "")
		c.Params = gin.Params{{Key: "id", Value: "abc"}}
		GetNote(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应 400, got %d", w.Code)
		}
	})
	_ = n
}

func TestDeleteNoteHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("dn", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "待删", "<p>x</p>", "", 1000); err != nil {
		t.Fatal(err)
	}

	t.Run("删除 200", func(t *testing.T) {
		c, w := newNoteContext("DELETE", "/api/notes/1", "")
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		DeleteNote(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("重复删除 404", func(t *testing.T) {
		c, w := newNoteContext("DELETE", "/api/notes/1", "")
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		DeleteNote(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("重复删除应 404, got %d", w.Code)
		}
	})
	t.Run("非法 id 400", func(t *testing.T) {
		c, w := newNoteContext("DELETE", "/api/notes/xyz", "")
		c.Params = gin.Params{{Key: "id", Value: "xyz"}}
		DeleteNote(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应 400, got %d", w.Code)
		}
	})
}

func TestGetAllTagsHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("gt", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "带标签", "<p>x</p>", "工作, 生活", 1000); err != nil {
		t.Fatal(err)
	}

	c, w := newNoteContext("GET", "/api/tags", "")
	GetAllTags(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "工作") {
		t.Errorf("应含标签: %s", w.Body.String())
	}
}

func TestBatchDeleteNotesHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("bd", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "b1", "<p>a</p>", "", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "b2", "<p>b</p>", "", 1000); err != nil {
		t.Fatal(err)
	}

	c, w := newNoteContext("POST", "/api/notes/batch-delete", `{"ids":[1,2]}`)
	c.Set("username", "bd")
	BatchDeleteNotes(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"deleted":2`) {
		t.Errorf("应删除 2 篇: %s", w.Body.String())
	}
}

func TestTrashLifecycleHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("tr", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "回收1", "<p>a</p>", "", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "回收2", "<p>b</p>", "", 1000); err != nil {
		t.Fatal(err)
	}
	// 删除两篇进回收站
	for _, id := range []string{"1", "2"} {
		c, w := newNoteContext("DELETE", "/api/notes/"+id, "")
		c.Params = gin.Params{{Key: "id", Value: id}}
		DeleteNote(c)
		if w.Code != http.StatusOK {
			t.Fatalf("前置删除失败: %d", w.Code)
		}
	}

	t.Run("ListTrash 200 含 2 篇", func(t *testing.T) {
		c, w := newNoteContext("GET", "/api/trash", "")
		ListTrash(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "回收1") {
			t.Errorf("应含回收站笔记: %s", w.Body.String())
		}
	})
	t.Run("TrashStats 200", func(t *testing.T) {
		c, w := newNoteContext("GET", "/api/trash/stats", "")
		TrashStats(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), `"count":2`) {
			t.Errorf("count 应=2: %s", w.Body.String())
		}
	})
	t.Run("RestoreNote 200", func(t *testing.T) {
		c, w := newNoteContext("POST", "/api/trash/1/restore", "")
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		RestoreNote(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
		}
		// v21.107 P2-9：恢复响应须携带去向文件夹字段（默认「我的笔记」）
		if !strings.Contains(w.Body.String(), `"folder"`) {
			t.Errorf("恢复响应应含 folder 字段: %s", w.Body.String())
		}
	})
	t.Run("RestoreNote 重复恢复 404", func(t *testing.T) {
		c, w := newNoteContext("POST", "/api/trash/1/restore", "")
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		RestoreNote(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("已恢复再恢复应 404, got %d", w.Code)
		}
	})
	t.Run("PermanentDeleteNote 非法 id 400", func(t *testing.T) {
		c, w := newNoteContext("DELETE", "/api/trash/abc", "")
		c.Params = gin.Params{{Key: "id", Value: "abc"}}
		PermanentDeleteNote(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应 400, got %d", w.Code)
		}
	})
	t.Run("PermanentDeleteNote 200", func(t *testing.T) {
		c, w := newNoteContext("DELETE", "/api/trash/2", "")
		c.Params = gin.Params{{Key: "id", Value: "2"}}
		PermanentDeleteNote(c)
		if w.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("EmptyTrash 清空剩余", func(t *testing.T) {
		// 恢复的 1 号笔记再删一次，保证回收站有内容
		c, w := newNoteContext("DELETE", "/api/notes/1", "")
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		DeleteNote(c)
		_ = w
		c2, w2 := newNoteContext("POST", "/api/trash/empty", "")
		EmptyTrash(c2)
		if w2.Code != http.StatusOK {
			t.Fatalf("应 200, got %d body=%s", w2.Code, w2.Body.String())
		}
		if !strings.Contains(w2.Body.String(), `"deleted":1`) {
			t.Errorf("应清空 1 篇: %s", w2.Body.String())
		}
	})
}

func TestGetQuotaHandler(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	if _, err := model.RegisterUser("qu", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateNote(1, "配额测试", "<p>x</p>", "", 1000); err != nil {
		t.Fatal(err)
	}

	c, w := newNoteContext("GET", "/api/quota", "")
	GetQuota(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, k := range []string{"notes_count", "notes_limit", "storage_bytes", "storage_limit_mb"} {
		if !strings.Contains(body, `"`+k+`"`) {
			t.Errorf("应含字段 %s: %s", k, body)
		}
	}
}
