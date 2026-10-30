package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// foldergrant.go —— v2.3.0 文件夹访问锁的会话授权令牌。
//
// 设计要点：
//   - 无服务端状态：HMAC 签名，重启不掉线；令牌绑定 user.token_version，
//     登出/改密 BumpTokenVersion 后立即失效；
//   - 绑定文件夹密码哈希指纹：解密后重新加密（改密）会让旧令牌自动失效；
//   - 30 天 exp 只是被盗令牌的兜底；正常「关浏览器/关应用即锁」由前端
//     sessionStorage / 桌面进程内存保证。

const (
	FolderGrantTTL = 30 * 24 * time.Hour
	folderGrantVer = 1
)

type FolderGrant struct {
	Version      int    `json:"v"`
	UserID       int64  `json:"uid"`
	FolderID     int64  `json:"fid"`
	TokenVersion int64  `json:"tv"`
	Fingerprint  string `json:"fp"`
	ExpiresAt    int64  `json:"exp"`
}

func folderGrantKey() []byte {
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte("stickynote-folder-grant-v1"))
	return mac.Sum(nil)
}

// SignFolderGrant 签发单个文件夹的解锁令牌。
func SignFolderGrant(userID, folderID, tokenVersion int64, fingerprint string) (string, time.Time, error) {
	exp := time.Now().Add(FolderGrantTTL)
	g := FolderGrant{
		Version:      folderGrantVer,
		UserID:       userID,
		FolderID:     folderID,
		TokenVersion: tokenVersion,
		Fingerprint:  fingerprint,
		ExpiresAt:    exp.Unix(),
	}
	payload, err := json.Marshal(g)
	if err != nil {
		return "", time.Time{}, err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, folderGrantKey())
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, exp, nil
}

// ParseFolderGrant 校验签名/格式/有效期，返回令牌声明（不含 DB 指纹比对）。
func ParseFolderGrant(token string) (*FolderGrant, error) {
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			body, sig := token[:i], token[i+1:]
			rawSig, err := base64.RawURLEncoding.DecodeString(sig)
			if err != nil {
				return nil, err
			}
			mac := hmac.New(sha256.New, folderGrantKey())
			mac.Write([]byte(body))
			if !hmac.Equal(rawSig, mac.Sum(nil)) {
				return nil, errors.New("folder grant signature invalid")
			}
			raw, err := base64.RawURLEncoding.DecodeString(body)
			if err != nil {
				return nil, err
			}
			var g FolderGrant
			if err := json.Unmarshal(raw, &g); err != nil {
				return nil, err
			}
			if g.Version != folderGrantVer || g.FolderID <= 0 || g.UserID <= 0 {
				return nil, errors.New("folder grant invalid")
			}
			if time.Now().Unix() > g.ExpiresAt {
				return nil, errors.New("folder grant expired")
			}
			return &g, nil
		}
	}
	return nil, errors.New("folder grant malformed")
}
