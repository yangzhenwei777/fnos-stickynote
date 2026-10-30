package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/microcosm-cc/bluemonday"
	"stickynote/app"
	"stickynote/database"
	"stickynote/logger"
	"stickynote/model"
	"stickynote/notetext"
)

// 本文件原持有「文件 → 笔记」的全部转换实现（标题提取/消毒、md/txt/html → HTML、
// GFM 任务清单直构）。v21.140 起转换与消毒实现下沉到零 gin 依赖的 `stickynote/notetext`，
// 使**桌面离线导入**能复用同一套规则（见 desktop/import_bindings.go）。
// handler 侧保留 ImportFile 的传输层职责（multipart 解析、大小闸门、配额、落库事件）
// 与同名薄封装，既有调用点与单测零改动。

// ImportFile 导入文本文件（.md/.txt/.html/.htm）为笔记。
func ImportFile(c *gin.Context) {
	userID, _ := c.Get("userID")

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择文件"})
		return
	}
	defer file.Close()

	if !notetext.AllowedExt(notetext.ExtOf(header.Filename)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 .md / .txt / .html / .htm 文件"})
		return
	}

	// 大小限制：与 max_note_content_kb 一致（0=不限制）。
	// v21.52 P3 审计修复：header.Size 是客户端声明值不可信，改用 io.LimitReader
	// 硬限制实际读取量；v21.55：读取上限跟随前台【单篇笔记上限】配置（与动态中间件同源），
	// 管理后台改配置后 ≤5s 生效，无需改代码。
	maxKB := getMaxImportKB()
	if maxKB > 0 && header.Size > int64(maxKB)*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("文件过大，限制 %dKB（与单篇笔记上限一致）", maxKB)})
		return
	}

	// 动态导入上限：跟随 max_note_content_kb（X>0 → X 字节 + 余量；X=0 → 100MB 兜底），
	// 与 middleware.DynamicMaxBodyLimit 同规则（导入文件本身 ≈ 内容大小，无需 JSON 转义余量，
	// 直接用 X，X=0 时 100MB）。
	maxImportSize := importSizeLimit()
	raw, err := io.ReadAll(io.LimitReader(file, maxImportSize+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取文件失败"})
		return
	}
	if int64(len(raw)) > maxImportSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("导入文件过大，上限 %dMB", maxImportSize/(1<<20))})
		return
	}

	// 转换与消毒全部委托 notetext（与桌面离线导入同一实现，产出逐字节一致）。
	// 扩展名在上方已校验，此处 ErrUnsupportedExt 属理论不可达，仍按防御性分支处理。
	title, htmlContent, cerr := notetext.Convert(header.Filename, raw)
	if cerr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 .md / .txt / .html / .htm 文件"})
		return
	}

	// v2.2.1：Web 端补齐「导入落当前文件夹」。v21.213 只修了桌面路径（当时前端
	// FormData 已带 folder_id，但注释明言「Web 端 handler 忽略该字段」=本处一直
	// 没接线），用户在 fnOS 网页导入恒落「我的笔记」。语义对齐 note.go CreateNote
	// 与前端 transport.folderOfForm：缺失/空/"0" → nil（默认文件夹，老客户端零
	// 影响）；非数字/负数 → 400 拒绝（fail-closed，防脏数据静默落错位置）。
	var folderID *int64
	if v := strings.TrimSpace(c.Request.FormValue("folder_id")); v != "" {
		n, ferr := strconv.ParseInt(v, 10, 64)
		if ferr != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的文件夹参数"})
			return
		}
		if n > 0 {
			folderID = &n
		}
	}
	if folderID != nil && folderRequiresUnlock(c, *folderID) {
		respondFolderLocked(c)
		return
	}

	// v21.58 P1：导入与创建笔记一致受配额约束（max_notes_per_user，事务内判断）
	// v21.66 P1-5：n >= 0 与 note.go 对齐，0 = 不限（此前 n > 0 使 0 被误判为默认 1000）
	limit := 1000
	if v, err := model.GetConfig("max_notes_per_user"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			limit = n
		}
	}
	// P2 批次1（§129 门禁）：导入也是建笔记——委托 app Facade 同产 ChangeLog CREATE 事件
	// （否则导入的笔记永远进不了同步事件流，桌面端 pull 不可见）。
	// v2.2.1：folderID 透传（归属校验在 app 层，ErrInvalidFolder 同 note.go 口径）。
	note, err := app.NewServerNoteService(limit).CreateNote(userID.(int64), title, htmlContent, "", folderID)
	if err != nil {
		if errors.Is(err, model.ErrQuotaExceeded) {
			c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("已达到笔记数量上限（%d 篇），请先清理不需要的笔记", limit)})
			return
		}
		if errors.Is(err, model.ErrInvalidFolder) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "文件夹不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建笔记失败"})
		return
	}

	c.JSON(http.StatusCreated, note)
}

// getMaxImportKB 读取 max_note_content_kb 配置，0 表示不限制。
func getMaxImportKB() int {
	var val string
	if err := database.DB.QueryRow("SELECT value FROM configs WHERE key = 'max_note_content_kb'").Scan(&val); err != nil {
		logger.Error("getMaxImportKB: read config: %v", err) // v21.58 F-16
		return 0
	}
	var kb int
	fmt.Sscanf(val, "%d", &kb)
	return kb
}

// importSizeLimit 导入文件硬上限（v21.55）：跟随 max_note_content_kb 配置。
// 换算规则已下沉 notetext.SizeLimitForConfig（桌面离线导入共用同一上限，避免两端不一致）。
func importSizeLimit() int64 {
	return notetext.SizeLimitForConfig(getMaxImportKB())
}

// sanitizeHTML 对 HTML 做 bluemonday 消毒（UGCPolicy，供非笔记场景使用；笔记内容走
// sanitizeNoteContent 的专用白名单）。
var htmlSanitizer = bluemonday.UGCPolicy()

func sanitizeHTML(text string) string {
	return htmlSanitizer.Sanitize(text)
}

// ---- 以下为搬移后保留的同名薄封装（既有单测直接调用，保持零改动）----

// extractTitle 从文本中提取标题：首个 # 标题 → 文件名去扩展名。
func extractTitle(text string, filename string) string {
	return notetext.ExtractTitle(text, filename)
}

// txtToHTML 纯文本 → 每行一个 <p>，空行保留空段落（v2.2.1 逐行成段）。
func txtToHTML(text string) string {
	return notetext.TextToHTML(text)
}

// escapeHTML 最小 HTML 转义（& < >）。
func escapeHTML(s string) string {
	return notetext.EscapeHTML(s)
}

// mdToHTML 把 Markdown 渲染为 HTML（含 GFM 任务清单直构）。
func mdToHTML(text string) string {
	return notetext.MarkdownToHTML(text)
}
