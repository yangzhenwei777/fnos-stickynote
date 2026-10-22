package handler

import (
	"archive/zip"
	"fmt"
	"net/http"
	"net/url"
	"stickynote/crypto"
	"stickynote/htmltext"
	"stickynote/model"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// note_export.go — 导出相关 handler（v21.54 P2：从 note.go 拆分，纯移动不改逻辑）

func ExportNote(c *gin.Context) {
	userID, _ := c.Get("userID")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的笔记ID"})
		return
	}
	note, err := model.GetNote(id, userID.(int64))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "笔记不存在"})
		return
	}

	var content string
	if note.IsEncrypted {
		// v21.21（审计三轮）：废弃 GET query 密码通道——密码入 URL 会进访问日志/浏览器历史。
		// 加密笔记导出仅支持 POST body 密码；GET 直接 400 提示。
		if c.Request.Method != "POST" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "加密笔记导出请使用 POST 请求，密码勿放 URL"})
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		password := ""
		if err := c.ShouldBindJSON(&body); err == nil && body.Password != "" {
			password = body.Password
		}
		if password == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "加密笔记导出需要密码"})
			return
		}
		pwHash, ciphertext, err := model.GetEncryptedContent(id, userID.(int64))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取加密信息失败"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(pwHash), []byte(password)); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "密码错误"})
			return
		}
		plaintext, err := crypto.Decrypt(ciphertext, password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "解密失败"})
			return
		}
		content = plaintext
	} else {
		content = note.Content
	}

	// 为加密笔记构建临时的 MD 内容（不解密不导出）
	tmp := *note
	tmp.Content = content
	filename := sanitizeExportFilename(tmp.Title) + ".md"
	setDownloadHeaders(c, filename, "text/markdown; charset=utf-8")
	c.String(http.StatusOK, buildNoteMD(&tmp))
}

// ExportAllNotes 导出当前用户全部未加密笔记，打包 zip（v20.0 起跳过加密笔记）。
// 回收站中的笔记不导出。v19.3 起流式写入响应体，不再全量缓冲到内存。

func ExportAllNotes(c *gin.Context) {
	userID, _ := c.Get("userID")
	notes, err := model.ListAllNotesForExport(userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取笔记列表失败"})
		return
	}

	// v20.0：统计跳过的加密笔记数
	var skipped int
	model.CountEncryptedNotes(userID.(int64), &skipped)

	filename := "便签导出-" + time.Now().Format("20060102-150405") + ".zip"
	setDownloadHeaders(c, filename, "application/zip")
	if skipped > 0 {
		c.Header("X-Encrypted-Skipped", strconv.Itoa(skipped))
	}

	zw := zip.NewWriter(c.Writer)
	defer zw.Close()

	used := make(map[string]bool)
	for i := range notes {
		entry := dedupeExportName(used, sanitizeExportFilename(notes[i].Title)) + ".md"
		w, err := zw.Create(entry)
		if err != nil {
			return // 头已发出，无法改报 JSON 错误——中断连接
		}
		if _, err := w.Write([]byte(buildNoteMD(&notes[i]))); err != nil {
			return
		}
	}
}

// buildNoteMD 生成单篇笔记的 Markdown 内容：标题 + 元信息 + 正文。
// 不加 YAML frontmatter（用户确认），纯 MD 正文互操作最佳。

func buildNoteMD(n *model.Note) string {
	var sb strings.Builder
	sb.WriteString("# " + n.Title + "\n\n")
	if strings.TrimSpace(n.Tags) != "" {
		sb.WriteString("**标签：** " + n.Tags + "\n\n")
	} else {
		sb.WriteString("\n")
	}
	sb.WriteString(htmltext.ToMarkdown(n.Content))
	sb.WriteString("\n")
	return sb.String()
}

// sanitizeExportFilename 清洗文件名：替换 Windows/zip 非法字符，去首尾空格和点，
// 空名回退「未命名」，最长 80 字（为扩展名留余量）。

func sanitizeExportFilename(title string) string {
	name := strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 32 {
			return -1
		}
		return r
	}, title)
	name = strings.TrimSpace(name)
	name = strings.TrimRight(name, ". ")
	if name == "" {
		name = "未命名"
	}
	if utf8.RuneCountInString(name) > 80 {
		name = string([]rune(name)[:80])
	}
	return name
}

// dedupeExportName zip 内重名加序号：标题.txt、标题(2).txt、标题(3).txt……
// 大小写不敏感（Windows 解压目标文件系统不区分大小写）。

func dedupeExportName(used map[string]bool, base string) string {
	name := base
	for i := 2; ; i++ {
		key := strings.ToLower(name)
		if !used[key] {
			used[key] = true
			return name
		}
		name = fmt.Sprintf("%s(%d)", base, i)
	}
}

// setDownloadHeaders 设置下载响应头。中文文件名走 RFC 5987 filename*（UTF-8 百分号编码），
// 另给一个 ASCII 兜底文件名兼容老客户端。

func setDownloadHeaders(c *gin.Context, filename, contentType string) {
	fallback := strings.Map(func(r rune) rune {
		if r < 128 && r != '"' && r != '\\' {
			return r
		}
		return -1
	}, filename)
	if fallback == "" || strings.HasPrefix(fallback, ".") {
		fallback = "download" + fallback
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", fallback, url.PathEscape(filename)))
}

// ---- v20.0 笔记加密 ----

type encryptRequest struct {
	Password        string `json:"password" binding:"required"`
	ConfirmPassword string `json:"confirm_password" binding:"required"`
}

type decryptRequest struct {
	Password string `json:"password" binding:"required"`
}

type verifyEncryptRequest struct {
	Password string `json:"password" binding:"required"`
}

// encryptGuard 笔记加密验证防爆破（v20.0）：维度 (笔记ID, IP)，独立于登录 guard。
var encryptGuard = &loginGuard{fails: make(map[string]*failRecord)}

// EncryptNote 为笔记设置加密密码（v20.0）。
// PUT /api/notes/:id/encrypt

func BatchExportNotes(c *gin.Context) {
	userID, _ := c.Get("userID")
	var req batchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供要导出的笔记 ID 列表"})
		return
	}

	notes, err := model.ListNotesByIDs(req.IDs, userID.(int64))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取笔记失败"})
		return
	}

	if len(notes) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "没有找到可导出的笔记"})
		return
	}

	filename := "便签批量导出-" + time.Now().Format("20060102-150405") + ".zip"
	setDownloadHeaders(c, filename, "application/zip")

	// v21.58 F-12：先统计跳过的加密笔记数并写 header，供前端准确提示实际导出数量
	skipped := 0
	for _, n := range notes {
		if n.IsEncrypted {
			skipped++
		}
	}
	if skipped > 0 {
		c.Header("X-Encrypted-Skipped", strconv.Itoa(skipped))
		c.Header("X-Exported-Count", strconv.Itoa(len(notes)-skipped))
	}

	zw := zip.NewWriter(c.Writer)
	defer zw.Close()

	used := make(map[string]bool)
	for _, n := range notes {
		if n.IsEncrypted {
			continue // 加密笔记跳过，不解密导出
		}
		entry := dedupeExportName(used, sanitizeExportFilename(n.Title)) + ".md"
		w, err := zw.Create(entry)
		if err != nil {
			return
		}
		if _, err := w.Write([]byte(buildNoteMD(&n))); err != nil {
			return
		}
	}
}
