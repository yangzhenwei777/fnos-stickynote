package main

import (
	"errors"
	"sort"
	"sync"
	"time"

	"stickynote/model"
)

// folder_unlock.go —— v2.3.0 桌面端文件夹访问锁。
//
// 与 Web 的签名令牌不同：桌面端的授权状态**只存在本进程内存**，关闭应用即锁；
// 页面刷新通过 ListFolderUnlockStatus 回填 UI。Go 侧是唯一权威，前端 sessionStorage
// 不参与桌面判定（即使 WebView2 残留旧存储也不会放行）。

var folderUnlockMu sync.Mutex
var folderUnlocked = map[int64]time.Time{}

func (l *LocalService) folderGrantSet() map[int64]struct{} {
	folderUnlockMu.Lock()
	defer folderUnlockMu.Unlock()
	out := make(map[int64]struct{}, len(folderUnlocked))
	for id := range folderUnlocked {
		out[id] = struct{}{}
	}
	return out
}

func (l *LocalService) clearFolderUnlocks() {
	folderUnlockMu.Lock()
	folderUnlocked = map[int64]time.Time{}
	folderUnlockMu.Unlock()
}

func (l *LocalService) forgetFolderUnlock(id int64) {
	folderUnlockMu.Lock()
	delete(folderUnlocked, id)
	folderUnlockMu.Unlock()
}

// ensureFolderUnlocked 写入/删除类操作的目标文件夹守卫（未解锁 → ErrFolderLocked）。
func (l *LocalService) ensureFolderUnlocked(folderID *int64) error {
	if folderID == nil || *folderID <= 0 {
		return nil
	}
	_, encrypted, _, _, err := model.GetFolderEncryptInfo(*folderID, l.userID)
	if err != nil {
		if errors.Is(err, model.ErrFolderNotFound) {
			return nil // 交给既有 ErrInvalidFolder 错误链
		}
		return err
	}
	if !encrypted {
		return nil
	}
	if _, ok := l.folderGrantSet()[*folderID]; !ok {
		return model.ErrFolderLocked
	}
	return nil
}

func (l *LocalService) EncryptFolder(id int64, password, confirmPassword string) (int64, error) {
	version, err := l.folders.Encrypt(id, l.userID, password, confirmPassword)
	if err != nil {
		return 0, err
	}
	l.forgetFolderUnlock(id)
	l.enqueueFolder(id, "UPDATE")
	return version, nil
}

func (l *LocalService) DecryptFolder(id int64, password string) (int64, error) {
	version, err := l.folders.Decrypt(id, l.userID, password)
	if err != nil {
		return 0, err
	}
	l.forgetFolderUnlock(id)
	l.enqueueFolder(id, "UPDATE")
	return version, nil
}

// UnlockFolder 校验密码并记录本进程解锁态。桌面不签发签名令牌（无跨进程需求）。
func (l *LocalService) UnlockFolder(id int64, password string) (map[string]any, error) {
	if _, _, err := l.folders.Unlock(id, l.userID, password); err != nil {
		return nil, err
	}
	folderUnlockMu.Lock()
	folderUnlocked[id] = time.Now()
	folderUnlockMu.Unlock()
	return map[string]any{"unlock_token": "", "expires_at": 0, "unlocked": true}, nil
}

// ListFolderUnlockStatus 页面刷新后回填 UI（按 id 升序稳定输出）。
func (l *LocalService) ListFolderUnlockStatus() []int64 {
	folderUnlockMu.Lock()
	ids := make([]int64, 0, len(folderUnlocked))
	for id := range folderUnlocked {
		ids = append(ids, id)
	}
	folderUnlockMu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
