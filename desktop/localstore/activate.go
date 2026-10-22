package localstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrInsufficientSpace 激活前空间预检失败（§52/§88：不足即拒绝，绝不产生半成品 Generation）。
var ErrInsufficientSpace = errors.New("insufficient disk space")

const spaceSafetyMargin = 64 << 20 // 64MB 安全边距（§52 Required+Margin 的 Margin）

// Activator Generation 激活器。failAt 为测试故障注入钩子（生产恒 nil）：
// 在对应相位**落盘之前**调用，返回 err 即模拟进程在该点崩溃。
type Activator struct {
	L      Layout
	failAt func(next Phase) error
}

func NewActivator(profileDir string) *Activator {
	return &Activator{L: Layout{ProfileDir: profileDir}}
}

// Activate 把已封闭（sealed，单文件、无 -wal 伴生）的 staging 库激活为新 Generation
// 并原子切换。流程 = 空间预检 → staging 完整性+hash → PREPARED → 落代目录+manifest →
// VERIFIED → 先写 last_known_good(F2/§84) → SWITCHING → active 原子替换(§13) → SEALED。
// manifest 为本代元数据（§9），本包视其为不透明字节。
//
// ⚠ v21.203 契约变更（**调用方必须配合**）：本函数返回 nil 时 journal 停在 **SEALED**，
// **不会自行清 journal**。换代之后调用方还有若干可失败步骤（bootstrap：附件合并/锚与游标/
// outbox 搬运/身份迁移/自检；portable：附件合并/档案重植/outbox 并入），只有这些全部成功后
// 才调 `SealCommitted(gen)` 落 COMMITTED 并清 journal。
//
// 崩溃语义（Recover 收敛）：
//   - 停在 SEALED/SWITCHING 且指针已在 NextGen ⇒ 后置步骤未确认 ⇒ **回退指针到 PrevGen
//     并删除残缺新代**（prev 为空=首装无退路，则保留新代并写 recovery note）；
//   - 停在 COMMITTED ⇒ 后置已确认 ⇒ 仅清 journal，保留新代。
//
// 进程内任一步失败 → abort（指针复位、残缺代目录删除、journal 清理）。
func (a *Activator) Activate(stagingDBPath string, manifest []byte) (string, error) {
	if err := a.L.EnsureDirs(); err != nil {
		return "", err
	}
	fi, err := os.Stat(stagingDBPath)
	if err != nil {
		return "", fmt.Errorf("stat staging: %w", err)
	}
	if free, ferr := freeBytes(a.L.ProfileDir); ferr != nil {
		return "", ferr
	} else if need := uint64(fi.Size())*2 + spaceSafetyMargin; free < need {
		return "", fmt.Errorf("%w: need>=%d free=%d", ErrInsufficientSpace, need, free)
	}
	// staging 封闭库校验 + hash（F1：只对 sealed snapshot 算 hash）。
	if err := IntegrityCheckFile(stagingDBPath); err != nil {
		return "", fmt.Errorf("stage verify: %w", err)
	}
	sha, err := SealSHA256(stagingDBPath)
	if err != nil {
		return "", err
	}
	next, err := a.L.nextGenerationID()
	if err != nil {
		return "", err
	}
	prev, err := a.L.Active()
	if err != nil {
		return "", err
	}
	j := &Journal{ActivationID: randShortID(), NextGen: next, PrevGen: prev, BaseSHA256: sha, path: a.L.JournalFile()}

	// 失败即回滚（返回 nil 前）。用显式收尾函数而非 defer 以便成功返回后关闭回滚。
	done := false
	defer func() {
		if !done {
			a.abort(j)
		}
	}()

	if a.failAt != nil && a.failAt(PhasePrepared) != nil {
		return "", errors.New("injected crash before PREPARED")
	}
	if err := j.advance(PhasePrepared); err != nil {
		return "", err
	}
	// 复制入代目录（0700/0600）并原子落 manifest。
	if err := os.MkdirAll(a.L.GenDir(next), 0700); err != nil {
		return "", err
	}
	if err := copyFileFsync(stagingDBPath, a.L.GenDB(next)); err != nil {
		return "", err
	}
	if err := atomicWriteFile(a.L.GenManifest(next), manifest); err != nil {
		return "", err
	}
	if a.failAt != nil && a.failAt(PhaseVerified) != nil {
		return "", errors.New("injected crash before VERIFIED")
	}
	if err := j.advance(PhaseVerified); err != nil {
		return "", err
	}
	// §F2：lkg 必须在切换**前**固化为当前 active。
	if prev != "" {
		if err := a.L.writePointer(a.L.LKGFile(), prev); err != nil {
			return "", err
		}
	}
	if a.failAt != nil && a.failAt(PhaseSwitching) != nil {
		return "", errors.New("injected crash before SWITCHING")
	}
	if err := j.advance(PhaseSwitching); err != nil {
		return "", err
	}
	// active_generation 原子替换（写全值→读回；只可能是 prev 或 next 两个合法全值）。
	if err := a.L.writePointer(a.L.PointerFile(), next); err != nil {
		return "", err
	}
	// 故障注入点：模拟「指针已切、journal 仍停 SWITCHING」即崩溃 —— 与 SEALED 同属
	// 「后置未确认」窗口，Recover 必须回退而非完成化（v21.203 起此注入点等价 SEALED 崩溃）。
	if a.failAt != nil && a.failAt(PhaseSealed) != nil {
		done = true // 指针已切换成功，进程内不得回滚删除新代；留给 Recover 按 SEALED 收敛
		return "", errors.New("injected crash before SEALED record")
	}
	if err := j.advance(PhaseSealed); err != nil {
		return "", err
	}
	done = true
	return next, nil
}

// SealCommitted 由调用方在**全部换代后置步骤成功后**调用：落 COMMITTED 并清 journal。
// 这是「后置步骤已确认」的唯一权威标记 —— 缺它则下次启动 Recover 按 SEALED 崩溃态回退。
// 幂等：journal 已清（重复调用）时按「指针是否在该代」判成功，不报错。
func (a *Activator) SealCommitted(gen string) error {
	if gen == "" {
		return errors.New("seal: empty generation")
	}
	j, err := loadJournal(a.L)
	if err != nil {
		return err
	}
	if j == nil {
		// 已封相（重复调用）：确认指针在该代即视为成功，绝不误判失败。
		if cur, cerr := a.L.Active(); cerr != nil || cur != gen {
			return fmt.Errorf("seal: journal absent and active=%q want %q", cur, gen)
		}
		return nil
	}
	if j.NextGen != gen {
		return fmt.Errorf("seal: journal next=%q want %q", j.NextGen, gen)
	}
	if j.Phase != PhaseSealed && j.Phase != PhaseCommitted {
		return fmt.Errorf("seal: journal phase %s not sealable", j.Phase)
	}
	if err := j.advance(PhaseCommitted); err != nil {
		return err
	}
	if err := clearJournal(a.L); err != nil {
		return err
	}
	return a.L.writeRecoveryNote(fmt.Sprintf("activated %s base_sha=%s", gen, j.BaseSHA256[:12]))
}

// abort 进程内失败回滚：指针若已指向新代则复位 prev（首次激活则清空），删半成品代目录。
func (a *Activator) abort(j *Journal) {
	if j == nil || j.Phase == "" {
		return
	}
	if cur, _ := a.L.Active(); cur == j.NextGen {
		_ = a.L.writePointer(a.L.PointerFile(), j.PrevGen)
	}
	_ = os.RemoveAll(a.L.GenDir(j.NextGen))
	_ = clearJournal(a.L)
	_ = a.L.writeRecoveryNote(fmt.Sprintf("activation %s aborted at %s", j.ActivationID, j.Phase))
}

// Recover 启动期崩溃收敛（必须在打开任何 Generation 库之前调用）。
// 判据 = journal 相位 × 实际指针 对账；结果恒满足：active 指向**存在且可开**的代，
// 或安全回落到 last_known_good；两者皆不可用才上抛（人工/bootstrap 面）。
func Recover(profileDir string) (string, error) {
	l := Layout{ProfileDir: profileDir}
	if err := l.EnsureDirs(); err != nil {
		return "", err
	}
	j, err := loadJournal(l)
	if err != nil {
		return "", err
	}
	if j == nil {
		// 无在途激活：指针自洽体检。
		active, err := l.Active()
		if err != nil {
			return "", err
		}
		if active == "" {
			return "clean:first-run", nil
		}
		if genExists(l.GenerationsDir(), active) {
			return "clean", nil
		}
		lkg, err := l.LastKnownGood()
		if err == nil && genExists(l.GenerationsDir(), lkg) {
			if werr := l.writePointer(l.PointerFile(), lkg); werr != nil {
				return "", werr
			}
			_ = l.writeRecoveryNote("pointer fallback to last_known_good " + lkg)
			return "fallback:" + lkg, nil
		}
		return "", fmt.Errorf("active generation %q missing and no usable last_known_good (manual bootstrap needed)", active)
	}

	active, err := l.Active()
	if err != nil {
		return "", err
	}
	switch j.Phase {
	case PhasePrepared, PhaseVerified:
		// 指针不可能已指向 NextGen（切换发生在 SWITCHING 之后）；真出现也按「未确认」回退。
		if active == j.NextGen {
			if rerr := revertUnsealed(l, j); rerr != nil {
				return "", rerr
			}
			return "reverted:" + j.NextGen, nil
		}
		// 切换未发生：删半成品、清 journal，状态回到 prev。
		if err := os.RemoveAll(l.GenDir(j.NextGen)); err != nil {
			return "", err
		}
		if err := clearJournal(l); err != nil {
			return "", err
		}
		_ = l.writeRecoveryNote("aborted activation " + j.ActivationID)
		return "aborted:" + j.NextGen, nil
	case PhaseSwitching, PhaseSealed:
		// ⭐ v21.203 语义收口：SWITCHING/SEALED 两相位的共同前提是
		//「指针可能已切，但**换代后置步骤一定未确认完成**」。因此指针落在 NextGen 时
		// 一律**回退**（绝不做完成化）——完成化 = 把内容残缺、且可能缺 client_profile
		// 的新代当成健康代交给用户，这正是 v21.200~202 实机「笔记消失 + 显示未登录」
		// 的机制。v21.162 及更早在此处做「完成化」，是审计定罪的最重一处。
		if active == j.NextGen {
			if rerr := revertUnsealed(l, j); rerr != nil {
				return "", rerr
			}
			return "reverted:" + j.NextGen, nil
		}
		// 指针尚未切换：删半成品、清 journal。
		if err := os.RemoveAll(l.GenDir(j.NextGen)); err != nil {
			return "", err
		}
		if err := clearJournal(l); err != nil {
			return "", err
		}
		_ = l.writeRecoveryNote("aborted activation " + j.ActivationID)
		return "aborted:" + j.NextGen, nil
	case PhaseCommitted:
		if !genExists(l.GenerationsDir(), j.NextGen) {
			return "", fmt.Errorf("journal COMMITTED but generation %q missing", j.NextGen)
		}
		if err := clearJournal(l); err != nil {
			return "", err
		}
		_ = l.writeRecoveryNote("finalized activation " + j.ActivationID)
		return "finalized:" + j.NextGen, nil
	default:
		return "", fmt.Errorf("unknown activation journal phase %q", j.Phase)
	}
}

// revertUnsealed 收敛「指针已切到新代但后置步骤未确认」的崩溃态：
// ① 指针复位回 PrevGen；② 删除残缺新代目录（内容不完整，留着只会被下次 GC 或人工误用）；
// ③ lkg 修正 = PrevGen（Activate 切换前已把 lkg 写成 prev，回退后 active==lkg 自洽）；
// ④ 清 journal。
//
// prev 为空（首装无退路）时：保留新代（回退=退到空库，等于丢全部数据），
// 改清 journal + 写 recovery note 明示「未封相但已保留」，把处置交回用户/bootstrap 面。
func revertUnsealed(l Layout, j *Journal) error {
	if j.PrevGen == "" {
		if !genExists(l.GenerationsDir(), j.NextGen) {
			// 既无退路又无可用新代：只能上抛，交给 bootstrap 面。
			_ = clearJournal(l)
			return fmt.Errorf("activation %s unsealed at first install and generation %q missing (manual bootstrap needed)",
				j.ActivationID, j.NextGen)
		}
		if err := clearJournal(l); err != nil {
			return err
		}
		return l.writeRecoveryNote(fmt.Sprintf(
			"⚠ activation %s sealed-phase crash at first install; generation %s kept (no previous to revert to)",
			j.ActivationID, j.NextGen))
	}
	if err := l.RollbackActive(j.PrevGen); err != nil {
		return fmt.Errorf("revert unsealed activation to %s: %w", j.PrevGen, err)
	}
	if err := os.RemoveAll(l.GenDir(j.NextGen)); err != nil {
		return err
	}
	if err := clearJournal(l); err != nil {
		return err
	}
	return l.writeRecoveryNote(fmt.Sprintf(
		"reverted unsealed activation %s (%s): active restored to %s, incomplete generation removed",
		j.ActivationID, j.Phase, j.PrevGen))
}

func randShortID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "fixed"
	}
	return hex.EncodeToString(b)
}

func copyFileFsync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
