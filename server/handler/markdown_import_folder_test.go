package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"stickynote/model"
)

// v2.2.1 导入落当前文件夹（Web 端 folder_id 接线）handler 级单测。
// 回针：带 folder_id 导入 → 笔记落选中文件夹（修复前恒落「我的笔记」）。
// 反向对照：不传/传 "0" → 默认文件夹（老客户端零影响）；非法值/负数 → 400；
// 不存在/他人文件夹 → 400（app 层归属校验，同 note.go CreateNote 口径）。

// newImportCtx 构造 multipart 导入请求上下文（file 字段 + 可选 folder_id 字段）。
func newImportCtx(userID int64, folderID string) (*gin.Context, *httptest.ResponseRecorder) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "note.md")
	_, _ = fw.Write([]byte("# 导入标题\n\n正文内容"))
	if folderID != "" {
		_ = mw.WriteField("folder_id", folderID)
	}
	_ = mw.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/notes/import", &buf)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	c.Set("userID", userID)
	return c, w
}

func importNoteBody(w *httptest.ResponseRecorder) *model.Note {
	var note model.Note
	_ = json.Unmarshal(w.Body.Bytes(), &note)
	return &note
}

func TestImportFile_Folder(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	u, _ := model.RegisterUser("imp_folder", "Passw0rd!2026", "q", "a")

	t.Run("回针·带folder_id落选中文件夹", func(t *testing.T) {
		f, err := model.CreateFolder(u.ID, "工作")
		if err != nil {
			t.Fatal(err)
		}
		c, w := newImportCtx(u.ID, strconv.FormatInt(*f.ID, 10))
		ImportFile(c)
		if w.Code != http.StatusCreated {
			t.Fatalf("应 201, got %d body=%s", w.Code, w.Body.String())
		}
		note := importNoteBody(w)
		if note.FolderID == nil || *note.FolderID != *f.ID {
			t.Fatalf("导入应落选中文件夹 %d, got %v（修复前恒落默认=本断言必红）", *f.ID, note.FolderID)
		}
	})

	t.Run("反向·不传folder_id落默认（老客户端兼容）", func(t *testing.T) {
		c, w := newImportCtx(u.ID, "")
		ImportFile(c)
		if w.Code != http.StatusCreated {
			t.Fatalf("应 201, got %d body=%s", w.Code, w.Body.String())
		}
		if note := importNoteBody(w); note.FolderID != nil {
			t.Fatalf("不传 folder_id 应落默认文件夹, got %v", note.FolderID)
		}
	})

	t.Run("反向·folder_id=0归一化为默认", func(t *testing.T) {
		c, w := newImportCtx(u.ID, "0")
		ImportFile(c)
		if w.Code != http.StatusCreated {
			t.Fatalf("应 201, got %d body=%s", w.Code, w.Body.String())
		}
		if note := importNoteBody(w); note.FolderID != nil {
			t.Fatalf("folder_id=0 应归一化为默认文件夹, got %v", note.FolderID)
		}
	})

	t.Run("反向·非法值拒绝400", func(t *testing.T) {
		for _, bad := range []string{"abc", "-1", "1.5"} {
			c, w := newImportCtx(u.ID, bad)
			ImportFile(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("folder_id=%q 应 400, got %d body=%s", bad, w.Code, w.Body.String())
			}
		}
	})

	t.Run("反向·不存在/他人文件夹拒绝400", func(t *testing.T) {
		other, _ := model.RegisterUser("imp_other", "Passw0rd!2026", "q", "a")
		f, _ := model.CreateFolder(other.ID, "他人的文件夹")
		// 他人文件夹
		c, w := newImportCtx(u.ID, strconv.FormatInt(*f.ID, 10))
		ImportFile(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("他人文件夹应 400, got %d body=%s", w.Code, w.Body.String())
		}
		// 不存在的文件夹
		c2, w2 := newImportCtx(u.ID, "99999")
		ImportFile(c2)
		if w2.Code != http.StatusBadRequest {
			t.Fatalf("不存在文件夹应 400, got %d body=%s", w2.Code, w2.Body.String())
		}
	})
}
