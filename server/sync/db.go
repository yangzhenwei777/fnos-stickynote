// db.go —— sync 包的 DB 句柄注入（v21.166 计划项 5 · X3 销账）。
//
// 背景（长期挂账 X3）：本包链路测试此前只能跑契约假面（stub）—— 假面测的是
// "我以为的 SQL 行为"，对不上真实事务/驱动语义时会出现**假绿**（用例全过、线上仍炸）。
// 根因是 handlers 直连进程级全局 `database.DB`：进程内起真服务端做集成测试时，
// 与被测实例抢同一个全局句柄，测试之间互相污染。
//
// 本文件提供**包级可注入**的句柄：
//   - 生产：未注入时行为与此前**完全一致**（走 store.go 既有的 db() → database.GetDB()）；
//   - 测试：SetDB 注入专用句柄，即可在本进程内跑真服务端链路（同一份 SQL/事务语义）。
//
// 取句柄只有一个决策点（store.go 的 db()），本文件只负责"注入/解除注入"，
// 避免出现第二个取句柄入口导致注入失效。
package sync

import (
	"database/sql"
	"sync/atomic"

	"stickynote/database"
)

// injectedDB 优先句柄；nil 表示"未注入"（生产路径）。
// 用 atomic 而非裸变量：本包 handler 并发调用，注入/解除多发生在测试装配期。
var injectedDB atomic.Pointer[sql.DB]

// SetDB 注入本包专用 DB 句柄（供进程内真服务端集成测试）。
// 传 nil 恢复默认（回落到全局 database.GetDB()）。
// 注意：调用方负责注入句柄的生命周期——本包不接管 Close。
func SetDB(h *sql.DB) { injectedDB.Store(h) }

// dbFilePath 返回底层库文件路径。
// 未注入时与历史行为一致；注入时仍走全局接口取路径（避免重复维护路径推导，
// 且生产路径必须与此前逐字节一致）。
func dbFilePath() string { return database.DBFilePath() }
