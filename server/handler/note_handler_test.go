package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"stickynote/model"
)

// handler 层笔记流测试（v21.54 P0：CreateNote 配额/加密编辑/更新 404——v18.4/v20.0/v21.52 回归点）
// 基建：真实 SQLite（setupNoteDB 已存在）+ gin.CreateTestContext。

func newNoteContext(method, path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	// gin.SetMode 已在 init_test.go 包 init() 里一次性完成；此处不再重复调用，
	// 避免并发测试（如 TestRefreshRotation_ConcurrentSameToken）里多 goroutine
	// 同时 SetMode 触发 CI Linux race 检测的 DATA RACE。
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("userID", int64(1))
	// 手动注入路径参数（CreateTestContext 不走路由，Param 需手动设置）
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	return c, w
}

// newNoteContextID 指定笔记 ID 的上下文（用于 404 场景）。
func newNoteContextID(id string) (*gin.Context, *httptest.ResponseRecorder) {
	c, w := newNoteContext("PUT", "/api/notes/"+id, `{"title":"x","content":"<p>x</p>","tags":""}`)
	c.Params = gin.Params{{Key: "id", Value: id}}
	return c, w
}

// TestCreateNote_Success 创建笔记成功：201 + 笔记返回。
func TestCreateNote_Success(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("creator", "Passw0rd!2026", "q", "a") // 满足外键

	c, w := newNoteContext("POST", "/api/notes", `{"title":"测试","content":"<p>内容</p>","tags":"a,b"}`)
	CreateNote(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("应 201, got %d body=%s", w.Code, w.Body.String())
	}
	var note model.Note
	if err := json.Unmarshal(w.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if note.Title != "测试" || note.ID == 0 {
		t.Errorf("笔记返回异常: %+v", note)
	}
}

// TestCreateNote_QuotaExceeded 配额超限：max_notes_per_user=2 时第 3 篇 403。
func TestCreateNote_QuotaExceeded(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("quota", "Passw0rd!2026", "q", "a")
	model.SetConfig("max_notes_per_user", "2")

	for i := 0; i < 2; i++ {
		c, w := newNoteContext("POST", "/api/notes", `{"title":"n","content":"<p>x</p>","tags":""}`)
		CreateNote(c)
		if w.Code != http.StatusCreated {
			t.Fatalf("第 %d 篇应 201, got %d", i+1, w.Code)
		}
	}
	c, w := newNoteContext("POST", "/api/notes", `{"title":"第3篇","content":"<p>x</p>","tags":""}`)
	CreateNote(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("第 3 篇应 403（配额 2）, got %d", w.Code)
	}
}

// TestCreateNote_EmptyTitle 空标题 400。
func TestCreateNote_EmptyTitle(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("notitle", "Passw0rd!2026", "q", "a")

	c, w := newNoteContext("POST", "/api/notes", `{"title":"","content":"<p>x</p>","tags":""}`)
	CreateNote(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空标题应 400, got %d", w.Code)
	}
}

// TestUpdateNote_EncryptedNote 加密笔记编辑：密码正确更新成功；缺密码 400。
func TestUpdateNote_EncryptedNote(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("encedit", "Passw0rd!2026", "q", "a")

	c, w := newNoteContext("POST", "/api/notes", `{"title":"加密","content":"<p>原文</p>","tags":""}`)
	CreateNote(c)
	var note model.Note
	json.Unmarshal(w.Body.Bytes(), &note)

	// 先加密（EncryptNote handler 需要 JSON body 含 password + confirm_password）
	c2, w2 := newNoteContext("PUT", "/api/notes/1/encrypt", `{"password":"secret123","confirm_password":"secret123"}`)
	c2.Params = gin.Params{{Key: "id", Value: "1"}}
	EncryptNote(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("加密应 200, got %d body=%s", w2.Code, w2.Body.String())
	}

	// 编辑加密笔记：缺密码 → 400
	c3, w3 := newNoteContext("PUT", "/api/notes/1", `{"title":"新","content":"<p>新内容</p>","tags":""}`)
	c3.Params = gin.Params{{Key: "id", Value: "1"}}
	UpdateNote(c3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("加密笔记缺密码应 400, got %d", w3.Code)
	}

	// 带密码 → 200 且内容为明文（响应解密态）。基点=2：P2 批次1 起 EncryptNote 同事务
	// version++（v20 §60，SOURCE-FACT #9），加密响应携带新版本。
	c4, w4 := newNoteContext("PUT", "/api/notes/1", `{"title":"新标题","content":"<p>新内容</p>","tags":"","password":"secret123","version":2}`)
	c4.Params = gin.Params{{Key: "id", Value: "1"}}
	UpdateNote(c4)
	if w4.Code != http.StatusOK {
		t.Fatalf("加密笔记带密码应 200, got %d body=%s", w4.Code, w4.Body.String())
	}
	var updated model.Note
	json.Unmarshal(w4.Body.Bytes(), &updated)
	if updated.IsEncrypted != true {
		t.Error("更新后仍应保持加密")
	}
	// DB 中不得有明文（持久化不变量）
	dbNote, _ := model.GetNote(note.ID, 1)
	if dbNote.Content != "" {
		t.Errorf("DB 中明文应清空, got %q", dbNote.Content)
	}
}

// TestUpdateNote_ResponseIncludesUpdatedAt v21.150 E7：普通更新响应携带 updated_at（列表排序键单一时间源）。
func TestUpdateNote_ResponseIncludesUpdatedAt(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("updtsync", "Passw0rd!2026", "q", "a")

	c, w := newNoteContext("POST", "/api/notes", `{"title":"甲","content":"<p>原文</p>","tags":""}`)
	CreateNote(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201, got %d body=%s", w.Code, w.Body.String())
	}
	var created model.Note
	json.Unmarshal(w.Body.Bytes(), &created)

	body := fmt.Sprintf(`{"title":"乙","content":"<p>新文</p>","tags":"","version":%d}`, created.Version)
	cu, wu := newNoteContext("PUT", "/api/notes/1", body)
	cu.Params = gin.Params{{Key: "id", Value: "1"}}
	UpdateNote(cu)
	if wu.Code != http.StatusOK {
		t.Fatalf("更新应 200, got %d body=%s", wu.Code, wu.Body.String())
	}
	var resp struct {
		Version   int64     `json:"version"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(wu.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.UpdatedAt.IsZero() {
		t.Fatalf("响应应包含 updated_at, body=%s", wu.Body.String())
	}
	if resp.Version != created.Version+1 {
		t.Errorf("version 应递增: got %d want %d", resp.Version, created.Version+1)
	}
	dbNote, err := model.GetNote(created.ID, 1)
	if err != nil || dbNote == nil {
		t.Fatalf("回读笔记失败: %v", err)
	}
	if !dbNote.UpdatedAt.Equal(resp.UpdatedAt) {
		t.Errorf("updated_at 应与库内一致: resp=%v db=%v", resp.UpdatedAt, dbNote.UpdatedAt)
	}
}

// TestUpdateNote_NotFound 更新不存在/他人笔记 → 404。
func TestUpdateNote_NotFound(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("owner", "Passw0rd!2026", "q", "a")

	c, w := newNoteContextID("999")
	UpdateNote(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的笔记应 404, got %d", w.Code)
	}
}

// TestEncryptNote_PasswordTooLong 超长密码（>72 字节）拒绝加密，防数据不可恢复（v21.67 P1-01）。
func TestEncryptNote_PasswordTooLong(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("enc_long", "Passw0rd!2026", "q", "a")

	c, w := newNoteContext("POST", "/api/notes", `{"title":"n","content":"<p>x</p>","tags":""}`)
	CreateNote(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建笔记失败: %d", w.Code)
	}
	var note model.Note
	_ = json.Unmarshal(w.Body.Bytes(), &note)

	longPwd := strings.Repeat("a", 80) // 80 字节 > 72 上限
	body := fmt.Sprintf(`{"password":"%s","confirm_password":"%s"}`, longPwd, longPwd)
	c2, w2 := newNoteContext("POST", "/api/notes/"+strconv.FormatInt(note.ID, 10)+"/encrypt", body)
	EncryptNote(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("超长密码应 400, got %d body=%s", w2.Code, w2.Body.String())
	}
}

// TestUpdateNote_VersionConflict 版本冲突：旧 version 更新 → 409（v21.72 P2-08 并发编辑乐观锁）。
func TestUpdateNote_VersionConflict(t *testing.T) {
	cleanup := setupNoteDB(t)
	defer cleanup()
	model.RegisterUser("vc", "Passw0rd!2026", "q", "a")

	c, w := newNoteContext("POST", "/api/notes", `{"title":"n","content":"<p>x</p>","tags":""}`)
	CreateNote(c)
	var note model.Note
	_ = json.Unmarshal(w.Body.Bytes(), &note)

	// 用错误 version（99）更新 → 409 冲突
	c2, w2 := newNoteContext("PUT", "/api/notes/"+strconv.FormatInt(note.ID, 10), `{"title":"n2","content":"<p>y</p>","tags":"","version":99}`)
	UpdateNote(c2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("版本冲突应 409, got %d body=%s", w2.Code, w2.Body.String())
	}
}
