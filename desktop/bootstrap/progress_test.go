package bootstrap

import (
	"testing"
	"time"
)

// TestPhaseLifecycle 阶段状态机读写与时间戳（2.0.3 · P1-3）。
// RunCtx 插桩的顺序正确性由既有换代集成用例间接覆盖（每个阶段 SetPhase 均在
// 对应阻塞点之前——阶段乱序会让前端显示回跳，属显示层问题，不破坏同步语义）。
func TestPhaseLifecycle(t *testing.T) {
	// 同包集成用例（真实换代）会残留 done 阶段——先重置为「本进程无换代」初始态再验契约。
	curPhase.Store(nil)
	if got := CurrentPhase(); got != nil {
		t.Fatalf("初始态应为 nil，got %+v", got)
	}

	SetPhase(PhaseRequest)
	p := CurrentPhase()
	if p == nil || p.Phase != PhaseRequest {
		t.Fatalf("request 阶段读回不符: %+v", p)
	}
	if p.Since <= 0 || p.Since > time.Now().UnixMilli()+5 {
		t.Fatalf("Since 应为当前 UnixMilli: %d", p.Since)
	}

	// 推进：后写覆盖前写
	time.Sleep(2 * time.Millisecond)
	SetPhase(PhasePoll)
	p2 := CurrentPhase()
	if p2.Phase != PhasePoll || p2.Since < p.Since {
		t.Fatalf("阶段推进异常: %+v -> %+v", p, p2)
	}

	// done 收尾
	SetPhase(PhaseDone)
	if got := CurrentPhase(); got.Phase != PhaseDone {
		t.Fatalf("done 阶段读回不符: %+v", got)
	}
}

// TestPhaseValuesStable 阶段值契约：前端按值映射中文文案，**只能追加不能改名**。
func TestPhaseValuesStable(t *testing.T) {
	want := map[string]bool{
		PhaseRequest: true, PhasePoll: true, PhaseDownload: true, PhaseVerify: true,
		PhaseActivate: true, PhasePostprocess: true, PhaseDone: true,
	}
	if len(want) != 7 {
		t.Fatalf("阶段数量变更须同步前端映射与交付说明: %d", len(want))
	}
	if PhaseIdle != "" {
		t.Fatalf("PhaseIdle 必须为空串（绑定返回 null 语义）")
	}
}
