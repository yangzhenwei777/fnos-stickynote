package database

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"stickynote/logger"
)

// configDescriptions 是【全部配置】页的唯一文案源：新装播种、存量刷新、
// 内部迁移旗标插入共用，避免多处文案漂移。文案中禁止出现版本号/工单号。
var configDescriptions = map[string]string{
	"allow_register":           "允许新用户注册（新装默认关闭；首个管理员注册不受此开关限制）",
	"site_title":               "应用标题",
	"max_notes_per_user":       "每用户最大笔记数",
	"cors_origins":             "CORS 跨域白名单（逗号分隔；* 全放行；空=仅同源）",
	"max_storage_mb_per_user":  "每用户上传存储总配额（MB，0=不限）",
	"max_note_content_kb":      "用户每条笔记的大小上限（KB），超出将无法保存。0=不限制",
	"trash_retention_days":     "回收站自动清理天数（0=永久保留）",
	"share_enabled":            "允许创建分享链接（公开只读）",
	"history_max_per_note":     "每篇笔记保留的历史版本份数（默认 50；范围 0~1000；0=停止生成新历史，已有历史保留）",
	"history_retention_days":   "历史版本保留天数（默认 180；范围 1~3650）",
	"history_max_total_mb":     "历史版本全库总容量上限（MB，默认 2048；范围 64~102400；按文本内容估算）",
	"uploads_backfilled":       "系统内部标记：uploads 存量回填与孤儿清理已完成",
	"note_uploads_backfilled":  "系统内部标记：note_uploads 存量回填已完成",
	"note_uploads_fk_added":    "系统内部标记：note_uploads 外键约束已添加",
	"title_entities_unescaped": "系统内部标记：存量标题 HTML 实体清洗已完成",
	"orphan_snapshots_purged":  "系统内部标记：存量孤儿备份快照清理已完成",
}

const uploadsBackfilledPartialDesc = "uploads 存量处理部分失败，待下次启动重试"

// ConfigDescription 返回配置项的标准文案；未知 key 返回空串。
func ConfigDescription(key string) string {
	return configDescriptions[key]
}

// ensureHistoryConfigs 幂等补入三项历史配置。
// 环境变量仅在键首次创建时作为初始值，之后以系统配置页/数据库为准。
func ensureHistoryConfigs() error {
	items := []struct {
		key, def string
		min, max int
	}{
		{"history_max_per_note", "50", 0, 1000},
		{"history_retention_days", "180", 1, 3650},
		{"history_max_total_mb", "2048", 64, 102400},
	}
	for _, item := range items {
		value := envIntOr(strings.ToUpper(item.key), item.def, item.min, item.max)
		if _, err := DB.Exec(
			"INSERT OR IGNORE INTO configs (key, value, description) VALUES (?, ?, ?)",
			item.key, value, ConfigDescription(item.key),
		); err != nil {
			return fmt.Errorf("ensure config %s: %w", item.key, err)
		}
	}
	return nil
}

// refreshConfigDescriptions 每次启动把已知配置项的描述刷新为标准文案。
// 只改 description，不改 value / updated_at；存量库的旧版本号文案由此收敛。
func refreshConfigDescriptions() error {
	for key, desc := range configDescriptions {
		if key == "uploads_backfilled" {
			var value string
			if err := DB.QueryRow("SELECT value FROM configs WHERE key = ?", key).Scan(&value); err == nil && value == "partial" {
				desc = uploadsBackfilledPartialDesc
			}
		}
		if _, err := DB.Exec(
			"UPDATE configs SET description = ? WHERE key = ? AND COALESCE(description, '') <> ?",
			desc, key, desc,
		); err != nil {
			return fmt.Errorf("refresh config description %s: %w", key, err)
		}
	}
	return nil
}

// migrateLegacyConfigs 处理历史遗留键：
//   - orphan_snapshots_purged_v170 → orphan_snapshots_purged（保留值）
//   - 删除已废弃的 allow_external_images 死键（代码已无任何读取）
func migrateLegacyConfigs() error {
	if err := renameLegacyConfigKey("orphan_snapshots_purged_v170", "orphan_snapshots_purged"); err != nil {
		return err
	}
	res, err := DB.Exec("DELETE FROM configs WHERE key = 'allow_external_images'")
	if err != nil {
		return fmt.Errorf("delete legacy allow_external_images: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		logger.Info("Legacy config removed: allow_external_images")
	}
	return nil
}

func renameLegacyConfigKey(oldKey, newKey string) error {
	var oldCount, newCount int
	if err := DB.QueryRow("SELECT COUNT(*) FROM configs WHERE key = ?", oldKey).Scan(&oldCount); err != nil {
		return err
	}
	if oldCount == 0 {
		return nil
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM configs WHERE key = ?", newKey).Scan(&newCount); err != nil {
		return err
	}
	if newCount == 0 {
		if _, err := DB.Exec(
			"UPDATE configs SET key = ?, description = ? WHERE key = ?",
			newKey, ConfigDescription(newKey), oldKey,
		); err != nil {
			return fmt.Errorf("rename config %s -> %s: %w", oldKey, newKey, err)
		}
		logger.Info("Config key renamed: %s -> %s", oldKey, newKey)
		return nil
	}
	if _, err := DB.Exec("DELETE FROM configs WHERE key = ?", oldKey); err != nil {
		return fmt.Errorf("drop duplicate legacy config %s: %w", oldKey, err)
	}
	logger.Info("Legacy config removed: %s", oldKey)
	return nil
}

func envIntOr(name, def string, min, max int) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		return def
	}
	return strconv.Itoa(v)
}
