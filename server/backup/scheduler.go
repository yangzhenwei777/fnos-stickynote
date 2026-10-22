package backup

import (
	"strconv"
	"strings"
	"time"

	"stickynote/logger"
	"stickynote/model"
)

// 备份调度器（切片 2）：随进程常驻的轻量 ticker，串行执行"到期"的启用目标。
// 与手动触发共用同一 running 互斥（一次只跑一个），忙时不推进 next_run_at、下 tick 重试。
//
// 设计取舍：
//   - tick 60s（远小于任何合理调度间隔），足以准点触发，又避免每分钟扫库抖动。
//   - 失败退避：连续失败次数驱动 2^n 指数退避，封顶 maxBackoff，避免坏目标把配额打爆。
//   - RPO 观测：成功但"距上次成功 > 2×间隔"记 WARN，提示数据保护窗口正在拉长。
//   - 不自动补偿错过的多次执行（NAS 休眠唤醒后按"现在"排下一次，不做 backfill）。

const (
	schedulerTick      = 60 * time.Second
	defaultSchedule    = 24 * time.Hour
	maxBackoff         = 7 * 24 * time.Hour
	rpoWarnMultiFactor = 2
)

// parseInterval 解析调度间隔：支持 "Nd"（天）与任意 Go duration（"24h"/"30m"）。非法/空→24h。
func parseInterval(s string) time.Duration {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return defaultSchedule
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "d"))); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return defaultSchedule
}

// backoffInterval 依据连续失败次数对基础间隔做 2^n 退避（封顶 maxBackoff）。
func backoffInterval(base time.Duration, consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 0 {
		return base
	}
	n := consecutiveFailures
	if n > 5 {
		n = 5 // 2^5=32×，再大也基本被 maxBackoff 截断
	}
	d := base << uint(n)
	if d <= 0 || d > maxBackoff { // 溢出或超限
		return maxBackoff
	}
	return d
}

// StartScheduler 启动调度循环（非阻塞）。stop 关闭时退出。
func (m *Manager) StartScheduler(stop <-chan struct{}) {
	go m.scheduleLoop(stop)
}

func (m *Manager) scheduleLoop(stop <-chan struct{}) {
	// 启动期：崩溃扫描（残留 STARTING/RUNNING → INTERRUPTED）。
	if n, err := m.OnStartup(); err != nil {
		logger.Error("backup scheduler: interrupt stale jobs failed: %v", err)
	} else if n > 0 {
		logger.Info("backup scheduler: marked %d stale job(s) INTERRUPTED", n)
	}
	m.ensureNextRuns()

	ticker := time.NewTicker(schedulerTick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.ensureNextRuns()
			m.runDueOnce()
			m.warnRPO()
		}
	}
}

// ensureNextRuns 给"启用但 next_run_at 为空"的目标补一个排程（新启用/编辑后重置）。
func (m *Manager) ensureNextRuns() {
	ids, err := model.ListEnabledTargetIDs()
	if err != nil {
		logger.Error("backup scheduler: list targets failed: %v", err)
		return
	}
	now := m.nowFunc().UTC()
	for _, id := range ids {
		t, err := model.GetBackupTarget(id)
		if err != nil || t.NextRunAt != nil {
			continue
		}
		_ = model.SetTargetNextRun(id, now.Add(parseInterval(t.ScheduleInterval)).Format(time.RFC3339))
	}
}

// runDueOnce 取一个到期目标，抢到互斥就异步执行；忙则保持 due 状态待下 tick 重试。
func (m *Manager) runDueOnce() {
	nowRFC := m.nowFunc().UTC().Format(time.RFC3339)
	due, err := model.ListDueTargets(nowRFC)
	if err != nil {
		logger.Error("backup scheduler: list due targets failed: %v", err)
		return
	}
	for _, t := range due {
		jobID, bid, err := m.BeginJob(t.ID, "scheduled")
		if err != nil {
			// ErrAlreadyRunning：留待下 tick；其它错误记日志并顺延，避免风暴。
			if err != ErrAlreadyRunning {
				logger.Error("backup scheduler: begin job for target %d failed: %v", t.ID, err)
				_ = model.SetTargetNextRun(t.ID, m.nowFunc().UTC().Add(schedulerTick).Format(time.RFC3339))
			}
			return // 一次只推进一个
		}
		logger.Info("backup scheduler: starting scheduled backup target=%d job=%d", t.ID, jobID)
		go m.finishScheduled(t.ID, jobID, bid, t.ScheduleInterval)
		return
	}
}

// finishScheduled 跑完一个调度 job 后，依据结果推进 next_run_at（成功=基础间隔，失败=退避）。
func (m *Manager) finishScheduled(targetID, jobID int64, backupID, intervalSpec string) {
	runErr := m.RunBackupJob(targetID, "scheduled", jobID, backupID)
	if runErr != nil {
		logger.Error("backup scheduler: target %d backup failed: %v", targetID, runErr)
	}
	base := parseInterval(intervalSpec)
	nt, err := model.GetBackupTarget(targetID)
	failures := 0
	if err == nil {
		failures = nt.ConsecutiveFailures
	}
	next := m.nowFunc().UTC().Add(backoffInterval(base, failures))
	_ = model.SetTargetNextRun(targetID, next.Format(time.RFC3339))
}

// warnRPO 对"启用、有历史成功、但距上次成功超阈值"的目标记 RPO 拉长告警。
func (m *Manager) warnRPO() {
	ids, err := model.ListEnabledTargetIDs()
	if err != nil {
		return
	}
	now := m.nowFunc().UTC()
	for _, id := range ids {
		t, err := model.GetBackupTarget(id)
		if err != nil || t.LastSuccessAt == nil {
			continue
		}
		base := parseInterval(t.ScheduleInterval)
		if now.Sub(t.LastSuccessAt.UTC()) > rpoWarnMultiFactor*base {
			logger.Info("[WARN] backup RPO: target %d 距上次成功已超 %v（>2×间隔 %v），数据保护窗口拉长",
				t.ID, now.Sub(t.LastSuccessAt.UTC()).Round(time.Minute), base)
		}
	}
}
