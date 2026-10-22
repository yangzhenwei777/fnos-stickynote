package database

import (
	"sync/atomic"
	"time"
)

// 在线全量恢复的「安全窗口」支撑（v1.0.168 切片 3）。
//
// 恢复要在**不关闭 HTTP 服务**的前提下换掉整个 SQLite 库 + 附件目录。危险点在于：
// 关闭旧 DB、重开新 DB 的窗口里，任何还在摸 database.DB 的请求都会拿到关闭句柄
// （甚至 nil → panic，Health 的 DB.Ping 首当其冲）。
//
// 做法：置一个 restoring 门 + 一个在途请求计数。
//   - RestorationGate 中间件：restoring 时除白名单（/health、备份状态/恢复进度）外一律 503；
//     其余请求进入即计数、结束即销账。
//   - 恢复执行序：SetRestoring(true) → WaitInflight 排空在途 → Close → 破坏性换文件 → Reopen
//     → SetRestoring(false)。排空失败（有卡死的长请求）则在动手前放弃、恢复原状，绝不半途。

var (
	restoring     atomic.Bool
	inflightCount atomic.Int64
)

// SetRestoring 打开/关闭安全窗口（仅恢复流程调用）。
func SetRestoring(v bool) { restoring.Store(v) }

// IsRestoring 是否处于恢复安全窗口。
func IsRestoring() bool { return restoring.Load() }

// EnterRequest / ExitRequest 供中间件登记在途请求（用于排空）。
func EnterRequest() { inflightCount.Add(1) }
func ExitRequest()  { inflightCount.Add(-1) }

// WaitInflight 等待在途请求清零；超时返回 false（调用方据此在破坏性动作前放弃）。
func WaitInflight(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for inflightCount.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}
