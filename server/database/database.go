package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
	"stickynote/logger"
)

var (
	DB *sql.DB
	// v21.100：DB 读写保护——loginGuard persistWorker 为进程级单例后台 goroutine，
	// 测试中跨测试存活，其读取与 database.Init() 的重初始化并发（CI go test -race
	// 实测 DATA RACE）。写入（Init）与后台读取（GetDB）经互斥锁同步。
	dbMu sync.RWMutex

	dbFilePath string // 最近一次 Init 的库文件路径（批次5 快照 VACUUM 源）
)

// DBFilePath 返回当前库文件路径（未 Init 为空串）。
func DBFilePath() string {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return dbFilePath
}

// GetDB 返回当前数据库句柄（并发安全读，v21.100）。
func GetDB() *sql.DB {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return DB
}

// FTS5Enabled 全文搜索开关（v17）。默认 true；ensureSchema 建 notes_fts 失败时
// 启动期降级为 false，model 层搜索自动回退 LIKE（不阻断服务启动）。
var FTS5Enabled = true

func Init(dbPath string, appVersion string) error {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	var err error
	// v21.95 B-1：DSN 加 _txlock=immediate —— 所有 Begin() 变为 BEGIN IMMEDIATE，
	// 事务开始即持写锁，消除 SQLite 读→写锁升级导致的 "database is locked"
	// （busy_timeout(5000) + MaxOpenConns(2) 下长事务持锁阻塞场景，审计 P2）。
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(10000)&_txlock=immediate")
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	dbMu.Lock()
	DB = db
	dbFilePath = dbPath
	dbMu.Unlock()

	// v19.5：RegisterUser 已重构为单语句原子 INSERT（CASE WHEN + WHERE NOT EXISTS），
	// 不再依赖单连接串行。2 连接允许 1 读 + 1 写并行，SQLite WAL 安全。
	db.SetMaxOpenConns(2)

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	if err := runUpgrades(appVersion); err != nil {
		return fmt.Errorf("failed to run upgrades: %w", err)
	}

	// 预热第二个连接（v20.8 审计优化）：NAS 冷启动时避免首次写操作等待连接建立
	// 捕获当前连接句柄 db，避免后台 goroutine 与（测试中）后续重初始化竞争读取全局 DB。
	go func() {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master").Scan(&n); err != nil {
			logger.Error("db warmup query failed: %v", err) // v21.58 F-16：错误不再忽略
		}
	}()

	return nil
}

// Close 关闭数据库连接（优雅关闭用，v20.8 审计优化）。
func Close() {
	if DB != nil {
		DB.Close()
	}
}

// SetDB 受控替换全局句柄（离线方案批次4·刀1 / desktop localstore 生成库切换）。
// 持 dbMu 写锁完成换入+旧句柄关闭，与 Init 的重初始化同一竞争纪律（v21.100）。
// 语义约束：newDB 必须是**已按目标档位 PRAGMA 打开并回读验证**的句柄（desktop 侧
// localstore.OpenGenDB 负责），本函数不做任何 SQL；服务端(NAS)运行路径永不调用。
// newDB==nil 视为误用，直接拒绝且不动当前句柄。
func SetDB(newDB *sql.DB) (old *sql.DB) {
	if newDB == nil {
		return nil
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	old = DB
	DB = newDB
	return old
}
