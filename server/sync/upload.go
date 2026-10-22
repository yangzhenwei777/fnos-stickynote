package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"stickynote/logger"
	"stickynote/model"
	"stickynote/syncspec"
)

// upload.go —— POST /api/sync/upload（P1 批次3，ADR-003 R2/R5 + ADR-004 R3 通道面）。
//
// 桌面附件推送流第一跳：文件先于笔记 op 到达（staged），push 的 attachment_refs 校验据此通过；
// 断网超宽限被 GC → push 得 ATTACHMENT_NOT_FOUND → 客户端重传同文件同 op_id 重试（R3）。
// 与 Web /api/upload 同套校验（扩展名白名单/magic 交叉/大小帽/用户配额），差异只在：
//  1. 文件名由客户端申报（服务端生成会破坏 §105"本地附件保持 UUID 不 CAS 化"的镜像纪律），
//     但强校验 UUID 形态 + 图片白名单 + 归属冲突判定；
//  2. 落盘流式计算**服务端权威 sha256**；客户端申报值仅作比对（不符 → UPLOAD_HASH_MISMATCH
//     拒收并删临时文件），入库永远以实算为准（§65"服务器再次验证"姿态）。
//
// R5 幂等（filename 已存在）：size 不等 → IDENTITY_CONFLICT（不删既有文件）；
// 存储 sha 非空 ≠ 申报 → IDENTITY_CONFLICT；存储 sha='' → 服务端实算为准，等申报 → ACK。

var attachmentsDir string

// SetAttachmentsDir 由 cmd/server 注入上传目录（未注入时端点 503，防误路由裸写进程 CWD）。
func SetAttachmentsDir(dir string) { attachmentsDir = dir }

var syncUploadLocks sync.Map // per-user 串行化（sync 通道内部竞态；与 Web 通道跨路竞态由 INSERT OR IGNORE + 幂等重查兜底）

var syncFilenameRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}\.[a-zA-Z0-9]{1,10}$`)

var syncAllowedExts = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp",
}

func syncUserLock(uid int64) *sync.Mutex {
	v, _ := syncUploadLocks.LoadOrStore(uid, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func SyncUpload(c *gin.Context) {
	uidv, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}
	uid := uidv.(int64)
	if attachmentsDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "attachments dir not configured"})
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未选择文件", "code": syncspec.CodeBatchProtocol})
		return
	}
	name := strings.TrimSpace(c.PostForm("filename"))
	declared := strings.TrimSpace(strings.ToLower(c.PostForm("sha256")))
	if !syncFilenameRe.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "filename must be uuid.ext", "code": syncspec.CodeBatchProtocol})
		return
	}
	if declared != "" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(declared) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sha256 declaration malformed", "code": syncspec.CodeBatchProtocol})
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	wantMIME, ok := syncAllowedExts[ext]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 jpg、png、gif、webp 格式", "code": syncspec.CodeBatchProtocol})
		return
	}
	maxMB := 30
	if v := os.Getenv("UPLOAD_MAX_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxMB = n
		}
	}
	if file.Size > int64(maxMB)*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("文件超过大小限制（%dMB）", maxMB), "code": syncspec.CodeBatchProtocol})
		return
	}

	mu := syncUserLock(uid)
	mu.Lock()
	defer mu.Unlock()

	// R5 既有行判定（先于写盘，冲突零副作用）。
	existSize, existSHA, owner, exists, err := model.GetUploadMeta(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取归属失败", "code": "INTERNAL"})
		return
	}
	if exists {
		switch {
		case owner != uid:
			c.JSON(http.StatusConflict, gin.H{"error": "filename owned by another identity", "code": syncspec.CodeUploadIdentityConflict})
			return
		case existSize != file.Size:
			c.JSON(http.StatusConflict, gin.H{"error": "same filename different size", "code": syncspec.CodeUploadIdentityConflict})
			return
		case existSHA != "" && declared != "" && existSHA != declared:
			c.JSON(http.StatusConflict, gin.H{"error": "same filename different content", "code": syncspec.CodeUploadIdentityConflict})
			return
		case existSHA != "":
			// 已知权威内容：幂等 ACK（不重写盘）。申报缺省也放行（服务端哈希为准）。
			c.JSON(http.StatusOK, gin.H{"filename": name, "url": "/uploads/" + name, "sha256": existSHA, "size": existSize, "status": "ok"})
			return
		}
		// existSHA == ''：历史行未回填 → 以本次实算为准继续处理（写盘覆盖同内容文件安全：
		// size 已判等，内容以哈希复核兜底）。
	}

	// 配额（与 Web 同口径：max_storage_mb_per_user，0=不限；既有同名行不重复计）。
	if !exists {
		limitMB := 1024
		if v, err := model.GetConfig("max_storage_mb_per_user"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
				limitMB = n
			}
		}
		if limitMB > 0 {
			if used, err := model.SumUploadSizeByUser(uid); err == nil && used+file.Size > int64(limitMB)*1024*1024 {
				c.JSON(http.StatusForbidden, gin.H{"error": "存储空间不足", "code": "QUOTA_EXCEEDED"})
				return
			}
		}
	}

	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件读取失败", "code": "INTERNAL"})
		return
	}
	defer src.Close()
	head := make([]byte, 512)
	n, _ := src.Read(head)
	if got := http.DetectContentType(head[:n]); got != wantMIME {
		c.JSON(http.StatusBadRequest, gin.H{"error": "文件内容与图片格式不符，已拒绝", "code": syncspec.CodeBatchProtocol})
		return
	}
	// magic 嗅探消费了前 512B，重开全量流做哈希落盘。
	src2, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件读取失败", "code": "INTERNAL"})
		return
	}
	defer src2.Close()

	savePath := filepath.Join(attachmentsDir, name)
	tmpPath := savePath + ".sync-tmp"
	dst, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件保存失败", "code": "INTERNAL"})
		return
	}
	hasher := sha256.New()
	_, cpErr := io.Copy(io.MultiWriter(dst, hasher), src2)
	clErr := dst.Close()
	if cpErr != nil || clErr != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件保存失败", "code": "INTERNAL"})
		return
	}
	computed := hex.EncodeToString(hasher.Sum(nil))

	// R2 申报比对：不符拒收并删临时文件（服务端哈希为准，申报只作传输校验）。
	if declared != "" && declared != computed {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusBadRequest, gin.H{"error": "declared sha256 mismatch", "code": syncspec.CodeUploadHashMismatch, "server_sha256": computed})
		return
	}

	if err := os.Rename(tmpPath, savePath); err != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件保存失败", "code": "INTERNAL"})
		return
	}
	if !exists {
		if err := model.RecordUploadHashed(name, uid, file.Size, computed); err != nil {
			_ = os.Remove(savePath)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "记录上传文件失败", "code": "INTERNAL"})
			return
		}
		// 附件事件面（§129 延伸）：新文件到达**不产事件**——refs 随笔记 op 携带，
		// 附件本体不属于实体事件流（v20 §63/§67 姿态）。
	} else {
		// 历史 '' 行以实算收敛（R5 竞态回填）。
		if err := model.SetUploadHashForce(name, computed); err != nil {
			logger.Error("sync upload sha persist failed %s: %v", name, err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"filename": name, "url": "/uploads/" + name, "sha256": computed, "size": file.Size, "status": "staged"})
}

// checkAttachmentRefs push 校验（ADR-004 R3）：payload refs 必须已在服务端存在且归属本人。
// 返回缺失清单；非空时 op 以 ATTACHMENT_NOT_FOUND 拒绝且**不登记 op**（同 op_id 可在
// 客户端重传后原样重试——op_id 幂等语义不被结构性失败消费，R3 自恢复路径的前提）。
func checkAttachmentRefs(uid int64, refs []syncspec.AttachmentRef) []string {
	var missing []string
	for _, r := range refs {
		if r.Filename == "" {
			continue
		}
		if _, _, _, exists, err := model.GetUploadMeta(r.Filename); err != nil || !exists {
			missing = append(missing, r.Filename)
		} else if owner, _ := model.GetUploadOwner(r.Filename); owner != uid {
			missing = append(missing, r.Filename) // 归属他人视同缺失（不泄露存在性）
		}
	}
	return missing
}

func attachmentNotFound(missing []string) string {
	b, _ := json.Marshal(map[string]any{"missing": missing})
	return string(b)
}
