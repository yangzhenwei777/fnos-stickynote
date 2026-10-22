package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"stickynote/database"
)

// SyncProtocolVersion 离线同步协议版本（R0 定版=1，v20 §155/§156）。
// 与 App 版本、Schema 版本、备份格式版本彻底分层：协议 breaking 时此处 ++，不是 App Version++。
const SyncProtocolVersion = 1

// GetServerEpoch 读取服务端数据集代际。
//
// 表/行不存在（理论上仅出现在迁移未跑的裸库读取路径）→ 返回 (0, nil)：
// 语义即"legacy，无代际记录"，与归档 manifest 缺 server_epoch 字段（零值 0）同一口径，
// 由 NextRestoreEpoch 的 max() 保证单调不减。查询级错误原样上抛（恢复路径须 fail-closed）。
func GetServerEpoch() (int64, error) {
	// 锁读句柄 + nil 守卫：异步预检（restore goroutine 首次触库）在关停/测试时序下
	// 裸用全局 DB 会 nil panic 并触发跨用例回滚风暴（v1.0.175 P0-A CI 观察）→ 转 fail-closed 错误。
	db := database.GetDB()
	if db == nil {
		return 0, errors.New("database not ready")
	}
	var epoch int64
	err := db.QueryRow("SELECT server_epoch FROM sync_meta WHERE id = 1").Scan(&epoch)
	if err == nil {
		return epoch, nil
	}
	// legacy 口径（返回 0 不算失败）：行缺失，或迁移未跑的裸库读取（跨版本快照）。
	if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no such table") {
		return 0, nil
	}
	// 其余查询级错误原样上抛——恢复路径必须 fail-closed，禁止把 IO/锁异常当成"无代际"。
	return 0, fmt.Errorf("get server_epoch: %w", err)
}

// SetServerEpoch 持久化服务端代际（单例行）。仅在恢复成功路径的 commitReady 中调用；
// 恢复失败/回滚路径绝不触及（旧库连文件一起复位，epoch 自然不变，v20 §85）。
func SetServerEpoch(epoch int64) error {
	db := database.GetDB()
	if db == nil {
		return errors.New("database not ready")
	}
	res, err := db.Exec("UPDATE sync_meta SET server_epoch = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1", epoch)
	if err != nil {
		return fmt.Errorf("set server_epoch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	// 行缺失（legacy 库迁移半途）：先补 seed 再写目标值，两步幂等。
	if _, err := db.Exec("INSERT OR IGNORE INTO sync_meta (id, server_epoch) VALUES (1, 1)"); err != nil {
		return fmt.Errorf("seed sync_meta: %w", err)
	}
	if _, err := db.Exec("UPDATE sync_meta SET server_epoch = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1", epoch); err != nil {
		return fmt.Errorf("set server_epoch (after seed): %w", err)
	}
	return nil
}

// NextRestoreEpoch 恢复成功后的新代际（v20 §85，纯函数便于测试）：
//
//	new = max(pre_epoch, restored_epoch) + 1
//
// pre=当前活动服务器代际；restored=归档内记录的代际（legacy 归档无字段=0）。
// 例：pre=42 恢复 epoch=17 的旧档 → 43（绝不倒退回 18，红线 14）；
//
//	pre=2 恢复 legacy(0) → 3。
func NextRestoreEpoch(pre, restored int64) int64 {
	if restored > pre {
		pre = restored
	}
	return pre + 1
}
