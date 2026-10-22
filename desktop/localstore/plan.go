// Package localstore 桌面本地库纪律（离线方案 v20-Project P0-B，§80~84/§93~94）。
//
// 覆盖四件事，全部可独立测试、崩溃可恢复：
//  1. 连接级 PRAGMA（§93/§94）：WAL + synchronous=FULL + foreign_keys + busy_timeout +
//     _txlock=immediate，且**每条物理连接建立时回读验证**——不许只在首建时执行一次；
//  2. Generation 目录 + active_generation 原子指针（§80/§13）：tmp→fsync→rename→读回，
//     Windows rename 走 MOVEFILE_REPLACE_EXISTING，禁止"删旧写新"；
//  3. Activation Journal（§12/§84）：PREPARED→VERIFIED→SWITCHING→COMMITTED 落盘推进，
//     last_known_good 在切换**前**写入；崩溃后 Recover 依指针与 journal 对账收敛；
//  4. sealed snapshot（§10）：复用服务端 backup.SnapshotTo（VACUUM INTO 一致性快照），
//     hash 只对封闭快照算，绝不对活库算（F1）。
//
// 磁盘预检（§52/§88）在激活前执行：不足即拒绝，不产生半成品 Generation。
package localstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ---- 目录布局（§80） ----

type Layout struct{ ProfileDir string }

func (l Layout) RuntimeDir() string     { return filepath.Join(l.ProfileDir, "runtime") }
func (l Layout) GenerationsDir() string { return filepath.Join(l.ProfileDir, "generations") }
func (l Layout) StagingDir() string     { return filepath.Join(l.ProfileDir, "staging") }
func (l Layout) GenDir(gen string) string {
	return filepath.Join(l.GenerationsDir(), gen)
}
func (l Layout) GenDB(gen string) string { return filepath.Join(l.GenDir(gen), "notes.db") }
func (l Layout) GenManifest(gen string) string {
	return filepath.Join(l.GenDir(gen), "manifest.json")
}
func (l Layout) PointerFile() string  { return filepath.Join(l.RuntimeDir(), "active_generation") }
func (l Layout) LKGFile() string      { return filepath.Join(l.RuntimeDir(), "last_known_good") }
func (l Layout) JournalFile() string  { return filepath.Join(l.RuntimeDir(), "activation.journal") }
func (l Layout) RecoveryFile() string { return filepath.Join(l.RuntimeDir(), "recovery.state") }

// EnsureDirs 幂等建出骨架目录（0700：本地库纪律对齐 fnOS 侧权限口径）。
func (l Layout) EnsureDirs() error {
	for _, d := range []string{l.RuntimeDir(), l.GenerationsDir(), l.StagingDir()} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

// ---- 指针与读回（§13 原子切换三层保护的第一层） ----

// atomicWriteFile：完整写 → Sync(=FlushFileBuffers) → rename(REPLACE_EXISTING) → 读回验证。
func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open tmp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("flush %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("atomic replace %s: %w", path, err)
	}
	back, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read-back %s: %w", path, err)
	}
	if string(back) != string(data) {
		return fmt.Errorf("read-back mismatch %s", path)
	}
	return nil
}

// ReadPointer 读指针文件；不存在=空串（首装前的合法空态）。
func (l Layout) ReadPointer(file string) (string, error) {
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// FreeBytes 磁盘可用字节（导出面：provision/预检调用方用；内部 Activate 走同名非导出实现）。
func FreeBytes(path string) (uint64, error) { return freeBytes(path) }

func (l Layout) Active() (string, error)        { return l.ReadPointer(l.PointerFile()) }
func (l Layout) LastKnownGood() (string, error) { return l.ReadPointer(l.LKGFile()) }

func (l Layout) writePointer(file, value string) error {
	return atomicWriteFile(file, []byte(value+"\n"))
}

// RollbackActive 把 active_generation 指针复位回指定代（v21.200 换代原子性收口）。
//
// 用途：Bootstrap 换代时 `Activate` 之后仍有附件合并/锚设置/carry/身份迁移/自检
// 五个可失败步骤，任一失败都必须**回退到上一代**，否则用户重启后读到的是「指针已
// 切换但内容残缺」的新代（实测症状：已同步笔记全没了 + 显示未登录）。
//
// 为什么不用 journal/Recover 兜：Recover 收敛的是「指针已切、journal 未推进」的
// **崩溃态**；这里是**业务级失败**，journal 视角下已 SEALED，回退由调用方负责。
//
// 幂等：cur == prev 时直接返回 nil。prev 为空表示首装无旧代（退无可退），
// 调用方需自行判断 —— 本函数此时返回 nil 且**不改指针**。
func (l Layout) RollbackActive(prev string) error {
	if prev == "" {
		return nil
	}
	cur, err := l.Active()
	if err != nil {
		return err
	}
	if cur == prev {
		return nil
	}
	if _, serr := os.Stat(l.GenDB(prev)); serr != nil {
		return fmt.Errorf("rollback target %s 不可用: %w", prev, serr)
	}
	if err := l.writePointer(l.PointerFile(), prev); err != nil {
		return err
	}
	// v21.203：回退后 active 即 lkg，固化 lkg=prev 使「active 与 lkg 同代」成为显式不变量
	// （GC 以 lkg 为保护集；不变量破裂时 GC 会误删唯一退路）。
	if lkg, lerr := l.LastKnownGood(); lerr != nil || lkg != prev {
		if werr := l.writePointer(l.LKGFile(), prev); werr != nil {
			return werr
		}
	}
	_ = l.writeRecoveryNote(fmt.Sprintf("rollback to %s (v21.200)", prev))
	return nil
}

// RollbackActiveDiscard = RollbackActive + 删除残缺新代目录（v21.203）。
//
// 为什么不复用纯 RollbackActive：残缺新代留着会有三个具体危害 ——
// ① 诊断面/用户手工打开时看到的是一份「有指针历史、内容不完整」的库；
// ② GCGenerations 的 keep-N 可能保留它，反复换代后白占磁盘（快照库通常几十 MB 起）；
// ③ 下次换代 nextGenerationID 取「最大号+1」，残缺代把序号顶高，割裂代际可读性。
//
// discard 为空 = 只回退指针（保留新代，兼容旧调用）。prev 为空（首装无退路）时**只删不指**：
// 无退路下删新代等于丢全部数据，宁可保留并靠 SealCommitted 语义收敛。
func (l Layout) RollbackActiveDiscard(prev, discard string) error {
	if prev == "" {
		return nil
	}
	if err := l.RollbackActive(prev); err != nil {
		return err
	}
	if discard == "" || discard == prev {
		return nil
	}
	if cur, cerr := l.Active(); cerr == nil && cur == discard {
		return fmt.Errorf("rollback discard %s refused: still active", discard)
	}
	if err := os.RemoveAll(l.GenDir(discard)); err != nil {
		return err
	}
	_ = l.writeRecoveryNote(fmt.Sprintf("discarded incomplete generation %s", discard))
	return nil
}

// ---- Generation 编号 ----

// nextGenerationID 扫描 generations/ 下 g-NNNNNN 取最大 +1（§80 命名）。
func (l Layout) nextGenerationID() (string, error) {
	ents, err := os.ReadDir(l.GenerationsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return "g-000001", nil
		}
		return "", err
	}
	var nums []int
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if n, ok := parseGen(e.Name()); ok {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	next := 1
	if len(nums) > 0 {
		next = nums[len(nums)-1] + 1
	}
	return fmt.Sprintf("g-%06d", next), nil
}

func parseGen(name string) (int, bool) {
	if !strings.HasPrefix(name, "g-") || len(name) != 8 {
		return 0, false
	}
	n, err := strconv.Atoi(name[2:])
	if err != nil {
		return 0, false
	}
	return n, true
}

func genExists(generationsDir, gen string) bool {
	if gen == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(generationsDir, gen, "notes.db"))
	return err == nil && !st.IsDir() && st.Size() > 0
}

// SealSHA256 对**已封闭的快照文件**算 hash（F1：活库禁算实时 hash）。流式，常数内存。
func SealSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeRecoveryNote 数据中心可见的最近一次恢复收敛说明（非权威状态，仅诊断）。
func (l Layout) writeRecoveryNote(note string) error {
	return atomicWriteFile(l.RecoveryFile(), []byte(note+"\n"))
}
