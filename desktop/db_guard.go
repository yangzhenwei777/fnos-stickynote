package main

// db_guard.go — P1-1（2026-10-15）：绑定层 DB 读操作超时护栏。
//
// 背景（技术债 P1-1 收口）：desktop 连接池 MaxOpenConns=2，池排队 acquire 无超时
// （database/sql 限制；busy_timeout 只管 SQLite 文件锁、管不到池等待）。217 死锁根治后
// 已无已知"永久持连接"路径，但一旦出现未知的慢查询/持连接，UI 列表仍会以"永久卡"呈现
// （无报错、无超时、无日志）。本护栏把绑定层**只读**调用的观感从"永久卡"降为
// "可报错可重试"——这正是任务 P1-1 的验收语义。
//
// 边界与取舍（登记于《2.1.1-交付轮执行计划》，勿无据翻案）：
//  1. 只包只读绑定：写操作若超时返回"失败"而事务可能实际已提交，用户重试会造成重复数据
//     （CreateNote 非幂等）——写路径维持现状，残余风险由 90s 池饱和看门狗兜底（可观测可取证）。
//  2. AuthStatus/SyncStatus 不包：二者有既有的 degraded 降级返回语义（读不到时返回降级 map
//     而非 error），外包 ErrDBBusy 会绕过该语义；且 v21.218 刚重构过前端 reconcile 链路，稳定优先。
//  3. 超时后底层 goroutine 仍会跑完并归还连接（只读无副作用）；若真发生永久持连接，
//     看门狗 90s goroutine 转储兜底取证。护栏不承诺自愈，承诺"调用侧有界"。
//  4. 绑定方法签名零变更：Wails 契约冻结（签名变更会重新生成 wailsjs 并波及前端），
//     护栏只包在方法体内部。

import (
	"errors"
	"time"
)

// dbReadTimeout 只读绑定的统一超时窗。正常读 <50ms、大库冷读几百 ms；10s 覆盖慢盘
// 且远小于"永久"。包级 var：测试可注入短窗（如 50ms）验证超时分支。
var dbReadTimeout = 10 * time.Second

// ErrDBBusy 超时错误：文案直接可上屏（request.js 把 error.Error() 呈现给用户）。
var ErrDBBusy = errors.New("本地数据库响应超时，请稍后重试；若反复出现，请在设置中导出诊断包")

// dbReadGuard 给一个只读 DB 调用加超时护栏（P1-1）。fn 在独立 goroutine 中执行：
// 正常完成则透传结果；超过 dbReadTimeout 未完成则返回 ErrDBBusy，fn 继续跑完并退场。
func dbReadGuard[T any](fn func() (T, error)) (T, error) {
	type outcome struct {
		val T
		err error
	}
	// 带缓冲 1：超时放弃后 fn 仍可完成投递，goroutine 不因无人接收而泄漏阻塞。
	ch := make(chan outcome, 1)
	go func() {
		v, err := fn()
		ch <- outcome{val: v, err: err}
	}()
	t := time.NewTimer(dbReadTimeout)
	defer t.Stop()
	select {
	case o := <-ch:
		return o.val, o.err
	case <-t.C:
		var zero T
		return zero, ErrDBBusy
	}
}
