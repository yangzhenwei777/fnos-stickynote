package database

import (
	"os"
	"path/filepath"
	"time"

	"stickynote/logger"
)

// 无归属旧文件的宽限期：修改时间距今小于该值的文件不删，
// 防止误删"刚上传、尚未保存进笔记"的在途文件。
const orphanGracePeriod = 24 * time.Hour

// BackfillUploads（v18.5，一次性，configs.uploads_backfilled 旗标）：
// 存量上传文件归属回填 + 无归属旧文件激进清理。
//
// 背景：v18.5 之前上传文件只有 UUID 文件名，没有任何归属记录，
// 删除用户后文件成为永久孤儿。本函数在升级后首次启动时：
//  1. 扫描全部笔记（含回收站——回收站笔记可能恢复，其引用文件仍需保留），
//     把 content 中引用的 /uploads/<file> 按笔记属主回填进 uploads 表；
//  2. 目录中无归属（无任何笔记引用、表中无记录）且超过 24h 宽限期的文件
//     物理删除（用户已确认的激进清理策略）；
//  3. 写入旗标，后续启动不再重复扫描。
//
// 本函数只覆盖历史存量；v18.5 起新上传在 Upload handler 中实时落库。
func BackfillUploads(uploadDir string) error {
	uploadsHas, err := tableExists("uploads")
	if err != nil || !uploadsHas {
		return err
	}

	var flag string
	err = DB.QueryRow("SELECT value FROM configs WHERE key = 'uploads_backfilled'").Scan(&flag)
	if err == nil && flag == "true" {
		return nil // 已执行过
	}

	// 1. 扫描全部笔记 content，建立 文件名 -> 属主 user_id 映射
	rows, err := DB.Query("SELECT user_id, content FROM notes")
	if err != nil {
		return err
	}
	referenced := make(map[string]int64)
	for rows.Next() {
		var uid int64
		var content string
		if err := rows.Scan(&uid, &content); err != nil {
			rows.Close()
			return err
		}
		for _, m := range uploadRefRe.FindAllStringSubmatch(content, -1) {
			referenced[m[1]] = uid
		}
	}
	rows.Close()

	// 2. 回填归属（INSERT OR IGNORE：已落库的记录不动）
	now := time.Now()
	backfilled := 0
	backfillFailed := 0
	for name, uid := range referenced {
		var size int64
		createdAt := now
		if st, err := os.Stat(filepath.Join(uploadDir, name)); err == nil {
			size = st.Size()
			createdAt = st.ModTime()
		}
		res, err := DB.Exec(
			"INSERT OR IGNORE INTO uploads (filename, user_id, size, created_at, status) VALUES (?, ?, ?, ?, 'attached')",
			name, uid, size, createdAt,
		)
		if err != nil {
			return err
		}
		// v21.67 P1-02：已存在记录也标记 attached（被 content 引用），避免被 staged GC 误删。
		// v21.69 P1-03：标记失败累积计数，最终不写 completed（下次启动重试）。
		if _, uerr := DB.Exec("UPDATE uploads SET status = 'attached' WHERE filename = ? AND user_id = ?", name, uid); uerr != nil {
			logger.Error("BackfillUploads: mark attached %s failed: %v", name, uerr)
			backfillFailed++
		}
		if n, _ := res.RowsAffected(); n > 0 {
			backfilled++
		}
	}

	// 3. 激进清理：无归属且过宽限期的文件物理删除
	removed := 0
	cleanupFailed := 0
	entries, err := os.ReadDir(uploadDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if _, ok := referenced[name]; ok {
			continue // 有笔记引用，保留
		}
		var tracked int
		if err := DB.QueryRow("SELECT COUNT(*) FROM uploads WHERE filename = ?", name).Scan(&tracked); err != nil {
			// v21.69 P1-02：归属查询失败即不删除（fail-closed），状态不确定宁可漏清理也不误删
			logger.Error("BackfillUploads: tracking query failed for %s: %v (skip)", name, err)
			cleanupFailed++
			continue
		}
		if tracked > 0 {
			continue // 表中已有归属记录，保留
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) < orphanGracePeriod {
			continue // 宽限期内（可能是刚上传的在途文件），保留
		}
		path := filepath.Join(uploadDir, name)
		if err := os.Remove(path); err != nil {
			logger.Error("uploads 孤儿清理失败: %s: %v", path, err)
			cleanupFailed++
			continue
		}
		removed++
		logger.Info("uploads 孤儿清理: 删除无归属旧文件 %s", name)
	}

	// 4. 旗标：v21.68 P1-03 + v21.69 P1-03——回填标记或清理任一失败都写 partial（下次启动重试），全部成功才写 true
	flagValue := "true"
	desc := "uploads 存量回填与孤儿清理已完成（v18.5）"
	if backfillFailed > 0 || cleanupFailed > 0 {
		flagValue = "partial"
		desc = "uploads 存量处理部分失败，待下次启动重试"
		logger.Error("uploads 存量处理：回填标记失败 %d 个、孤儿清理失败 %d 个，标记 partial 待下次重试", backfillFailed, cleanupFailed)
	}
	if _, err := DB.Exec(
		"INSERT INTO configs (key, value, description) VALUES ('uploads_backfilled', ?, ?) ON CONFLICT(key) DO UPDATE SET value = ?",
		flagValue, desc, flagValue,
	); err != nil {
		return err
	}
	logger.Info("uploads 存量处理完成: 回填 %d 条归属（失败 %d），清理无归属旧文件 %d 个（失败 %d）", backfilled, backfillFailed, removed, cleanupFailed)
	return nil
}
