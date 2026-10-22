// 批次 4 · 刀 1：localstore 启动链接入（离线方案 v20 §80~84 / P0-B 集成说明 README:58-62）。
//
// 启动序列 = Recover(profileDir) → ReadPointer → OpenGenDB(gen) → database.SetDB 换全局。
// 首装/旧骨架库（profiles/localdev/notepad.db 直开形态）→ 一次性迁移：
//
//	服务端栈 Init 老库补迁移（顺带产 schema）→ backup.SnapshotTo 出 sealed staging
//	→ Activator.Activate 落 generations/g-NNNNNN + 原子指针（lkg 先于切换纪律由 Activate 承担）
//	→ 丢弃临时句柄 → 统一走 OpenGenDB 代开路径。
//
// uploads 目录保持在 profileDir 下不随迁（附件目录随 generation 切换属 P5 Bootstrap 面）。
//
// 本文件函数不与 wails.Run 耦合，测试可独立驱动 bootLocalstore。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"stickynote/backup"
	"stickynote/database"

	"stickynote-desktop/localauth"
	"stickynote-desktop/localstore"
	"stickynote-desktop/synclocal"
)

// localstoreAppVersion 本地库 schema 基线标识。⚠ 沿用骨架期钉值（≥1.0.0 才执行建表迁移，
// database/upgrade.go 按二进制版本过滤）；desktop 独立 App Version 体系按 §155 后续落地，
// 届时此处切换为 VERSION 注入值——行为兼容增量为纯增量。
const localstoreAppVersion = "1.0.174"

// bootLocalstore 执行桌面启动序列并接管 database 全局句柄，返回激活的 generation id（调试/诊断面）。
// profileDir = profiles/<profile> 根（localDataDir 同级语义）。失败一律上抛（fail-closed，禁半态起窗）。
func bootLocalstore(profileDir string) (string, error) {
	state, err := localstore.Recover(profileDir)
	if err != nil {
		return "", fmt.Errorf("localstore recover: %w", err)
	}
	l := localstore.Layout{ProfileDir: profileDir}

	// 首代 provision：指针空（clean:first-run / 人工清理后）且存在旧骨架库 → 快照迁移；
	// 无旧库 → 全新生成。旧骨架库文件保留原名不改（灾备自清理由用户/后续版本处理）。
	// fallback:/completed:/… 状态已由 Recover 收敛指针，统一按指针开库，无需分支。
	_ = state
	if active, perr := l.Active(); perr != nil {
		return "", perr
	} else if active == "" {
		if _, err := provisionFirstGeneration(profileDir, l); err != nil {
			return "", err
		}
	}

	active, err := l.Active()
	if err != nil {
		return "", err
	}
	// v21.203：启动期 GC —— 回收历史失败换代留下的残缺代与过期 staging。
	// 必须在 Recover 之后（指针已收敛，保护集可信）、且在 OpenGenDB 之前完成，
	// 避免「开着一个即将被 GC 判为垃圾的代」这种竞态读。GC 内部只记不抛。
	if removed, staged := l.GC(); len(removed) > 0 || staged > 0 {
		log.Printf("[localstore] startup gc: removed generations=%v stale_staging=%d", removed, staged)
	}
	if !genHasDB(l, active) {
		return "", fmt.Errorf("pointer %q has no openable generation db after recover/provision", active)
	}
	genDB, err := localstore.OpenGenDB(l.GenDB(active))
	if err != nil {
		return "", fmt.Errorf("open generation %s: %w", active, err)
	}
	if old := database.SetDB(genDB); old != nil {
		_ = old.Close() // 换入完成（model/app 后续读经 GetDB/直引，与 v21.100 竞争纪律一致）
	}
	if err := ensureLocalUser(); err != nil {
		return "", fmt.Errorf("seed local user: %w", err)
	}
	// 本地记账表无条件幂等就位（走查实锤：断网冷启动若等登录路径建表，入队静默失败、
	// 离线编辑漏账）——outbox/profile 属"启动即有"基建，不挂登录时机。
	if err := synclocal.EnsureTables(genDB); err != nil {
		return "", fmt.Errorf("synclocal tables: %w", err)
	}
	if err := localauth.EnsureTables(genDB); err != nil {
		return "", fmt.Errorf("client_profile: %w", err)
	}
	// ⭐ v21.212：读回持久化的换代代数（见 sync_bindings.go genEpoch 的定罪）。
	// 必须在此（EnsureTables 之后、返回之前）：此时 sync_state 已就位，且
	// database.DB 已指向新代 —— 前端首轮 SyncScheduleStatus 拿到的 gen_epoch
	// 才是「跨进程真实代数」而非恒0，从而「换代过」这件事不会在重启后丢失。
	loadGenEpoch(genDB)
	return active, nil
}

// provisionFirstGeneration 返回新代 id。两条来源：旧骨架库快照 / 服务端栈新建空 schema。
func provisionFirstGeneration(profileDir string, l localstore.Layout) (string, error) {
	legacy := filepath.Join(profileDir, "notepad.db")
	if free, ferr := localstore.FreeBytes(profileDir); ferr == nil && free < minProvisionBytes {
		return "", fmt.Errorf("provision disk precheck: %w need>=%d free=%d", localstore.ErrInsufficientSpace, minProvisionBytes, free)
	}

	var staging string
	if _, err := os.Stat(legacy); err == nil {
		// 旧库在用形态：服务端栈补到当前 schema（迁移幂等，含 sync 表），随后 VACUUM INTO 出 sealed 快照。
		if err := database.Init(legacy, localstoreAppVersion); err != nil {
			return "", fmt.Errorf("legacy db migrate: %w", err)
		}
		if db := database.GetDB(); db != nil {
			_ = db.Close()
		}
		if err := l.EnsureDirs(); err != nil {
			return "", err
		}
		staging = filepath.Join(l.StagingDir(), "provision-legacy.db")
		if err := backup.SnapshotTo(staging, legacy); err != nil {
			return "", fmt.Errorf("legacy snapshot: %w", err)
		}
	} else if os.IsNotExist(err) {
		gen0 := filepath.Join(l.GenerationsDir(), "provision", "notes.db")
		if err := os.MkdirAll(filepath.Dir(gen0), 0700); err != nil {
			return "", err
		}
		if err := database.Init(gen0, localstoreAppVersion); err != nil {
			return "", fmt.Errorf("fresh schema init: %w", err)
		}
		if db := database.GetDB(); db != nil {
			_ = db.Close()
		}
		// ⚠ 不得把 gen0 直接当 staging（2026-09-23 真机实锤）：Init 建表落在 WAL，
		// 主文件可能是 4096B 空壳，Activate 只拷主文件 → 激活出 0 表库 →
		// ensureLocalUser "no such table: users" → 双击秒退（首装必踩）。
		// 与 legacy 路径对称：SnapshotTo 产出含 WAL 内容的 sealed 快照再 Activate。
		staging = filepath.Join(l.StagingDir(), "provision-fresh.db")
		if err := l.EnsureDirs(); err != nil {
			return "", err
		}
		if err := backup.SnapshotTo(staging, gen0); err != nil {
			return "", fmt.Errorf("fresh snapshot: %w", err)
		}
	} else {
		return "", fmt.Errorf("stat legacy db: %w", err)
	}

	manifest, merr := json.Marshal(map[string]any{
		"provenance": "desktop-provision-v1",
		"legacy":     func() bool { _, e := os.Stat(legacy); return e == nil }(),
	})
	if merr != nil {
		return "", merr
	}
	act := localstore.NewActivator(profileDir)
	gen, err := act.Activate(staging, manifest)
	if err != nil {
		return "", fmt.Errorf("provision activate: %w", err)
	}
	// v21.203 封相：首装 provision 的后置步骤在 bootLocalstore 后续段（EnsureTables/
	// ensureLocalUser 幂等）完成，但 Activate 返回即视为「本代可用」—— 首装无 prev，
	// 封相失败也不存在回退路径，故此处封相失败仅记日志不阻断（fail-open 是首装专属例外，
	// 与「有 prev 时必回退」的纪律不冲突：回退目标压根不存在）。
	if err := act.SealCommitted(gen); err != nil {
		log.Printf("[localstore] provision seal failed (first install, no rollback target): %v", err)
	}
	return gen, nil
}

func genHasDB(l localstore.Layout, gen string) bool {
	if gen == "" {
		return false
	}
	st, err := os.Stat(l.GenDB(gen))
	return err == nil && !st.IsDir() && st.Size() > 0
}

// minProvisionBytes 首代 provision 最低磁盘余量门槛（对齐 localstore 空间预检量级感受值，
// 真正强判在 Activate 内按 staging×2+margin 执行，这里只挡明显满盘）。
const minProvisionBytes = 128 << 20
