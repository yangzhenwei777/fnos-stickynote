package localstore

// gc.go —— v21.203 换代垃圾回收（批次 1-2 / 1-3）。
//
// 为什么需要：审计定罪「失败换代持续占盘」。换代会产两类垃圾，
//
//	① Generation 垃圾：Activate 在 SWITCHING 之前失败会留下半成品代目录（进程内
//	   abort 与崩溃态 Recover 都会删），但**业务级回退**（bootstrap 步骤 7~9.6 失败）
//	   与「post-login rotate 失败」这类历史路径只复位指针、不删新代；叠加用户反复
//	   点「强制全量同步」，一代几十 MB 的 notes.db 可以堆到几十上百 GB。
//	② Staging 垃圾：bootstrap 的 `<snapID>.zip` / `<snapID>.db` / `boot-uploads/`、
//	   portable 的 `portable-in-<id>/`、provision 的 `provision-*.db`，
//	   失败路径只删一部分（且 unpack 的 `notes.db.in` 中途失败会残留）。
//
// 回收纪律（安全优先，宁可不删不可删错）：
//   - 保护集 = {active, last_known_good} ∪ keep 个最近代（按序号倒序）；
//   - 保护集之外才删，且**只删能解析成 g-NNNNNN 的目录**（用户手放的目录不碰）；
//   - 有在途 journal 时，journal 涉及的 NextGen/PrevGen 一律进保护集
//     （Recover 可能正要拿它做回退，GC 抢先删 = 把回退变成丢数据）；
//   - Staging 只删「超过保留期」的文件/目录，保留期默认 24h —— 正在进行的换代
//     会持有 staging 文件（.part/.db/zip），不设保留期就会自伤；
//   - 全部错误**只记不抛**：GC 是维护性动作，绝不能因清理失败阻断启动或换代。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// keepGenerations 保留的「最近代」数量（不含 active/lkg 两个硬保护）。
//
// 取 2 的依据：§16 的底线是「active 与 previous 必须同时在盘」；多留一代是为了
// 让用户在「刚回退完、还没跑成一次完整换代」期间仍有肉眼可回退的中间态。
// 磁盘代价上限 = 2 × 单代体积（典型 5~40MB），可接受。
const keepGenerations = 2

// stagingRetention staging 垃圾保留期。正在进行的换代会写 staging，超过此窗口的
// 才允许删 —— 24h 远长于一次换代的最坏耗时（轮询 90s + 下载 + 迁移）。
const stagingRetention = 24 * time.Hour

// GCGenerations 清理非保护集的 Generation 目录，返回被删的代 id 列表（升序）。
// 语义保证：调用前后 active 与 last_known_good 两代**必然仍在盘**。
func (l Layout) GCGenerations(keep int) ([]string, error) {
	if keep < 0 {
		keep = 0
	}
	ents, err := os.ReadDir(l.GenerationsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// 只认 g-NNNNNN（用户手工目录、非本包命名一律不碰）。
	var gens []string
	nums := map[string]int{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if n, ok := parseGen(e.Name()); ok {
			gens = append(gens, e.Name())
			nums[e.Name()] = n
		}
	}
	if len(gens) == 0 {
		return nil, nil
	}
	sort.Slice(gens, func(i, j int) bool { return nums[gens[i]] < nums[gens[j]] })

	protect := map[string]bool{}
	if a, aerr := l.Active(); aerr == nil && a != "" {
		protect[a] = true
	}
	if k, kerr := l.LastKnownGood(); kerr == nil && k != "" {
		protect[k] = true
	}
	// 在途 journal 的两端代：Recover 可能正要拿 PrevGen 回退、或把 NextGen 完成化。
	if j, jerr := loadJournal(l); jerr == nil && j != nil {
		if j.NextGen != "" {
			protect[j.NextGen] = true
		}
		if j.PrevGen != "" {
			protect[j.PrevGen] = true
		}
	}
	// 最近 keep 个（倒序扫，跳过已在保护集里的不占额）
	keptRecent := 0
	for i := len(gens) - 1; i >= 0; i-- {
		if protect[gens[i]] {
			continue
		}
		if keptRecent >= keep {
			break
		}
		protect[gens[i]] = true
		keptRecent++
	}

	var removed []string
	for _, g := range gens {
		if protect[g] {
			continue
		}
		if err := os.RemoveAll(l.GenDir(g)); err != nil {
			// 单代失败不影响其余：记录并继续（调用方按 len(removed) 判断）。
			_ = l.writeRecoveryNote(fmt.Sprintf("gc: remove generation %s failed: %v", g, err))
			continue
		}
		removed = append(removed, g)
	}
	if len(removed) > 0 {
		_ = l.writeRecoveryNote("gc: removed generations " + joinIDs(removed))
	}
	return removed, nil
}

// GCStaging 清理超过保留期的 staging 文件与目录，返回被删条目数。
// 只删 staging 目录的**直接子项**（一代换代的产物都在这一层），不递归判断内容。
func (l Layout) GCStaging(retention time.Duration) (int, error) {
	if retention <= 0 {
		retention = stagingRetention
	}
	ents, err := os.ReadDir(l.StagingDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-retention)
	n := 0
	for _, e := range ents {
		p := filepath.Join(l.StagingDir(), e.Name())
		var mt time.Time
		if e.IsDir() {
			// 目录取「目录内最新 mtime」做保守判据：只要里面有任何近期写入就整体保留。
			latest, lerr := latestMTime(p)
			if lerr != nil {
				continue
			}
			mt = latest
		} else {
			fi, serr := e.Info()
			if serr != nil {
				continue
			}
			mt = fi.ModTime()
		}
		if mt.After(cutoff) {
			continue
		}
		if rerr := os.RemoveAll(p); rerr != nil {
			continue
		}
		n++
	}
	if n > 0 {
		_ = l.writeRecoveryNote("gc: removed stale staging entries")
	}
	return n, nil
}

// GC 是启动链与换代成功后的组合入口。**永不返回错误**（内部逐项吞掉），
// 调用方可以直接 `defer` 或忽略返回值。
func (l Layout) GC() (gens []string, staging int) {
	if g, err := l.GCGenerations(keepGenerations); err == nil {
		gens = g
	}
	if s, err := l.GCStaging(stagingRetention); err == nil {
		staging = s
	}
	return gens, staging
}

func latestMTime(dir string) (time.Time, error) {
	var latest time.Time
	ents, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, err
	}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().After(latest) {
			latest = fi.ModTime()
		}
	}
	if latest.IsZero() {
		// 空目录：取目录自身 mtime 判据。
		if fi, err := os.Stat(dir); err == nil {
			return fi.ModTime(), nil
		}
	}
	return latest, nil
}

func joinIDs(ids []string) string {
	out := ""
	for i, s := range ids {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
