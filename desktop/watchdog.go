package main

// watchdog.go —— v21.177：数据库连接池饱和看门狗（216 连接池死锁定案的观测面）。
//
// 背景（2026-10-13 定案）：连接池上限 2（§93 纪律），历史缺陷可在「持 Rows 再取连接 ×
// 并发调用」下永久互锁；而 database/sql 的连接等待**无超时、无日志** ⇒ 整进程静默瘫痪
//（UI 列表永不加载 / 同步轮不推进 / 关机「换票仍在途>16s」幽灵警告 +「调度器未在预期
// 时限内退出」），排查成本极高（必须 goroutine 转储才能看见）。
//
// 根治是消除嵌套模式（v21.177 已修 ListFolders 及同族）；本看门狗负责：**若再有任何
// 长阻塞**，让它可见、可取证——池持续饱和 ≥90s 时把 goroutine 全量转储写进 logs/ 并留
// 一条醒目日志（限频 10 分钟，防刷盘）。
//
// 注意：本看门狗**只观测与取证、不做自愈动作**（强杀 goroutine / 重启调度都有正确性风险，
// 与「fail-safe 优先」的既有纪律一致）。

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"stickynote/database"
)

const (
	watchdogTick       = 20 * time.Second
	watchdogStuckAfter = 90 * time.Second // 持续饱和超过阈值才判"卡死"（正常重负载不会这么久）
	watchdogMinDumpGap = 10 * time.Minute // 两次转储的最小间隔
)

// startDBWatchdog 启动池饱和看门狗（进程生命周期，无需停止）。
func startDBWatchdog(profileDir string) {
	go func() {
		var saturatedSince time.Time
		var lastDumpAt time.Time
		for {
			time.Sleep(watchdogTick)
			db := database.GetDB()
			if db == nil {
				continue
			}
			st := db.Stats()
			full := st.MaxOpenConnections > 0 && st.InUse >= st.MaxOpenConnections
			switch {
			case !full:
				saturatedSince = time.Time{}
			case saturatedSince.IsZero():
				saturatedSince = time.Now()
			case time.Since(saturatedSince) >= watchdogStuckAfter &&
				time.Since(lastDumpAt) >= watchdogMinDumpGap:
				if p, err := writeGoroutineDump(profileDir, st); err != nil {
					log.Printf("[watchdog] 数据库连接池持续饱和 ≥%s（InUse=%d/%d WaitCount=%d），但转储导出失败: %v",
						watchdogStuckAfter, st.InUse, st.MaxOpenConnections, st.WaitCount, err)
				} else {
					log.Printf("[watchdog] 数据库连接池持续饱和 ≥%s（InUse=%d/%d WaitCount=%d）——疑似长阻塞/死锁，goroutine 现场已导出: %s",
						watchdogStuckAfter, st.InUse, st.MaxOpenConnections, st.WaitCount, p)
				}
				lastDumpAt = time.Now()
				saturatedSince = time.Now() // 重置计时：下次需再满 90s 且距上次 ≥10 分钟
			}
		}
	}()
}

// writeGoroutineDump 把全量 goroutine 栈 + 池统计写入 <profileDir>/logs/goroutine-dump-<ts>.txt。
func writeGoroutineDump(profileDir string, st sql.DBStats) (string, error) {
	dir := filepath.Join(profileDir, "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("goroutine-dump-%d.txt", time.Now().Unix()))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprintf(f, "DB.STATS: MaxOpen=%d Open=%d InUse=%d Idle=%d WaitCount=%d WaitDuration=%s\n\n",
		st.MaxOpenConnections, st.OpenConnections, st.InUse, st.Idle, st.WaitCount, st.WaitDuration)
	buf := make([]byte, 4<<20)
	n := runtime.Stack(buf, true)
	if _, err := f.Write(buf[:n]); err != nil {
		return "", err
	}
	return path, nil
}
