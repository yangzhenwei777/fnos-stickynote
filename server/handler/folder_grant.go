package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"stickynote/auth"
	"stickynote/model"

	"github.com/gin-gonic/gin"
)

// folder_grant.go —— v2.3.0 文件夹访问锁的请求侧凭证处理。
//
// 客户端每次请求携带 X-Folder-Grants（逗号分隔的 HMAC 解锁令牌）。服务端只接受
// 同时满足「用户一致 + token_version 一致 + 文件夹仍锁定 + 密码哈希指纹一致」的令牌。
// 任何异常一律 fail-closed（视为未解锁），不向客户端解释原因。

const folderGrantHeader = "X-Folder-Grants"

const folderGrantContextKey = "folderGrantSet"

func folderGrants(c *gin.Context) map[int64]struct{} {
	if v, ok := c.Get(folderGrantContextKey); ok {
		if set, ok := v.(map[int64]struct{}); ok {
			return set
		}
	}
	set := map[int64]struct{}{}
	c.Set(folderGrantContextKey, set)

	raw := strings.TrimSpace(c.GetHeader(folderGrantHeader))
	if raw == "" {
		return set
	}
	uidAny, _ := c.Get("userID")
	uid, ok := uidAny.(int64)
	if !ok || uid <= 0 {
		return set
	}
	tvAny, _ := c.Get("tokenVersion")
	tv, _ := tvAny.(int64)

	parts := strings.Split(raw, ",")
	if len(parts) > 50 {
		parts = parts[:50]
	}
	type candidate struct {
		folderID    int64
		fingerprint string
	}
	cands := make([]candidate, 0, len(parts))
	ids := make([]int64, 0, len(parts))
	seen := map[int64]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		g, err := auth.ParseFolderGrant(p)
		if err != nil {
			continue // 过期/篡改/格式错：忽略该令牌
		}
		if g.UserID != uid || g.TokenVersion != tv {
			continue
		}
		if seen[g.FolderID] {
			continue
		}
		seen[g.FolderID] = true
		cands = append(cands, candidate{folderID: g.FolderID, fingerprint: g.Fingerprint})
		ids = append(ids, g.FolderID)
	}
	if len(cands) == 0 {
		return set
	}
	states, err := model.GetFolderGrantStates(uid, ids)
	if err != nil {
		return set // 查询失败：不授予任何解锁
	}
	for _, cd := range cands {
		st, ok := states[cd.folderID]
		if !ok || !st.IsEncrypted || st.Hash == "" {
			continue
		}
		if model.FolderFingerprint(st.Hash) != cd.fingerprint {
			continue // 密码已变更，旧令牌作废
		}
		set[cd.folderID] = struct{}{}
	}
	return set
}

// folderRequiresUnlock 写入/删除类操作的目标文件夹守卫。
// 文件夹不存在返回 false（交给既有 ErrInvalidFolder 错误链）；DB 异常 fail-closed。
func folderRequiresUnlock(c *gin.Context, folderID int64) bool {
	if folderID <= 0 {
		return false
	}
	uidAny, _ := c.Get("userID")
	uid, ok := uidAny.(int64)
	if !ok || uid <= 0 {
		return true
	}
	_, encrypted, _, _, err := model.GetFolderEncryptInfo(folderID, uid)
	if err != nil {
		if errors.Is(err, model.ErrFolderNotFound) {
			return false
		}
		return true
	}
	if !encrypted {
		return false
	}
	_, granted := folderGrants(c)[folderID]
	return !granted
}

func respondFolderLocked(c *gin.Context) {
	c.JSON(http.StatusLocked, gin.H{"error": "文件夹已加密，请先解锁", "code": "FOLDER_LOCKED"})
}

// issueFolderGrant 为已验证密码的文件夹签发本会话授权令牌。
func issueFolderGrant(c *gin.Context, folderID int64, hash string) (string, time.Time, error) {
	uidAny, _ := c.Get("userID")
	uid := uidAny.(int64)
	tvAny, _ := c.Get("tokenVersion")
	tv, _ := tvAny.(int64)
	return auth.SignFolderGrant(uid, folderID, tv, model.FolderFingerprint(hash))
}
