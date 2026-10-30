package database

import (
	"database/sql"
	"fmt"
	"html"

	"stickynote/logger"
)

type upgrade struct {
	version string
	fn      func(tx *sql.Tx) error
}

// upgrades 按版本号升序排列，新增版本追加在末尾
var upgrades = []upgrade{
	{"1.0.0", upgrade_v1_0_0},
	// {"1.2.0", upgrade_v1_2_0},
	// {"1.3.0", nil},  // fn 为 nil 表示空升级，仅记录版本
}

func runUpgrades(appVersion string) error {
	appVersion = normalizeVersion(appVersion)

	// 建表
	if _, err := DB.Exec(`
		CREATE TABLE IF NOT EXISTS upgrade_records (
			version TEXT PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("failed to create upgrade_records table: %w", err)
	}

	// 读取当前 DB 版本
	var dbVersion string
	err := DB.QueryRow("SELECT COALESCE(MAX(version), '') FROM upgrade_records").Scan(&dbVersion)
	if err != nil {
		return fmt.Errorf("failed to read current db version: %w", err)
	}
	if dbVersion == "" {
		dbVersion = "0.0.0"
	}

	// 防降级：DB 版本不能高于二进制版本
	if compareVersions(dbVersion, appVersion) > 0 {
		return fmt.Errorf("database version (%s) is newer than binary version (%s), cannot downgrade", dbVersion, appVersion)
	}

	if dbVersion == appVersion {
		logger.Info("Version %s, no upgrade needed", appVersion)
	} else {

		logger.Info("Upgrading from %s to %s", dbVersion, appVersion)

		// 按顺序执行升级脚本
		for _, u := range upgrades {
			if compareVersions(u.version, dbVersion) <= 0 {
				continue // 已执行过
			}
			if compareVersions(u.version, appVersion) > 0 {
				continue // 超出当前二进制版本
			}

			tx, err := DB.Begin()
			if err != nil {
				return fmt.Errorf("failed to begin transaction for upgrade %s: %w", u.version, err)
			}

			if u.fn != nil {
				if err := u.fn(tx); err != nil {
					tx.Rollback()
					return fmt.Errorf("upgrade %s failed: %w", u.version, err)
				}
			}

			if _, err := tx.Exec("INSERT INTO upgrade_records (version) VALUES (?)", u.version); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to record upgrade %s: %w", u.version, err)
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("failed to commit upgrade %s: %w", u.version, err)
			}

			logger.Info("Upgrade to %s applied", u.version)
		}
	}

	// 版本无关的幂等 schema 补丁：每次启动都执行，对新老库均生效。
	// 放在版本迁移之后——全新数据库的表由上方迁移刚建好，
	// 补丁（content_text 列、FTS5 索引等）可在首次启动一次到位。
	if err := ensureSchema(); err != nil {
		return fmt.Errorf("failed to ensure schema: %w", err)
	}
	if err := ensureAdminLogsTable(); err != nil {
		return fmt.Errorf("failed to ensure admin_logs: %w", err)
	}
	if err := ensureLoginFailsTable(); err != nil {
		return fmt.Errorf("failed to ensure login_fails: %w", err)
	}

	return nil
}

func upgrade_v1_0_0(tx *sql.Tx) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			security_question TEXT DEFAULT '',
			security_answer_hash TEXT DEFAULT '',
			role TEXT NOT NULL DEFAULT 'user',
			token_version INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS notes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		title TEXT NOT NULL DEFAULT 'Untitled',
		content TEXT NOT NULL DEFAULT '',
		content_text TEXT NOT NULL DEFAULT '',
		tags TEXT DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME DEFAULT NULL,
		folder_id INTEGER DEFAULT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_user_id ON notes(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_updated_at ON notes(updated_at)`,
		// v21.86 文件夹：自定义文件夹表；"我的笔记"为虚拟默认（notes.folder_id IS NULL）
		`CREATE TABLE IF NOT EXISTS note_folders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		sort_order INTEGER NOT NULL DEFAULT 0,
		is_encrypted INTEGER NOT NULL DEFAULT 0,
		encrypt_password_hash TEXT NOT NULL DEFAULT '',
		encrypted_at DATETIME DEFAULT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)`,
		`CREATE INDEX IF NOT EXISTS idx_note_folders_user_id ON note_folders(user_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_note_folders_user_name ON note_folders(user_id, name)`,
		`CREATE INDEX IF NOT EXISTS idx_note_folders_user_locked ON note_folders(user_id, is_encrypted)`,
		`CREATE TABLE IF NOT EXISTS configs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT NOT NULL UNIQUE,
			value TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS uploads (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			filename TEXT NOT NULL UNIQUE,
			user_id INTEGER NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			sha256 TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_uploads_user_id ON uploads(user_id)`,
		// v21.97（用户反馈 B1）：site_title 默认值「便签」→「Sticky note」（应用改名）。
		// INSERT OR IGNORE 仅影响全新安装（键已存在则跳过）；存量库的 site_title 保持用户
		// 既有值不变（含用户已自行修改的标题）。
		// v21.133 M-04（审计 Grok-M-04 / GPT）：allow_register 种子由 'true' 改为 'false'——
		// 新装实例默认不开放自助注册。仅影响全新安装（存量库保持用户既有值）。首管注册不受影响：
		// 注册处理仅在 CountUsers()>0 时校验此开关，故首个管理员（count==0）总能注册；之后局域网
		// 自助注册默认关闭，管理员可在【配额与限额/系统配置】中重新开启。
		`INSERT OR IGNORE INTO configs (key, value, description) VALUES
			('allow_register', 'false', '允许新用户注册（新装默认关闭；首个管理员注册不受此开关限制）'),
			('site_title', 'Sticky note', '应用标题'),
			('max_notes_per_user', '1000', '每用户最大笔记数'),
			('cors_origins', '', 'CORS 跨域白名单（逗号分隔；* 全放行；空=仅同源）'),
			('max_storage_mb_per_user', '1024', '每用户上传存储总配额（MB，0=不限）'),
			('max_note_content_kb', '0', '用户每条笔记的大小上限（KB），超出将无法保存。0=不限制'),
			('trash_retention_days', '0', '回收站自动清理天数（0=永久保留，P2-6）')`,
	}

	for _, q := range queries {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func normalizeVersion(v string) string {
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		return v[1:]
	}
	return v
}

// ensureSchema 每次启动执行的幂等 schema 检查：
// 补充版本迁移覆盖不到的列（应用版本固定时版本迁移不会再次触发）。
// 各表独立判存：全新数据库首次启动时表刚由版本迁移建好，补丁照常生效。
func ensureSchema() error {
	// users.token_version：refresh token 全局吊销所需的版本号列
	usersHas, err := tableExists("users")
	if err != nil {
		return err
	}
	if usersHas {
		has, err := columnExists("users", "token_version")
		if err != nil {
			return err
		}
		if !has {
			if _, err := DB.Exec("ALTER TABLE users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("failed to add users.token_version: %w", err)
			}
			logger.Info("Schema patched: users.token_version added")
		}

		// users.refresh_jti（v21.93 P1）：当前有效 refresh token 的 jti，
		// 用于一次性轮转的重放检测。存量库默认为空串 = 尚未轮转过，
		// Refresh 时接受旧票并登记新 jti（不强制已登录用户重新登录）。
		hasJTI, err := columnExists("users", "refresh_jti")
		if err != nil {
			return err
		}
		if !hasJTI {
			if _, err := DB.Exec("ALTER TABLE users ADD COLUMN refresh_jti TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("failed to add users.refresh_jti: %w", err)
			}
			logger.Info("Schema patched: users.refresh_jti added")
		}
	}

	// refresh_sessions（v21.94 P3-7 多设备）：多设备 refresh 一次性轮转的会话表。
	// 一次登录 = 一个 family；轮转在 family 内延续；重放（已吊销 jti 再现）撤销整个 family；
	// 登出/改密/重置批量撤销该用户全部活跃会话。users.refresh_jti 列保留兼容回滚。
	// 幂等：CREATE TABLE IF NOT EXISTS + 索引同语句创建。
	if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS refresh_sessions (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id      INTEGER NOT NULL,
		family_id    TEXT    NOT NULL,
		jti          TEXT    NOT NULL UNIQUE,
		device_name  TEXT    NOT NULL DEFAULT '',
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at   INTEGER NOT NULL,
		revoked_at   INTEGER DEFAULT NULL,
		last_used_at INTEGER DEFAULT NULL
	)`); err != nil {
		return fmt.Errorf("failed to create refresh_sessions: %w", err)
	}
	if _, err := DB.Exec("CREATE INDEX IF NOT EXISTS idx_refresh_sessions_user ON refresh_sessions(user_id)"); err != nil {
		return fmt.Errorf("failed to create refresh_sessions user index: %w", err)
	}
	if _, err := DB.Exec("CREATE INDEX IF NOT EXISTS idx_refresh_sessions_family ON refresh_sessions(family_id)"); err != nil {
		return fmt.Errorf("failed to create refresh_sessions family index: %w", err)
	}

	// configs.cors_origins（v18）：CORS 跨域白名单配置键，默认空（仅同源）。
	// 老库升级时幂等补入，管理后台通用配置页即可调整。
	// v18.7 同路补入：存储配额 / 内容上限 / 图片外链开关。
	configsHas, err := tableExists("configs")
	if err != nil {
		return err
	}
	if configsHas {
		if _, err := DB.Exec(`INSERT OR IGNORE INTO configs (key, value, description) VALUES ('cors_origins', '', 'CORS 跨域白名单（逗号分隔；* 全放行；空=仅同源）')`); err != nil {
			return fmt.Errorf("failed to ensure configs.cors_origins: %w", err)
		}
		if _, err := DB.Exec(`INSERT OR IGNORE INTO configs (key, value, description) VALUES
			('max_storage_mb_per_user', '1024', '每用户上传存储总配额（MB，0=不限）'),
			('max_note_content_kb', '0', '用户每条笔记的大小上限（KB），超出将无法保存。0=不限制')`); err != nil {
			return fmt.Errorf("failed to ensure v18.7 config keys: %w", err)
		}
		if _, err := DB.Exec(`INSERT OR IGNORE INTO configs (key, value, description) VALUES
			('trash_retention_days', '0', '回收站自动清理天数（0=永久保留，P2-6）')`); err != nil {
			return fmt.Errorf("failed to ensure trash_retention_days config: %w", err)
		}
	}

	// uploads 表（v18.5）：上传文件归属跟踪，删用户时按表物理清理文件。
	// 与 upgrade_v1_0_0 的 DDL 双路同步（踩坑 #29：两路必须一致）。
	if uploadsHas, err := tableExists("uploads"); err != nil {
		return err
	} else if !uploadsHas {
		if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS uploads (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			filename TEXT NOT NULL UNIQUE,
			user_id INTEGER NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			sha256 TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`); err != nil {
			return fmt.Errorf("failed to create uploads table: %w", err)
		}
		if _, err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_uploads_user_id ON uploads(user_id)`); err != nil {
			return fmt.Errorf("failed to create uploads index: %w", err)
		}
		logger.Info("Schema patched: uploads table created")
	}

	// notes 相关补丁（deleted_at / content_text / FTS5）
	notesHas, err := tableExists("notes")
	if err != nil {
		return err
	}
	if !notesHas {
		return nil
	}

	// notes.deleted_at：软删除（回收站）标记，NULL 表示正常笔记
	if has, err := columnExists("notes", "deleted_at"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE notes ADD COLUMN deleted_at DATETIME DEFAULT NULL"); err != nil {
			return fmt.Errorf("failed to add notes.deleted_at: %w", err)
		}
		logger.Info("Schema patched: notes.deleted_at added")
	}

	// notes.content_text（v17）：剥标签后的纯文本，供 FTS5 索引与导出使用
	if has, err := columnExists("notes", "content_text"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE notes ADD COLUMN content_text TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("failed to add notes.content_text: %w", err)
		}
		logger.Info("Schema patched: notes.content_text added")
	}

	// 回填存量行 content_text（老库升级后该列为空串）
	if err := backfillContentText(); err != nil {
		return fmt.Errorf("failed to backfill notes.content_text: %w", err)
	}

	// FTS5 全文索引基础设施（v17）。初始化失败不阻断启动：
	// 降级 FTS5Enabled=false，搜索回退 LIKE。
	if err := ensureFTS5(); err != nil {
		FTS5Enabled = false
		logger.Error("FTS5 init failed, search falls back to LIKE: %v", err)
	}

	// v20.0 笔记加密：is_encrypted / encrypt_password_hash / encrypted_content
	if has, err := columnExists("notes", "is_encrypted"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE notes ADD COLUMN is_encrypted INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("failed to add notes.is_encrypted: %w", err)
		}
		logger.Info("Schema patched: notes.is_encrypted added")
	}
	if has, err := columnExists("notes", "encrypt_password_hash"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE notes ADD COLUMN encrypt_password_hash TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("failed to add notes.encrypt_password_hash: %w", err)
		}
		logger.Info("Schema patched: notes.encrypt_password_hash added")
	}
	if has, err := columnExists("notes", "encrypted_content"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE notes ADD COLUMN encrypted_content TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("failed to add notes.encrypted_content: %w", err)
		}
		logger.Info("Schema patched: notes.encrypted_content added")
	}

	// P3-14: 回收站统计优化 — trash_upload_bytes 缓存
	if has, err := columnExists("notes", "trash_upload_bytes"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE notes ADD COLUMN trash_upload_bytes INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("failed to add notes.trash_upload_bytes: %w", err)
		}
		logger.Info("Schema patched: notes.trash_upload_bytes added")
	}

	// v21.72 P2-08：并发编辑乐观锁 version 列（每次更新 +1，更新时 WHERE version 匹配实现 CAS）
	if has, err := columnExists("notes", "version"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE notes ADD COLUMN version INTEGER NOT NULL DEFAULT 1"); err != nil {
			return fmt.Errorf("failed to add notes.version: %w", err)
		}
		logger.Info("Schema patched: notes.version added")
	}

	// v21.86 文件夹（老库幂等补丁，与 upgrade_v1_0_0 DDL 双路同步——踩坑 #29）：
	// ① note_folders 表：自定义文件夹
	foldersHas, err := tableExists("note_folders")
	if err != nil {
		return err
	}
	if !foldersHas {
		if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS note_folders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`); err != nil {
			return fmt.Errorf("failed to create note_folders table: %w", err)
		}
		logger.Info("Schema patched: note_folders table created")
	}
	if _, err := DB.Exec("CREATE INDEX IF NOT EXISTS idx_note_folders_user_id ON note_folders(user_id)"); err != nil {
		return fmt.Errorf("failed to create note_folders user index: %w", err)
	}
	if _, err := DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_note_folders_user_name ON note_folders(user_id, name)"); err != nil {
		return fmt.Errorf("failed to create note_folders name unique index: %w", err)
	}

	// v2.3.0 文件夹访问锁（老库幂等补列；新库 DDL 已含）
	if has, err := columnExists("note_folders", "is_encrypted"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE note_folders ADD COLUMN is_encrypted INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("failed to add note_folders.is_encrypted: %w", err)
		}
		logger.Info("Schema patched: note_folders.is_encrypted added")
	}
	if has, err := columnExists("note_folders", "encrypt_password_hash"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE note_folders ADD COLUMN encrypt_password_hash TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("failed to add note_folders.encrypt_password_hash: %w", err)
		}
		logger.Info("Schema patched: note_folders.encrypt_password_hash added")
	}
	if has, err := columnExists("note_folders", "encrypted_at"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE note_folders ADD COLUMN encrypted_at DATETIME DEFAULT NULL"); err != nil {
			return fmt.Errorf("failed to add note_folders.encrypted_at: %w", err)
		}
		logger.Info("Schema patched: note_folders.encrypted_at added")
	}
	if _, err := DB.Exec("CREATE INDEX IF NOT EXISTS idx_note_folders_user_locked ON note_folders(user_id, is_encrypted)"); err != nil {
		return fmt.Errorf("failed to create note_folders lock index: %w", err)
	}

	// ② notes.folder_id 列：NULL = 默认"我的笔记"（老笔记自动归入默认，无需回填）
	if has, err := columnExists("notes", "folder_id"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE notes ADD COLUMN folder_id INTEGER DEFAULT NULL"); err != nil {
			return fmt.Errorf("failed to add notes.folder_id: %w", err)
		}
		logger.Info("Schema patched: notes.folder_id added")
	}

	// P3-11: 标签关联表 — 替代逗号字符串
	if tagsHas, err := tableExists("note_tags"); err != nil {
		return err
	} else if !tagsHas {
		if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS note_tags (
			note_id INTEGER NOT NULL,
			tag TEXT NOT NULL,
			PRIMARY KEY (note_id, tag),
			FOREIGN KEY (note_id) REFERENCES notes(id) ON DELETE CASCADE
		)`); err != nil {
			return fmt.Errorf("failed to create note_tags: %w", err)
		}
		// 迁移存量数据
		// 注意：json_each() 表值函数自身含 id 列（元素序号），
		// 与 notes.id 冲突会导致 "ambiguous column name: id"，
		// 故 SELECT 必须显式限定为 notes.id。
		if _, err := DB.Exec(`INSERT OR IGNORE INTO note_tags (note_id, tag)
			SELECT notes.id, TRIM(value)
			FROM notes, json_each('["' || REPLACE(tags, ',', '","') || '"]')
			WHERE notes.tags != ''`); err != nil {
			logger.Error("note_tags migration: %v", err)
		}
		logger.Info("Schema patched: note_tags table created with existing data migrated")
	}

	// v21.58 F-02: 笔记附件关系表——替代「扫描 notes.content 提取附件」，
	// 加密笔记（Content 为空）也能通过此表追踪附件生命周期。
	nuHas, err := tableExists("note_uploads")
	if err != nil {
		return err
	}
	if !nuHas {
		if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS note_uploads (
			note_id INTEGER NOT NULL,
			filename TEXT NOT NULL,
			PRIMARY KEY (note_id, filename),
			FOREIGN KEY (note_id) REFERENCES notes(id) ON DELETE CASCADE,
			FOREIGN KEY (filename) REFERENCES uploads(filename) ON DELETE CASCADE
		)`); err != nil {
			return fmt.Errorf("failed to create note_uploads: %w", err)
		}
		if _, err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_note_uploads_filename ON note_uploads(filename)`); err != nil {
			return fmt.Errorf("failed to create note_uploads index: %w", err)
		}
		logger.Info("Schema patched: note_uploads table created")
	}

	// v21.68 P1-02：note_uploads 回填改为独立旗标控制的可重试迁移。
	// 此前只在建表时执行一次，中途失败即永久部分迁移；现改为每次启动
	// 检查旗标，未完成则重跑（INSERT OR IGNORE 幂等，可断点续跑）。
	var nuFlag string
	if err := DB.QueryRow("SELECT value FROM configs WHERE key = 'note_uploads_backfilled'").Scan(&nuFlag); err != nil || nuFlag != "true" {
		if berr := backfillNoteUploads(); berr != nil {
			logger.Error("note_uploads backfill incomplete, will retry next start: %v", berr)
		} else {
			if _, err := DB.Exec("INSERT INTO configs (key, value, description) VALUES ('note_uploads_backfilled', 'true', 'note_uploads 存量回填已完成（v21.68）') ON CONFLICT(key) DO UPDATE SET value = 'true'"); err != nil {
				return err
			}
			logger.Info("note_uploads backfill completed")
		}
	}

	// v21.71 P2-02：老库 note_uploads 补 FK 到 uploads(filename)——重建表（SQLite 不支持 ALTER 加 FK）。
	// 必须先回填完成（保证新插入关系校验归属），再补 FK；旗标控制只执行一次。
	if nuHas {
		var fkFlag string
		if err := DB.QueryRow("SELECT value FROM configs WHERE key = 'note_uploads_fk_added'").Scan(&fkFlag); err != nil || fkFlag != "true" {
			if err := addNoteUploadsFK(); err != nil {
				logger.Error("note_uploads add FK failed, will retry next start: %v", err)
			} else {
				logger.Info("note_uploads FK to uploads added")
			}
		}
	}

	// v20.2 TOTP 两步验证
	if has, err := columnExists("users", "totp_secret"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("failed to add users.totp_secret: %w", err)
		}
		logger.Info("Schema patched: users.totp_secret added")
	}
	if has, err := columnExists("users", "totp_enabled"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("failed to add users.totp_enabled: %w", err)
		}
		logger.Info("Schema patched: users.totp_enabled added")
	}
	if has, err := tableExists("totp_recovery"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS totp_recovery (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			code_hash TEXT NOT NULL,
			used INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`); err != nil {
			return fmt.Errorf("failed to create totp_recovery: %w", err)
		}
		logger.Info("Schema patched: totp_recovery table created")
	}
	// v20.4: 用户状态字段（active/disabled）
	if has, err := columnExists("users", "status"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE users ADD COLUMN status TEXT NOT NULL DEFAULT 'active'"); err != nil {
			return fmt.Errorf("failed to add users.status: %w", err)
		}
		logger.Info("Schema patched: users.status added")
	}
	// v21.66 P1-3：uploads 附件生命周期状态机字段（staged/attached/orphan）
	if has, err := columnExists("uploads", "status"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE uploads ADD COLUMN status TEXT NOT NULL DEFAULT 'staged'"); err != nil {
			return fmt.Errorf("failed to add uploads.status: %w", err)
		}
		// v21.67 P1-02：首次引入 status 时回填——把被 note_uploads 引用的历史文件标记 attached，
		// 否则它们会停留在 DEFAULT 'staged'，被 staged GC 误删。
		if _, err := DB.Exec("UPDATE uploads SET status = 'attached' WHERE filename IN (SELECT filename FROM note_uploads)"); err != nil {
			logger.Error("backfill uploads attached status failed: %v", err)
		}
		logger.Info("Schema patched: uploads.status added + attached backfilled")
	}
	if has, err := columnExists("uploads", "status_changed_at"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE uploads ADD COLUMN status_changed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP"); err != nil {
			return fmt.Errorf("failed to add uploads.status_changed_at: %w", err)
		}
		logger.Info("Schema patched: uploads.status_changed_at added")
	}
	// P1 批次3（ADR-003 R1）：附件完整性校验位 sha256（'' = 未回填，由回填任务/即时补算收敛）。
	if has, err := columnExists("uploads", "sha256"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE uploads ADD COLUMN sha256 TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("failed to add uploads.sha256: %w", err)
		}
		logger.Info("Schema patched: uploads.sha256 added")
	}
	// v21.66 P2：唯一管理员 DB 不变量（应用层已检查，DB 层兜底防并发竞态创建第二个管理员）。
	// 若存量数据已存在多个管理员，索引创建失败仅记录日志，不阻断启动（数据现状优先）。
	if _, err := DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_users_single_admin ON users(role) WHERE role = 'admin'"); err != nil {
		logger.Error("create single-admin unique index failed (may already have multiple admins): %v", err)
	} else {
		logger.Info("Schema patched: single-admin unique index ensured")
	}
	// v21.67 P1-03：删除用户时物理文件删除失败的待清理队列（不依赖 users 外键，用户已删）
	if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS pending_upload_cleanup (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT NOT NULL UNIQUE,
		user_id INTEGER NOT NULL DEFAULT 0,
		retry_count INTEGER NOT NULL DEFAULT 0,
		last_attempt_at DATETIME DEFAULT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("failed to create pending_upload_cleanup: %w", err)
	}
	// v21.68 P2-03：为既有表补齐 user_id / retry_count / last_attempt_at 列
	if has, err := columnExists("pending_upload_cleanup", "user_id"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE pending_upload_cleanup ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("failed to add pending_upload_cleanup.user_id: %w", err)
		}
	}
	if has, err := columnExists("pending_upload_cleanup", "retry_count"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE pending_upload_cleanup ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("failed to add pending_upload_cleanup.retry_count: %w", err)
		}
	}
	if has, err := columnExists("pending_upload_cleanup", "last_attempt_at"); err != nil {
		return err
	} else if !has {
		if _, err := DB.Exec("ALTER TABLE pending_upload_cleanup ADD COLUMN last_attempt_at DATETIME DEFAULT NULL"); err != nil {
			return fmt.Errorf("failed to add pending_upload_cleanup.last_attempt_at: %w", err)
		}
	}

	// v21.105 P0-1：存量标题实体回溯清洗（一次性，旗标控制可重试）。
	// v21.79~v21.104 期间标题入库前被 html.EscapeString 转义，且前端把已转义
	// 标题原样回传导致每次保存追加一层转义（"Q&A"→"Q&amp;A"→"Q&amp;amp;A"…）。
	// 本迁移把每条标题反复反解到不动点，恢复其最早原文；此后新存路径不再转义。
	// 注意：v21.105 之前用户刻意输入的实体文本（如真想显示 "&amp;amp;"）会被
	// 一并反解，属可接受的极小概率取舍（详见交付说明 v21.105）。
	var titleFlag string
	if err := DB.QueryRow("SELECT value FROM configs WHERE key = 'title_entities_unescaped'").Scan(&titleFlag); err != nil || titleFlag != "true" {
		if berr := backfillUnescapeNoteTitles(); berr != nil {
			logger.Error("title entities backfill incomplete, will retry next start: %v", berr)
		} else {
			if _, err := DB.Exec("INSERT INTO configs (key, value, description) VALUES ('title_entities_unescaped', 'true', '存量标题 HTML 实体回溯清洗已完成（v21.105）') ON CONFLICT(key) DO UPDATE SET value = 'true'"); err != nil {
				return err
			}
			logger.Info("note title entities backfill completed")
		}
	}
	// v1.0.167 系统备份功能：备份目标/快照/副本/任务/恢复终结 五表（幂等，新表名须同步
	// 登记到 columnExists 白名单）。DDL 见 upgrade_backup.go。
	if err := ensureBackupTables(); err != nil {
		return fmt.Errorf("failed to ensure backup tables: %w", err)
	}
	// v1.0.175 R0：sync_meta 单例表（server_epoch/current_change_seq/protocol_version）。
	// 幂等建表 + seed epoch=1，新老库一次到位；DDL 见 upgrade_sync.go。
	if err := ensureSyncMetaTable(); err != nil {
		return fmt.Errorf("failed to ensure sync_meta: %w", err)
	}
	// 批次 5 P5：Bootstrap 快照账本 + 附件 Hold 两表（§73/§75）。
	if err := ensureSyncSnapshotTables(); err != nil {
		return fmt.Errorf("failed to ensure sync_snapshots: %w", err)
	}
	return nil
}

// backfillUnescapeNoteTitles 对全部笔记标题做 HTML 实体反解（到不动点），
// 覆盖 notes 表全部行（含软删除/回收站/加密笔记——标题均为明文存储）。
// 幂等：反解到不动点后再次执行结果不变；失败返回 error 供下次启动重试。
func backfillUnescapeNoteTitles() error {
	rows, err := DB.Query("SELECT id, title FROM notes WHERE title LIKE '%&%'")
	if err != nil {
		return fmt.Errorf("scan note titles: %w", err)
	}
	type fix struct {
		id    int64
		title string
	}
	var fixes []fix
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			rows.Close()
			return fmt.Errorf("scan note title row: %w", err)
		}
		un := unescapeToFixpoint(title)
		if un != title {
			fixes = append(fixes, fix{id: id, title: un})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate note titles: %w", err)
	}
	rows.Close()
	for _, f := range fixes {
		if _, err := DB.Exec("UPDATE notes SET title = ? WHERE id = ?", f.title, f.id); err != nil {
			return fmt.Errorf("update note %d title: %w", f.id, err)
		}
	}
	if len(fixes) > 0 {
		logger.Info("note title entities backfill: %d titles restored", len(fixes))
	}
	return nil
}

// unescapeToFixpoint 反复 html.UnescapeString 直到结果不再变化（上限 16 轮防御）。
// "&amp;amp;"（保存两次的 "Q&A"）→ "&amp;" → "&"；不含实体的字符串原样返回。
func unescapeToFixpoint(s string) string {
	for i := 0; i < 16; i++ {
		un := html.UnescapeString(s)
		if un == s {
			return s
		}
		s = un
	}
	return s
}

// backfillContentText 把存量笔记的 content 转为纯文本回填 content_text。
