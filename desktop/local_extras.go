package main

// local_extras.go — P0-A 收官切片的补充绑定：真实 Vue 前端启动链路所需的最小端点集。
// 响应形状逐项对齐服务端 handler（LocalTransport adapter 只做同构转发，不做二次翻译），
// 未接线域（export blob / backup / admin / configs 管理 / TOTP）由 adapter 统一 501 明示，
// 不静默吞掉。LocalLogin 是开发档会话桩；正式离线解锁（DPAPI 设备令牌）按 v20 §99~101 属 P3。

import (
	"fmt"

	"stickynote/database"
	"stickynote/model"

	"stickynote-desktop/localauth"
)

// LocalLogin 本地单用户会话（P0-A 桩语义，返回值与 /auth/login 同形）。
//
// v21.140 修复：`username` 不再恒为桩值 "local"。桌面登录成功后前端会调 `/auth/login`
// 建立"本地前端会话"，而该端点在桌面被映射到这里 —— 原先硬编码 local 会把**远端账号名**
// 覆盖掉，于是侧边栏（AppSidebar/MainLayout 显示 auth.user.username）一直显示 local，
// 与用户实际登录的账号无关。现在改为：**已连远端（有设备档案）时用档案里的用户名**，
// 离线单机（无档案）时仍回落 "local"。
//
// role 恒为 "user"：桌面壳不提供管理后台面（admin 菜单不出现在桌面构建），
// 因此不需要也不应把远端角色透出到前端授权判定。
func (l *LocalService) LocalLogin() map[string]any {
	l.clearFolderUnlocks() // v2.3.0：新登录会话从锁定态开始
	username := "local"
	if p, err := localauth.Load(database.DB); err == nil && p.Username != "" {
		username = p.Username
	}
	return map[string]any{
		"access_token":  "local-desktop-session",
		"refresh_token": "local-desktop-session",
		"user": map[string]any{
			"id": 1, "username": username, "role": "user", "status": "active",
		},
	}
}

// SetupStatus 桌面端恒已初始化（本地库自带 seed 用户）。
func (l *LocalService) SetupStatus() map[string]any { return map[string]any{"needs_setup": false} }

// ListTags 与 GET /notes/tags 同源（adapter 包 {tags}）。
func (l *LocalService) ListTags() ([]string, error) {
	return dbReadGuard(func() ([]string, error) { return model.GetAllTags(l.userID, l.folderGrantSet()) })
}

// GetPublicConfig 与 GET /public-config 同源（model.GetPublicConfigs 键集一致）。
func (l *LocalService) GetPublicConfig() (map[string]string, error) {
	return dbReadGuard(func() (map[string]string, error) {
		cfg, err := model.GetPublicConfigs()
		if err != nil {
			return nil, err
		}
		// 与 handler/config.go 同形：键未落库时补默认 0（永久保留），前端无需再猜
		if _, ok := cfg["trash_retention_days"]; !ok {
			cfg["trash_retention_days"] = "0"
		}
		return cfg, nil
	})
}

// GetQuota 复刻 handler.GetQuota 的键与默认（notes_limit/storage_limit_mb 0=不限如实返回）。
// P1-1：整段为只读组合查询，外包统一超时护栏。
func (l *LocalService) GetQuota() (map[string]any, error) {
	return dbReadGuard(func() (map[string]any, error) {
		notesCount, err := model.CountActiveNotes(l.userID)
		if err != nil {
			return nil, err
		}
		notesLimit := 1000
		if v, err := model.GetConfig("max_notes_per_user"); err == nil {
			if n, ok := atoiAllow0(v); ok {
				notesLimit = n
			}
		}
		storageBytes, err := model.SumUploadSizeByUser(l.userID)
		if err != nil {
			return nil, err
		}
		storageLimitMB := 1024
		if v, err := model.GetConfig("max_storage_mb_per_user"); err == nil {
			if n, ok := atoiAllow0(v); ok {
				storageLimitMB = n
			}
		}
		return map[string]any{
			"notes_count": notesCount, "notes_limit": notesLimit,
			"storage_bytes": storageBytes, "storage_limit_mb": storageLimitMB,
		}, nil
	})
}

func atoiAllow0(s string) (int, bool) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// ---- 回收站（形状逐项对齐 handler/note_trash.go） ----

func (l *LocalService) ListTrash(page, pageSize int) (*model.NoteListResponse, error) {
	return dbReadGuard(func() (*model.NoteListResponse, error) {
		return model.ListTrashedNotes(l.userID, page, pageSize, l.folderGrantSet())
	})
}

func (l *LocalService) TrashStats() (map[string]any, error) {
	return dbReadGuard(func() (map[string]any, error) {
		count, uploadBytes, err := model.GetTrashStats(l.userID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": count, "upload_bytes": uploadBytes}, nil
	})
}

func (l *LocalService) RestoreNote(id int64) (map[string]any, error) {
	if err := model.RestoreNote(id, l.userID); err != nil {
		return nil, err
	}
	l.enqueueNote(id, "RESTORE")
	folder := "我的笔记" // v21.107 P2-9：恢复去向（原夹已删时模型层已置 NULL=默认）
	if name, ferr := model.GetNoteFolderName(id); ferr == nil && name != "" {
		folder = name
	}
	// v2.3.0：与 handler/note_trash.go 同形——恢复到锁定文件夹时提示需解锁查看（A-15 契约）
	locked, _ := model.FolderLockedForNote(id, l.userID, l.folderGrantSet())
	return map[string]any{"message": "已恢复", "folder": folder, "folder_locked": locked}, nil
}

func (l *LocalService) PermanentDeleteNote(id int64) (map[string]any, error) {
	// 与服务端同纪律：附件清单读取失败即停止删除（v21.69 P1-01 fail-closed）。
	filenames, err := database.ListNoteUploads(id)
	if err != nil {
		return nil, err
	}
	// E8（2026-09-23 用户实测缺口补全）：彻底删必须入队 PURGE 推给服务端——此前只做本地
	// 物理删（服务端回收站里仍留着）。**必须在物理删之前入队**：EnqueueNoteOp 构造 payload
	// 需要 notes 行在。服务端侧 opPurge 有「活行拒收」护栏，入队后本地删失败也不会误删服务端数据。
	l.enqueueNote(id, "PURGE")
	if err := model.PermanentDeleteNote(id, l.userID); err != nil {
		return nil, err
	}
	if len(filenames) > 0 {
		database.DeleteOrphanUploadFiles(l.uploadDir, filenames, l.userID)
	}
	wakeScheduler() // 变更入队后立刻触发同步（与 DeleteNote/MoveNote 同纪律）
	return map[string]any{"message": "已彻底删除"}, nil
}

func (l *LocalService) EmptyTrash() (map[string]any, error) {
	// E8：清空前逐条入队 PURGE（先收集 ids——payload 构造需要行在，且清空后不可再查）。
	var ids []int64
	rows, err := database.DB.Query(`SELECT id FROM notes WHERE user_id = ? AND deleted_at IS NOT NULL`, l.userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if serr := rows.Scan(&id); serr == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		l.enqueueNote(id, "PURGE")
	}
	notes, uploadNames, err := model.EmptyTrashNotes(l.userID)
	if err != nil {
		return nil, err
	}
	if len(uploadNames) > 0 {
		database.DeleteOrphanUploadFiles(l.uploadDir, uploadNames, l.userID)
	}
	if len(ids) > 0 {
		wakeScheduler()
	}
	return map[string]any{"message": "回收站已清空", "deleted": len(notes)}, nil
}

// ---- 批量 / 复制（对齐 handler 响应键） ----

func (l *LocalService) BatchDelete(ids []int64) map[string]any {
	// P2 批次1：单事务批量软删（桌面本地模式：不产事件不 bump，语义与旧逐行循环一致，
	// 但消除部分删除中间态）。
	deleted, err := l.notes.BatchDeleteNotes(l.userID, ids)
	if err != nil {
		return map[string]any{"message": "批量删除失败", "deleted": 0, "total": len(ids)}
	}
	return map[string]any{
		"message": fmt.Sprintf("已删除 %d 篇笔记", deleted),
		"deleted": deleted, "total": len(ids),
	}
}

func (l *LocalService) CopyNote(id int64, folderID *int64) (*model.Note, error) {
	if err := l.ensureFolderUnlocked(folderID); err != nil {
		return nil, err
	}
	return model.CopyNoteToFolder(id, l.userID, folderID, 100000)
}
