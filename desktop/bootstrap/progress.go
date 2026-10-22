package bootstrap

import (
	"sync/atomic"
	"time"
)

// 换代阶段常量（v21.218+ 进度可见性 / 2.0.3 P1-3）。
// 值会经 Wails 绑定透给前端显示，**只能追加不能改语义**；中文映射在前端。
const (
	PhaseIdle        = ""            // 无换代进行（进程内初始态）
	PhaseRequest     = "request"     // 请求快照
	PhasePoll        = "poll"        // 等待服务端生成快照（10~60s 主要波动段）
	PhaseDownload    = "download"    // 下载快照
	PhaseVerify      = "verify"      // sha256 校验 + 解包
	PhaseActivate    = "activate"    // 双代激活
	PhasePostprocess = "postprocess" // 附件合并 / 锚与游标 / outbox 搬运 / 身份迁移
	PhaseDone        = "done"        // 完成（前端随即因闸门恢复而收起横幅）
)

// PhaseState 单个阶段的可见状态。
type PhaseState struct {
	Phase string `json:"phase"`
	Since int64  `json:"since_ms"` // UnixMilli，前端据此显示「已等待 Ns」
}

// curPhase 当前阶段（原子指针：写方 = 换代 goroutine 单飞；读方 = 绑定轮询）。
var curPhase atomic.Pointer[PhaseState]

// SetPhase 阶段推进（RunCtx 各边界调用；进程内单飞纪律下无并发写）。
func SetPhase(p string) {
	curPhase.Store(&PhaseState{Phase: p, Since: time.Now().UnixMilli()})
}

// CurrentPhase 读当前阶段；nil = 本进程内无换代记录（idle）。
func CurrentPhase() *PhaseState {
	return curPhase.Load()
}
