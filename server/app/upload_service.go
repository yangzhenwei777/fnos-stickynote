package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"stickynote/model"

	"github.com/google/uuid"
)

// upload_service.go — 附件落盘门面（P0-A 接线切片）。校验链与服务端 handler/upload.go
// 逐项对齐（SOURCE-FACT §6）：扩展名白名单 → magic number 交叉嗅探 → 单文件大小帽 →
// 写盘（0600，UUID 受控文件名）→ RecordUpload(staged) 归属落库，落库失败回滚删文件（v21.58 F-08）。
//
// 与 Web 的差异仅在输入形态：binding 侧传 bytes（前端 LocalTransport 读文件后 base64/ArrayBuffer），
// 无 multipart；per-user 互斥锁与总量配额是服务端多用户关注点，桌面单机由文件系统权限兜底，
// 同步上推时再走服务端全套校验（§65）。

var ErrFileTypeRejected = errors.New("file type rejected")

// allowedExts 与 handler/upload.go 同一张表（改一处两处须同步——P2 收口后仅存此处）。
var allowedExts = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp",
}

type UploadService struct {
	Dir       string // 附件目录（桌面 = profile uploads/；服务端注入 UPLOAD_DIR）
	MaxFileMB int    // 默认对齐 UPLOAD_MAX_MB=30
}

func NewUploadService(dir string) *UploadService {
	return &UploadService{Dir: dir, MaxFileMB: 30}
}

// SaveBytes 校验并落盘，返回受控文件名（uuid.ext）与前端引用路径（/uploads/<name>）。
func (u *UploadService) SaveBytes(userID int64, originalName string, data []byte) (filename, url string, err error) {
	if u.MaxFileMB > 0 && int64(len(data)) > int64(u.MaxFileMB)*1024*1024 {
		return "", "", fmt.Errorf("文件超过大小限制（%dMB）", u.MaxFileMB)
	}
	ext := strings.ToLower(filepath.Ext(originalName))
	wantMIME, ok := allowedExts[ext]
	if !ok {
		return "", "", ErrFileTypeRejected
	}
	n := len(data)
	if n > 512 {
		n = 512
	}
	if got := http.DetectContentType(data[:n]); got != wantMIME {
		return "", "", ErrFileTypeRejected // 内容与扩展名不符（伪装文件纵深防御，同 Web）
	}
	filename = uuid.New().String() + ext
	if err := os.MkdirAll(u.Dir, 0700); err != nil {
		return "", "", err
	}
	path := filepath.Join(u.Dir, filename)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", "", err
	}
	// 归属落库；失败回滚文件，绝不留无主孤儿（v21.58 F-08 语义）。
	// P1 批次3（ADR-003 R2）：字节路径直接全量哈希后落库。
	sha := hex.EncodeToString(func() []byte { h := sha256.Sum256(data); return h[:] }())
	if userID > 0 {
		if err := model.RecordUploadHashed(filename, userID, int64(len(data)), sha); err != nil {
			_ = os.Remove(path)
			return "", "", err
		}
	}
	return filename, "/uploads/" + filename, nil
}

// ReadStored 供 LocalTransport/预览读取：拒绝分隔符与符号链接（ServeUpload 同源守卫）。
func (u *UploadService) ReadStored(name string) ([]byte, string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return nil, "", errors.New("invalid name")
	}
	path := filepath.Join(u.Dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.New("refuse symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	ext := strings.ToLower(filepath.Ext(name))
	mime, ok := allowedExts[ext]
	if !ok {
		mime = "application/octet-stream"
	}
	return data, mime, nil
}
