package model

import (
	"errors"
	"time"

	"stickynote/database"
)

// ErrDBUnavailable 表示数据库尚未初始化（启动早期 / 单元测试环境）。
var ErrDBUnavailable = errors.New("database not initialized")

type Config struct {
	ID          int64     `json:"id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func GetConfig(key string) (string, error) {
	// 防御：DB 未初始化时返回错误而不是 nil 解引用 panic。
	// 触发场景：启动早期请求、以及只装配中间件不建库的单元测试
	//（v2.1.2 CORS 单测就撞上了这个空指针）。
	if database.DB == nil {
		return "", ErrDBUnavailable
	}
	var value string
	err := database.DB.QueryRow("SELECT value FROM configs WHERE key = ?", key).Scan(&value)
	return value, err
}

func GetAllConfigs() ([]Config, error) {
	rows, err := database.DB.Query("SELECT id, key, value, description, updated_at FROM configs ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []Config
	for rows.Next() {
		var c Config
		if err := rows.Scan(&c.ID, &c.Key, &c.Value, &c.Description, &c.UpdatedAt); err != nil {
			return nil, err
		}
		configs = append(configs, c)
	}
	return configs, nil
}

func SetConfig(key, value string) error {
	_, err := database.DB.Exec(
		"INSERT INTO configs (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP",
		key, value,
	)
	return err
}

func GetPublicConfigs() (map[string]string, error) {
	// v21.107 P2-8：trash_retention_days 入公开集——回收站保留期是普通用户
	// 也应看到的展示型配置（此前仅 /configs 管理员可查，普通用户恒显示「永久保留」与实际策略不符）
	rows, err := database.DB.Query("SELECT key, value FROM configs WHERE key IN ('allow_register', 'site_title', 'trash_retention_days')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		result[k] = v
	}
	return result, nil
}
