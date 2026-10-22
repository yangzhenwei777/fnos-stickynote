package localstore

import (
	"encoding/json"
	"fmt"
	"os"
)

// Phase 激活事务相位（v20 §12 Activation Journal；每个不可逆动作之前落盘推进）。
type Phase string

const (
	PhasePrepared  Phase = "PREPARED"  // 已规划（staging 校验通过，Generation id 已分配）
	PhaseVerified  Phase = "VERIFIED"  // 目标 Generation 文件+manifest 落定并通过完整性
	PhaseSwitching Phase = "SWITCHING" // lkg 已写，指针替换即将发生（此后指针只会是 prev 或 next 全值）
	// PhaseSealed v21.203 新增：指针已切到新代，但**换代后置步骤尚未确认完成**
	// （bootstrap 侧：锚/游标/outbox 搬运/身份迁移/自检；portable 侧：附件合并/
	// 档案重植/outbox 并入）。这是 v21.200~202「笔记消失 + 显示未登录」事故的
	// 崩溃态根因：Activate 内部就把 journal 清了，进程在窗口期被杀时 defer 不执行，
	// 下次启动 Recover 看到的是「无 journal + 指针指向残缺新代」= 误判 clean。
	//
	// 纪律：**只有调用方确认全部后置步骤成功后调 SealCommitted 落 COMMITTED 并清 journal**。
	PhaseSealed    Phase = "SEALED"    // 指针已切、后置未确认（崩溃 ⇒ 回退 prev + 删残缺新代）
	PhaseCommitted Phase = "COMMITTED" // 指针已切且后置步骤已确认完成
)

// Journal 激活事务落盘记录（单实例串行 ⇒ 固定文件名）。
type Journal struct {
	ActivationID string `json:"activation_id"`
	NextGen      string `json:"next_gen"`
	PrevGen      string `json:"prev_gen"` // 空 = 首次激活（无回退代）
	BaseSHA256   string `json:"base_snapshot_sha256"`
	Phase        Phase  `json:"phase"`
	path         string `json:"-"`
}

func loadJournal(l Layout) (*Journal, error) {
	b, err := os.ReadFile(l.JournalFile())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("activation journal corrupt: %w", err)
	}
	j.path = l.JournalFile()
	return &j, nil
}

// write 原子落盘并推进相位（tmp→fsync→rename→读回）。
func (j *Journal) advance(p Phase) error {
	j.Phase = p
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(j.path, b)
}

func clearJournal(l Layout) error {
	if err := os.Remove(l.JournalFile()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
