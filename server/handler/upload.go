package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"stickynote/logger"
	"stickynote/model"
)

var uploadDir string

func SetUploadDir(dir string) {
	uploadDir = dir
}

// uploadLocks per-user 上传互斥锁（v21.58 F-06）：消除「检查配额→写文件→落库」
// 之间的并发竞态——两个并发上传同时读到剩余配额、双双放行会突破配额上限。
// v21.70 P3-01：改 sync.Map（免显式锁）+ 提供 releaseUserUploadLock 供删除用户时回收。
var uploadLocks sync.Map // map[int64]*sync.Mutex

func userUploadLock(uid int64) *sync.Mutex {
	v, _ := uploadLocks.LoadOrStore(uid, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// releaseUserUploadLock 删除用户后回收其上传锁（v21.70 P3-01），避免 map 项永久累积。
func releaseUserUploadLock(uid int64) {
	uploadLocks.Delete(uid)
}

func Upload(c *gin.Context) {
	maxMB := envInt("UPLOAD_MAX_MB", 30)
	file, err := c.FormFile("file")
	if err != nil {
		// v21.107 P1-13：区分「body 超网关上限被截断」与「未发文件」——
		// 此前两类混报「未选择文件」，大文件用户看到莫名其妙的文案。
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("文件过大：超过服务器接收上限（单文件最大 %dMB），请压缩后再上传", maxMB),
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "未选择文件"})
		return
	}

	// 大小上限（v18）：默认 30MB，UPLOAD_MAX_MB 可调（冒烟用小阈值验证，防磁盘被恶意占满）
	if file.Size > int64(maxMB)*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("文件超过大小限制（%dMB）", maxMB)})
		return
	}

	// 扩展名白名单（同时记录期望的 MIME，供下方交叉校验）
	ext := strings.ToLower(filepath.Ext(file.Filename))
	allowedExts := map[string]string{
		".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
		".gif": "image/gif", ".webp": "image/webp",
	}
	wantMIME, ok := allowedExts[ext]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 jpg、png、gif、webp 格式"})
		return
	}

	// magic number 校验（v18）：读文件头嗅探真实类型，与扩展名交叉匹配，
	// 防止伪装扩展名的可执行/脚本文件入库（nosniff 之外的纵深防御）。
	fh, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件读取失败"})
		return
	}
	// defer 兜底：未来在本函数内新增提前 return 也不会泄漏句柄（v18.3）
	defer fh.Close()
	head := make([]byte, 512)
	n, _ := fh.Read(head)
	if gotMIME := http.DetectContentType(head[:n]); gotMIME != wantMIME {
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件内容与图片格式不符，已拒绝"})
		return
	}

	// 生成唯一文件名
	filename := uuid.New().String() + ext
	savePath := filepath.Join(uploadDir, filename)

	// v21.58 F-06：per-user 互斥锁串行化「配额检查→写文件→落库」，
	// 消除两个并发上传同时读到剩余配额、双双放行突破配额的竞态。
	var unlock func()
	if userID, exists := c.Get("userID"); exists {
		mu := userUploadLock(userID.(int64))
		mu.Lock()
		unlock = mu.Unlock
	}
	if unlock != nil {
		defer unlock()
	}

	// 每用户存储总配额（v18.7）：configs.max_storage_mb_per_user（默认 1024MB，0=不限）。
	// v18.5 的 uploads 归属表让 SUM(size) 成为可能——单文件限大小，总量限配额。
	if userID, exists := c.Get("userID"); exists {
		limitMB := 1024
		if v, err := model.GetConfig("max_storage_mb_per_user"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
				limitMB = n
			}
		}
		if limitMB > 0 {
			if used, err := model.SumUploadSizeByUser(userID.(int64)); err == nil && used+file.Size > int64(limitMB)*1024*1024 {
				c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("存储空间不足：每用户上限 %dMB，请先在笔记中清理不再需要的图片", limitMB)})
				return
			}
		}
	}

	// P1 批次3（ADR-003 R2）：落盘即流式计算服务端权威 sha256（图片级成本毫秒）。
	// file.Open() 返回全新只读流（magic 嗅探消耗的 512B 不影响此处全量拷贝）。
	src2, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件读取失败"})
		return
	}
	defer src2.Close()
	dst, err := os.OpenFile(savePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件保存失败"})
		return
	}
	hasher := sha256.New()
	_, cpErr := io.Copy(io.MultiWriter(dst, hasher), src2)
	closeErr := dst.Close()
	if cpErr != nil || closeErr != nil {
		_ = os.Remove(savePath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件保存失败"})
		return
	}
	contentSHA := hex.EncodeToString(hasher.Sum(nil))

	// 落库归属记录（v18.5）：删用户时按表物理清理文件。
	// v21.58 F-08：落库失败必须回滚——删文件 + 返回 500，否则产生无归属孤儿文件。
	if userID, exists := c.Get("userID"); exists {
		if err := model.RecordUploadHashed(filename, userID.(int64), file.Size, contentSHA); err != nil {
			logger.Error("Upload: record upload failed for %s: %v", filename, err)
			_ = os.Remove(savePath)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "记录上传文件失败"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"url": "/uploads/" + filename,
	})
	logger.Info("Upload: saved as %s", filename)
}

// ServeUpload 受保护的文件访问（v20.7），替代旧 /uploads 公开静态路由。
func ServeUpload(c *gin.Context) {
	name := c.Param("name")
	// 防路径穿越：拒绝含路径分隔符的请求
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == "" || name == "." || name == ".." {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	path := filepath.Join(uploadDir, name)

	// 符号链接攻击防护（v20.8 审计修复）：拒绝符号链接文件，
	// 防止攻击者通过 symlink 读取 uploadDir 之外的系统文件。
	info, err := os.Lstat(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		logger.Info("ServeUpload: symlink refused for %s", name)
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// 二次确认真实路径仍在 uploadDir 内
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	realPath, _ = filepath.Abs(realPath)
	uploadAbs, _ := filepath.Abs(uploadDir)
	if !strings.HasPrefix(realPath, uploadAbs+string(filepath.Separator)) && realPath != uploadAbs {
		logger.Info("ServeUpload: path escape refused for %s (resolved: %s)", name, realPath)
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// 文件归属校验（v20.8 审计修复）：防止已登录用户通过 UUID 猜测访问他人上传文件。
	// 管理员可跨用户访问（便于排查），非本人且非管理员返回 404。
	// v21.67 P1-03：改为 fail-closed——owner==0（无 uploads 归属记录，可能为已删用户
	// 残留文件）也拒绝访问，杜绝「删用户后文件删除失败仍可被其他登录用户访问」。
	if userID, exists := c.Get("userID"); exists {
		uid := userID.(int64)
		owner, _ := model.GetUploadOwner(name)
		isAdmin := false
		if role, ok := c.Get("role"); ok && role.(string) == "admin" {
			isAdmin = true
		}
		if !isAdmin && (owner == 0 || owner != uid) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
	}

	c.File(path)
}
