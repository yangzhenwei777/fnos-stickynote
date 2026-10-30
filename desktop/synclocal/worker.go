package synclocal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"stickynote/htmltext"
	"stickynote/model"
	"stickynote/syncspec"
)

// worker.go —— 单 Sync Worker（§35：所有同步请求进同一队列；"立即同步"=唤醒本 Worker，
// 不是第二个并发体）。一次 RunOnce = state 校验 → pull 应用 → push 排空。
//
// 内嵌决策：
//   - §74/§92 state 锚：epoch/instance 变化 → SERVER_EPOCH_CHANGED 停止普通 Push，Outbox 原样保留（红线 10）；
//   - ADR-001 pull 撞脏实体 → sync_conflicts + 脏链转 CONFLICT，cursor 照常推进（同事务）；
//   - §34 每条 change 的"应用 + cursor"同一事务；
//   - ADR-002 批构造：一批一实体一 op（PENDING 链合并取最终态），ACK 后 rebase 后续 base（§50）。

var (
	ErrEpochChanged = errors.New("SERVER_EPOCH_CHANGED")
	ErrAuthRevoked  = errors.New("AUTH_REVOKED")
	// ErrPayloadGone §32 P1-b：blob 载荷在服务端不可得（引用失效/文件缺失）。
	// 客户端按"重试 + 逃逸计数"处置：短暂故障退避重试收敛；连续多轮仍缺失则
	// 跳过该 change 并 WARN 留痕（实体随后续 Bootstrap/RESYNC 全量自愈）。
	ErrPayloadGone = errors.New("PAYLOAD_GONE")
	// ErrBadCredentials 仅用于登录换票：服务端 401 打在 /api/auth/login 上 = 用户名或密码错误
	// （实测 handler/auth.go 账密错回 401、空值回 400、锁定回 429）。它和 ErrAuthRevoked 必须
	// 分开：前者"再试一次/改一下密码"就好，后者"会话作废必须重新登录"，混在一起 UI 只能瞎猜。
	ErrBadCredentials = errors.New("BAD_CREDENTIALS")
	// ErrTOTPRequired 登录通道专用（v21.151）：服务端 200 {totp_required}（账号已开启两步验证）——
	// 需前端收集动态码走第二步 VerifyTOTP 换正式会话。它不是失败终态，绝不能与
	// ErrBadCredentials/ErrAuthRevoked 混用：旧实现把该响应当成功（空票+nil）→ 下游 401 →
	// UI 误报「登录会话已失效（被吊销）」。
	ErrTOTPRequired = errors.New("TOTP_REQUIRED")
	ErrResyncNeeded = errors.New("RESYNC_REQUIRED")
)

type Worker struct {
	db     *sql.DB
	cl     *Client
	device string
	// AttachDir 本地附件目录（sync 上传预传 / 下载落位；空 = 附件通道关闭）。
	AttachDir string
	// ⭐ v21.212：让路检查闭包（换代窗口要求在途轮立即让路时为 true）。
	// 用 atomic.Pointer 而非裸函数字段：SetYieldCheck 由调度层在每轮开始前调用，
	// RunOnce 在分页循环里读 —— 存在并发读写，裸字段是数据竞争。
	yieldCheck atomic.Pointer[func() bool]
	// payloadMiss §32 P1-b：blob 载荷缺失逃逸计数（worker_payload.go）。
	payloadMiss payloadMissingTracker
}

func NewWorker(db *sql.DB, cl *Client) *Worker { return &Worker{db: db, cl: cl, device: cl.Device} }

// SetToken 换票成功后为在位 worker 补票（设备会话早期挂载时 token 为空，
// ensureAccess 换到 access 后经此注入；空串拒绝——不许把在位票清空成假登录态）。
func (w *Worker) SetToken(token string) { w.cl.SetToken(token) }

// ClearToken 主动丢弃内存票（v21.203 新增，会话降级专用）。
//
// 为什么不复用 SetToken("")：SetToken 的"空串拒绝"是一条**有意的护栏**
// （防止某个换票失败分支顺手把票抹掉，制造"看起来在位其实无票"的假态）。
// 降级是**唯一**需要真清票的场景，所以另立一个名字明确的 API，
// 让"清票"这件事在代码里永远可见、可 grep，不靠参数值区分意图。
func (w *Worker) ClearToken() {
	if w == nil || w.cl == nil {
		return
	}
	w.cl.ClearToken()
}

// HasToken 当前是否已持有效 access（挂载但未换票时为 false）。
func (w *Worker) HasToken() bool { return w != nil && w.cl != nil && w.cl.AccessToken() != "" }

// RebindDB 换代后重绑库句柄（BootstrapNow 专用；旧句柄关闭由调用侧负责）。
func (w *Worker) RebindDB(db *sql.DB) { w.db = db }

// AccessToken 只读取当前票（BootstrapNow 复用会话票，不复制凭据面）。
func (w *Worker) AccessToken() string {
	if w == nil || w.cl == nil {
		return ""
	}
	return w.cl.AccessToken()
}

// State 代理服务端 state 探测（绑定层用）。
func (w *Worker) State() (StateResp, error) { return w.cl.State() }

// ⭐ v21.212：让路检查点注入点（nil = 不启用让路，保持既有语义）。
//
// 用途见 Scheduler.YieldInFlight 的定罪：换代窗口需要让**在途轮**在分页循环边界
// 立即 return，而不是等它跑完整个网络往返。211 修法"摘 worker 指针让在途轮撞空"
// 已被实机证伪（`w := workerPtr()` 早已把 worker 取进局部变量，摘全局指针无效）。
//
// 装配：调度层在每轮开始前 `SetYieldCheck(...)` 注入「让路信号非阻塞检查」闭包。
var yieldProbe atomic.Bool // worker 上是否已装配让路检查（诊断用）

// SetYieldCheck 装配让路检查（每轮开始前由调度层调用）。
func (w *Worker) SetYieldCheck(fn func() bool) {
	if w == nil {
		return
	}
	w.yieldCheck.Store(&fn)
	yieldProbe.Store(true)
}

// shouldYield 询问是否应让路（无装配/无信号 ⇒ false，绝不影响既有轮次）。
func (w *Worker) shouldYield() bool {
	if w == nil {
		return false
	}
	p := w.yieldCheck.Load()
	if p == nil || *p == nil {
		return false
	}
	return (*p)()
}

type Summary struct {
	Applied          int `json:"applied"`
	Conflicts        int `json:"conflicts"`
	Pushed           int `json:"pushed"`
	Acked            int `json:"acked"`
	Downloaded       int `json:"downloaded"`
	Repaired         int `json:"attachments_repaired"`
	AttachmentFailed int `json:"attachments_failed"`
}

func (w *Worker) RunOnce(ctx context.Context) (Summary, error) {
	var s Summary
	// ⭐ v21.211（210 实机：关机 72 秒不退出）：ctx 归一 + 三个传输调用全部走
	// 可取消通道。此前 State/Pull/Push 都是无 ctx 的旧方法，ctx 只在 Pull 循环
	// **开头**被检查 ⇒ 单次 HTTP 往返中途完全无视取消（state 15s / bulk 10min），
	// 关机只能干等超时。现在取消是**秒级**的。
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := w.cl.StateCtx(ctx)
	if err != nil {
		return s, err
	}
	// §74/§92 代际与实例锚校验（首跑无存档则建立基线）。
	if saved, _ := getState(w.db, "server_epoch"); saved != "" {
		savedN, _ := strconv.ParseInt(saved, 10, 64)
		inst, _ := getState(w.db, "server_instance_id")
		if savedN != st.ServerEpoch || (inst != "" && st.ServerInstanceID != "" && inst != st.ServerInstanceID) {
			return s, ErrEpochChanged
		}
	}
	if err := setState(w.db, "server_epoch", strconv.FormatInt(st.ServerEpoch, 10)); err != nil {
		return s, err
	}
	if err := setState(w.db, "server_instance_id", st.ServerInstanceID); err != nil {
		return s, err
	}

	cursorStr, _ := getState(w.db, "cursor")
	cursor, _ := strconv.ParseInt(cursorStr, 10, 64)
	if cursorStr != "" && cursor < st.MinAvailableChangeSeq {
		return s, ErrResyncNeeded // §90：Bootstrap（P5）面；Outbox 保护在 P5 实现
	}

	// ---- Pull（fixed watermark = 本轮 state 的 current_change_seq，§32）----
	for {
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		default:
		}
		// ⭐ v21.212：让路检查点。换代窗口需要本轮**立即**停在分页边界，
		// 而不是把整轮 changes 拉完（211 实机：摘指针方案下这一轮跑了 60s 未收敛）。
		// 让路时返回的是**普通成功**而非错误：换代是计划内动作，不是失败，
		// 不能污染退避计数，也不能在 OnRound 里触发 401 降级。
		if w.shouldYield() {
			return s, nil
		}
		p, err := w.cl.PullCtx(ctx, cursor, st.CurrentChangeSeq, 200)
		if err != nil {
			return s, err
		}
		for _, ch := range p.Changes {
			n, err := w.applyChange(ch, cursor)
			if err != nil {
				return s, err
			}
			s.Applied += n.applied
			s.Conflicts += n.conflicts
			cursor = ch.ChangeSeq
		}
		if !p.HasMore || len(p.Changes) == 0 {
			break
		}
	}

	// v21.201 A3：游标兜底落库。
	//
	// 定罪（用户实机 19:01~19:05 日志）：`cursor` **只在 applyChange 内推进**，
	// 上面 `len(p.Changes)==0` 的正常收尾分支不落库；进程被关闭时 pull 循环
	// 经 `ctx.Done()` 中途 return 同样不落库。于是 `sync_state.cursor` 长期滞后，
	// 下一轮 `RunOnce` 的 `cursor < st.MinAvailableChangeSeq` 判定成立
	// → `ErrResyncNeeded` → auto-bootstrap 无限重复（19:02:17 g-000002、
	// 19:03:02 g-000003，两代 watermark 都是 253 —— 服务端根本没变）。
	//
	// 修法：本轮成功收尾且游标仍落后于服务端水位时，直接对齐到
	// `st.CurrentChangeSeq`（语义 = "本轮已确认无更多增量"）。
	// 必须在 push 之前落库：push 失败也要保住"拉取水位"，否则用户关窗后又要重建。
	if cursor < st.CurrentChangeSeq {
		if err := setState(w.db, "cursor", strconv.FormatInt(st.CurrentChangeSeq, 10)); err != nil {
			return s, err
		}
		cursor = st.CurrentChangeSeq
	}

	// ---- Push（批 ≤50，实体级单飞 + 合并，§41/ADR-002）----
	// P1 批次3（R3 承接）：FAILED 且未达上限的 op 回到 PENDING 重试（此前 buildBatch 只取
	// PENDING、FAILED 实为僵尸态——本轮修正）；达上限转 CONFLICT 人工面。
	if err := w.reconcileOutboxRetries(); err != nil {
		return s, err
	}
	for {
		// ⭐ v21.212：Push 循环也让路。理由同 Pull 循环——换代窗口要的是
		// 「尽快把库句柄交出去」，而不是「把待推批次推完」。
		// 注意让路点必须在 buildBatch **之前**：已标SENDING 的批次若在此处中断，
		// 会留下僵尸 SENDING 行（靠 reconcileOutboxRetries 回收），
		// 放在 buildBatch 之前可保证不产生半批状态。
		if w.shouldYield() {
			return s, nil
		}
		batch, err := w.buildBatch()
		if err != nil {
			return s, err
		}
		if len(batch.ops) == 0 {
			break
		}
		// P1 批次3：NOTE op 的 refs 先确保已上传（账本驱动）；结构性分歧（MISMATCH/
		// IDENTITY）转该实体 CONFLICT 并从本批剔除；其他错误中止本轮（SENDING 回拨）。
		if err := w.ensureRefUploads(&batch); err != nil {
			w.unmarkSending(batch.ids)
			return s, err
		}
		if len(batch.ops) == 0 {
			break
		}
		results, err := w.cl.PushCtx(ctx, batch.ops)
		if err != nil {
			w.unmarkSending(batch.ids)
			return s, err
		}
		s.Pushed += len(results)
		for _, r := range results {
			acked, err := w.applyResult(r, batch)
			if err != nil {
				return s, err
			}
			if acked {
				s.Acked++
			}
		}
	}
	// P1 批次3：拉完/推完做一轮附件下载（sha256 权威校验，R6）。
	if w.AttachDir != "" {
		d, rp, f, aerr := w.SyncAttachmentsOnce()
		if aerr != nil && !errors.Is(aerr, ErrAuthRevoked) {
			return s, aerr
		}
		s.Downloaded, s.Repaired, s.AttachmentFailed = d, rp, f
	}
	_ = setState(w.db, "last_sync_at", time.Now().UTC().Format(time.RFC3339))
	return s, nil
}

type applyStat struct{ applied, conflicts int }

// applyChange 单条 change：脏检查 → 冲突记录 或 应用；cursor 与应用同事务（§34）。
// 实体路由（P1 批次2）：NOTE / FOLDER 逐实体 + FOLDER_ORDER 集合级；未知类型仅推进 cursor
// （向后兼容：旧客户端对新实体天然走这条路，协议非破坏性扩展的依据）。
func (w *Worker) applyChange(ch syncspec.ChangeDTO, oldCursor int64) (applyStat, error) {
	var out applyStat
	// 自回声跳过：本设备推送的事件不再应用/不判脏（否则 ACK 回写前的 pull 会把
	// 自己的 op 误标 CONFLICT——P0-D 集成测试实锤）。cursor 仍同事务推进。
	if ch.DeviceID != "" && ch.DeviceID == w.device {
		return out, w.setCursor(oldCursor, ch.ChangeSeq)
	}

	// §32 P1-b（2026-10-15）：blob_ref 载荷先下载解析再走统一应用链——脏检查/冲突
	// 记录/应用逻辑零感知（看到的都是展开后的 JSON）。下载失败不推 cursor = 下轮
	// pull 重取（与传输失败同语义）；连续 payloadMissingEscapeLimit 轮仍缺失则跳过
	// 该 change 并 WARN（防"服务端文件永久丢失 → pull 永久卡死"的永不收敛形态；
	// 该实体随后续 Bootstrap/RESYNC 全量自愈）。
	if ch.PayloadKind == "blob_ref" {
		data, err := w.fetchBlobPayload(ch)
		if err != nil {
			if errors.Is(err, ErrPayloadGone) && w.notePayloadMissing(ch.ChangeSeq) {
				log.Printf("[sync] ⚠ payload missing after %d attempts, skip seq=%d ref=%s（实体由后续全量重建自愈）",
					payloadMissingEscapeLimit, ch.ChangeSeq, ch.PayloadRef)
				return out, w.setCursor(oldCursor, ch.ChangeSeq)
			}
			return out, err
		}
		ch.Payload = data
	}

	// FOLDER_ORDER 集合级 op（§55/ADR-001 集合面）：order 内任一成员脏 → 整 op 记冲突不部分应用。
	if ch.EntityType == syncspec.EntityTypeFolderOrder {
		var op syncspec.FolderOrderPayload
		if err := json.Unmarshal(ch.Payload, &op); err != nil {
			return out, w.setCursor(oldCursor, ch.ChangeSeq) // 坏载荷：跳过但 cursor 照推（服务端权威产）
		}
		var dirtyIDs []string
		for _, sid := range op.Order {
			if dirty, err := IsDirty(w.db, sid); err != nil {
				return out, err
			} else if dirty {
				dirtyIDs = append(dirtyIDs, sid)
			}
		}
		tx, err := w.db.Begin()
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if len(dirtyIDs) > 0 {
			if err := addConflict(tx, "FOLDER_ORDER", syncspec.EntityTypeFolderOrder, "UPDATE", "CONFLICT_ON_PULL", "", string(ch.Payload), 0, ch.ServerVersion); err != nil {
				return out, err
			}
			for _, sid := range dirtyIDs {
				if _, err := tx.Exec(`UPDATE sync_outbox SET status='CONFLICT', updated_at=CURRENT_TIMESTAMP
					WHERE entity_sync_id = ? AND status IN ('PENDING','FAILED')`, sid); err != nil {
					return out, err
				}
			}
			if err := w.setCursorTx(tx, oldCursor, ch.ChangeSeq); err != nil {
				return out, err
			}
			if err := tx.Commit(); err != nil {
				return out, err
			}
			out.conflicts = 1
			return out, nil
		}
		if err := w.applyFolderOrder(tx, op); err != nil {
			return out, err
		}
		if err := w.setCursorTx(tx, oldCursor, ch.ChangeSeq); err != nil {
			return out, err
		}
		if err := tx.Commit(); err != nil {
			return out, err
		}
		out.applied = 1
		return out, nil
	}

	if ch.EntitySyncID == nil {
		return out, w.setCursor(oldCursor, ch.ChangeSeq)
	}
	isFolder := ch.EntityType == syncspec.EntityTypeFolder
	if !isFolder && ch.EntityType != "NOTE" {
		// 未知实体类型：仅推进 cursor（前向兼容）。
		return out, w.setCursor(oldCursor, ch.ChangeSeq)
	}
	// E7（2026-09-23）：PURGE（远端永久删）是**终态**，优先于 ADR-001 脏保护执行——
	// 否则本地有 FAILED/PENDING 挂账时 PURGE 被拦成普通 CONFLICT_ON_PULL → 僵尸行永留、
	// 恢复必再撞 ENTITY_GONE（真机第六轮实锤形态）。本地未推内容保全进冲突记录
	//（kind=ENTITY_GONE + local_payload 供「另存副本」），随后物理清理本地行。
	if ch.Operation == syncspec.OperationPurge && !isFolder {
		tx, err := w.db.Begin()
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if err := w.applyNoteChange(tx, ch); err != nil {
			return out, err
		}
		if err := w.setCursorTx(tx, oldCursor, ch.ChangeSeq); err != nil {
			return out, err
		}
		if err := tx.Commit(); err != nil {
			return out, err
		}
		out.applied = 1
		return out, nil
	}
	dirty, err := IsDirty(w.db, *ch.EntitySyncID)
	if err != nil {
		return out, err
	}
	if dirty {
		// ADR-001：不覆盖本地未同步修改；生成冲突并把脏链转 CONFLICT；cursor 照推。
		tx, err := w.db.Begin()
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		var localP string
		_ = tx.QueryRow(`SELECT payload FROM sync_outbox WHERE entity_sync_id = ?
			AND status IN ('PENDING','SENDING','FAILED') ORDER BY seq DESC LIMIT 1`, *ch.EntitySyncID).Scan(&localP)
		if err := addConflict(tx, *ch.EntitySyncID, ch.EntityType, ch.Operation, "CONFLICT_ON_PULL", localP, string(ch.Payload), 0, ch.ServerVersion); err != nil {
			return out, err
		}
		if _, err := tx.Exec(`UPDATE sync_outbox SET status='CONFLICT', updated_at=CURRENT_TIMESTAMP
			WHERE entity_sync_id = ? AND status IN ('PENDING','FAILED')`, *ch.EntitySyncID); err != nil {
			return out, err
		}
		if err := w.setCursorTx(tx, oldCursor, ch.ChangeSeq); err != nil {
			return out, err
		}
		if err := tx.Commit(); err != nil {
			return out, err
		}
		out.conflicts = 1
		return out, nil
	}

	tx, err := w.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if isFolder {
		if err := w.applyFolderChange(tx, ch); err != nil {
			return out, err
		}
	} else if err := w.applyNoteChange(tx, ch); err != nil {
		return out, err
	}
	if err := w.setCursorTx(tx, oldCursor, ch.ChangeSeq); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.applied = 1
	return out, nil
}

type payloadIn struct {
	SyncID       string  `json:"sync_id"`
	Title        string  `json:"title"`
	Content      string  `json:"content"`
	Tags         string  `json:"tags"`
	IsEncrypted  bool    `json:"is_encrypted"`
	Hash         string  `json:"encrypt_password_hash"`
	Encrypted    string  `json:"encrypted_content"`
	DeletedAt    *string `json:"deleted_at"`
	Version      int64   `json:"version"`
	FolderSyncID *string `json:"folder_sync_id"`
	Refs         []struct {
		Filename string `json:"filename"`
		Sha256   string `json:"sha256"`
		Size     int64  `json:"size"`
	} `json:"attachment_refs"`
}

// ⭐ v21.214（211c 实机·web 端移动笔记桌面端不同步）解析目标文件夹。
//
// 定罪（读服务端链路逐层坐实）：
//
//	handler/note_move.go:74 → app.NewServerNoteService(0).MoveNoteToFolder
//	→ app/note_service.go:228 `finishWriteTx(..., "UPDATE", ...)`
//	→ server/changelog BuildNotePayload：payload 带的是 **`folder_sync_id`**
//	  （LEFT JOIN note_folders 取 f.sync_id），**不是** folder_id。
//	⇒ 语义上「我的笔记」（folder_id IS NULL）时 `folder_sync_id` 为 **nil**，
//	  自定义文件夹时为该文件夹的 sync_id。桌面端据 `folder_sync_id` 反查本地 id。
//
// 原实现的缺陷（第 444-450 行）：
//
//	if p.FolderSyncID != nil && *p.FolderSyncID != "" {
//	    if err := ...Scan(&fid); err == nil { folderID = &fid }
//	}                       // ← err != nil 时**静默吞掉**，folderID 保持 nil
//
// 后果：① 反查失败（本地还没收到那个 FOLDER change，sync_id 尚不存在）
// → folderID=nil → **UPDATE 把笔记写回「我的笔记」**，移动被静默撤销；
// ② 与 CREATE 分支语义混淆 —— nil 既可能是"真的没有文件夹"，也可能是"查不到"，
// 两者不可区分。**这是"web 移动 → 桌面不同步"的直接机制**。
//
// 修法：把「查不到」与「本就没有」区分开。
//
//	· payload 带 FolderSyncID 但本地查不到 ⇒ 说明**依赖的文件夹还没落地**
//	  （FOLDER change 排在本次 NOTE 之后到达，或本地漏收）。此时**必须记账并跳过**
//	  本条 change —— 若照旧写 nil，等于把用户的移动无声地撤销。
//	  跳过不是丢数据：cursor 不推进（返回错误让本轮中止），下一轮 FOLDER 先落地后
//	  这条 NOTE 会被重新拉到并正确应用。
//	· payload 不带 FolderSyncID ⇒ 权威语义「我的笔记」，落 nil 是正确的。
func resolveNoteFolderID(tx *sql.Tx, p payloadIn) (*int64, error) {
	if p.FolderSyncID == nil || *p.FolderSyncID == "" {
		return nil, nil // 权威语义：不在任何文件夹 =「我的笔记」
	}
	var fid int64
	switch err := tx.QueryRow(`SELECT id FROM note_folders WHERE sync_id = ?`, *p.FolderSyncID).Scan(&fid); {
	case err == nil:
		return &fid, nil
	case errors.Is(err, sql.ErrNoRows):
		// v21.214：本地尚无此文件夹。**记账 + 中止本条**，绝不静默写 nil。
		return nil, fmt.Errorf("folder_sync_id %s 未落地（本地缺该文件夹）：拒绝应用本条 change，"+
			"避免把用户的移动静默撤销为「我的笔记」", *p.FolderSyncID)
	default:
		return nil, fmt.Errorf("查 folder_sync_id %s 失败: %w", *p.FolderSyncID, err)
	}
}
func (w *Worker) applyNoteChange(tx *sql.Tx, ch syncspec.ChangeDTO) error {
	var p payloadIn
	if err := json.Unmarshal(ch.Payload, &p); err != nil || p.SyncID == "" {
		return fmt.Errorf("bad change payload @%d", ch.ChangeSeq)
	}
	// v21.214：文件夹解析失败即中止本条（不再静默写 nil 把移动撤销掉）。
	folderID, ferr := resolveNoteFolderID(tx, p)
	if ferr != nil {
		return fmt.Errorf("change @%d (note %s): %w", ch.ChangeSeq, p.SyncID, ferr)
	}
	var existID int64
	err := tx.QueryRow(`SELECT id FROM notes WHERE sync_id = ?`, p.SyncID).Scan(&existID)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return err
	}
	switch ch.Operation {
	case "CREATE", "UPDATE":
		if missing {
			res, ierr := tx.Exec(`INSERT INTO notes (user_id, title, content, content_text, tags,
					is_encrypted, encrypt_password_hash, encrypted_content, folder_id, sync_id, version)
				VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				p.Title, p.Content, htmltext.ToText(p.Content), p.Tags, p.IsEncrypted, p.Hash, p.Encrypted,
				folderID, p.SyncID, ch.ServerVersion)
			if ierr != nil {
				return ierr
			}
			existID, _ = res.LastInsertId()
		} else if p.DeletedAt != nil {
			// §56 回收站行重指向类 UPDATE：payload 携带 deleted_at，应用端必须保持墓碑态
			// （无条件 deleted_at=NULL 会把回收站笔记复活——批次2 修复）。
			if _, ierr := tx.Exec(`UPDATE notes SET title=?, content=?, content_text=?, tags=?, is_encrypted=?,
					encrypt_password_hash=?, encrypted_content=?, folder_id=?, version=?, deleted_at=?,
					updated_at=CURRENT_TIMESTAMP WHERE id=?`,
				p.Title, p.Content, htmltext.ToText(p.Content), p.Tags, p.IsEncrypted, p.Hash, p.Encrypted,
				folderID, ch.ServerVersion, *p.DeletedAt, existID); ierr != nil {
				return ierr
			}
		} else {
			if _, ierr := tx.Exec(`UPDATE notes SET title=?, content=?, content_text=?, tags=?, is_encrypted=?,
					encrypt_password_hash=?, encrypted_content=?, folder_id=?, version=?, deleted_at=NULL,
					updated_at=CURRENT_TIMESTAMP WHERE id=?`,
				p.Title, p.Content, htmltext.ToText(p.Content), p.Tags, p.IsEncrypted, p.Hash, p.Encrypted,
				folderID, ch.ServerVersion, existID); ierr != nil {
				return ierr
			}
		}
		if terr := model.SyncNoteTagsTx(tx, existID, p.Tags); terr != nil {
			return terr
		}
		if _, uerr := tx.Exec(`DELETE FROM note_uploads WHERE note_id = ?`, existID); uerr != nil {
			return uerr
		}
		for _, ref := range p.Refs {
			// uploads 父行必须先于 note_uploads 引用存在（v21.71 起真 FK；批次 6 走查
			// 坐实：他机图片笔记首次 pull 因缺父行整事务 FK 失败——批次 3 集成工装最小
			// DDL 无 FK 掩盖）。同本地已自传同名件：OR IGNORE 保既有 sha/status。
			if _, ierr := tx.Exec(`INSERT OR IGNORE INTO uploads (filename, user_id, size, sha256, status)
				VALUES (?, 1, ?, ?, 'attached')`, ref.Filename, ref.Size, ref.Sha256); ierr != nil {
				return ierr
			}
			if _, ierr := tx.Exec(`INSERT OR IGNORE INTO note_uploads (note_id, filename) VALUES (?, ?)`,
				existID, ref.Filename); ierr != nil {
				return ierr
			}
		}
		// P1 批次3：refs 入下载队列（同事务；本地已有且哈希一致直接 OK 免重下）。
		refs := make([]syncspec.AttachmentRef, 0, len(p.Refs))
		for _, r := range p.Refs {
			refs = append(refs, syncspec.AttachmentRef{Filename: r.Filename, Sha256: r.Sha256, Size: r.Size})
		}
		if err := QueueAttachmentsFromRefs(tx, w.AttachDir, refs); err != nil {
			return err
		}
	case "DELETE":
		if !missing {
			if _, derr := tx.Exec(`UPDATE notes SET deleted_at = CURRENT_TIMESTAMP, version = ? WHERE id = ?`,
				ch.ServerVersion, existID); derr != nil {
				return derr
			}
		}
	case "RESTORE":
		if !missing {
			if _, derr := tx.Exec(`UPDATE notes SET deleted_at = NULL, trash_upload_bytes = 0, version = ? WHERE id = ?`,
				ch.ServerVersion, existID); derr != nil {
				return derr
			}
		}
	case syncspec.OperationPurge:
		// E7（2026-09-23）：服务端永久删（彻底删/清空回收站）——本地**物理清理**该行，
		// 撤销其实体全部未收账 op，并把 open 冲突自动收口（远端已永久消失，人工处置无意义）。
		// 若本地仍有未推送改动，冲突记录保留 local_payload 供「另存副本」找回内容。
		if !missing {
			var pend int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM sync_outbox
				WHERE entity_sync_id = ? AND status IN ('PENDING','SENDING','FAILED')`,
				ch.EntitySyncID).Scan(&pend); err != nil {
				return err
			}
			if pend > 0 {
				var localP string
				_ = tx.QueryRow(`SELECT payload FROM sync_outbox WHERE entity_sync_id = ?
					ORDER BY seq DESC LIMIT 1`, ch.EntitySyncID).Scan(&localP)
				if err := addConflict(tx, *ch.EntitySyncID, "NOTE", "PURGE", "ENTITY_GONE",
					localP, "", 0, ch.ServerVersion); err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE sync_conflicts SET status='resolved',
					resolution='remote-purged', resolved_at=CURRENT_TIMESTAMP
					WHERE entity_sync_id = ? AND status='open'`, ch.EntitySyncID); err != nil {
					return err
				}
			}
			if _, derr := tx.Exec(`UPDATE sync_outbox SET status='ACKED',
				last_error='dropped-by-remote-purge', updated_at=CURRENT_TIMESTAMP
				WHERE entity_sync_id = ? AND status IN ('PENDING','SENDING','FAILED')`,
				ch.EntitySyncID); derr != nil {
				return derr
			}
			if _, derr := tx.Exec(`UPDATE sync_conflicts SET status='resolved',
				resolution='remote-purged', resolved_at=CURRENT_TIMESTAMP
				WHERE entity_sync_id = ? AND status='open'`, ch.EntitySyncID); derr != nil {
				return derr
			}
			if _, derr := tx.Exec(`DELETE FROM notes WHERE id = ?`, existID); derr != nil {
				return derr
			}
		}
	}
	return nil
}

// FolderLockChangedHook v2.3.0：拉取到文件夹锁定态变化时通知桌面端清理本进程解锁授权。
// 由 main 包注入（synclocal 不反向依赖 LocalService）；测试可为 nil。
var FolderLockChangedHook func(folderID int64, encrypted bool)

// applyFolderChange FOLDER CREATE/UPDATE/DELETE 应用（批次2，§18）。
// DELETE 时本地引用该夹的笔记（含回收站行）统一改指默认文件夹（与服务端 §56 语义同构）。
func (w *Worker) applyFolderChange(tx *sql.Tx, ch syncspec.ChangeDTO) error {
	var p syncspec.FolderPayload
	if err := json.Unmarshal(ch.Payload, &p); err != nil || p.SyncID == "" {
		return fmt.Errorf("bad folder payload @%d", ch.ChangeSeq)
	}
	var existID int64
	err := tx.QueryRow(`SELECT id FROM note_folders WHERE sync_id = ?`, p.SyncID).Scan(&existID)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return err
	}
	switch ch.Operation {
	case "CREATE", "UPDATE":
		encrypted := false
		hash := ""
		if p.IsEncrypted != nil {
			encrypted = *p.IsEncrypted
		}
		if p.EncryptPasswordHash != nil {
			hash = *p.EncryptPasswordHash
		}
		if missing {
			res, ierr := tx.Exec(`INSERT INTO note_folders (user_id, name, sort_order, sync_id, version, is_encrypted, encrypt_password_hash)
				VALUES (1, ?, ?, ?, ?, ?, ?)`,
				p.Name, p.SortOrder, p.SyncID, ch.ServerVersion, boolToInt(encrypted), hash)
			if ierr != nil {
				return ierr
			}
			existID, _ = res.LastInsertId()
		} else {
			// 旧服务端/旧载荷缺字段 → 保留本地现状，绝不能覆盖为未加密。
			if p.IsEncrypted == nil || p.EncryptPasswordHash == nil {
				var curEnc int
				var curHash string
				if qerr := tx.QueryRow(`SELECT is_encrypted, encrypt_password_hash FROM note_folders WHERE id = ?`, existID).
					Scan(&curEnc, &curHash); qerr != nil {
					return qerr
				}
				if p.IsEncrypted == nil {
					encrypted = curEnc != 0
					if p.EncryptPasswordHash == nil {
						hash = curHash
					}
				}
			}
			if !encrypted {
				hash = ""
			}
			if _, uerr := tx.Exec(`UPDATE note_folders SET name = ?, sort_order = ?, version = ?, is_encrypted = ?, encrypt_password_hash = ? WHERE id = ?`,
				p.Name, p.SortOrder, ch.ServerVersion, boolToInt(encrypted), hash, existID); uerr != nil {
				return uerr
			}
		}
		if FolderLockChangedHook != nil {
			FolderLockChangedHook(existID, encrypted)
		}
	case "DELETE":
		if !missing {
			if _, uerr := tx.Exec(`UPDATE notes SET folder_id = NULL WHERE folder_id = ?`, existID); uerr != nil {
				return uerr
			}
			if _, derr := tx.Exec(`DELETE FROM note_folders WHERE id = ?`, existID); derr != nil {
				return derr
			}
			if FolderLockChangedHook != nil {
				FolderLockChangedHook(existID, false)
			}
		}
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// applyFolderOrder 集合级重排应用：按权威版本表重写 sort_order/version（单调不回退）。
// 未知 sync_id 的 UPDATE 影响 0 行自然跳过（本地未同步到的夹由各自事件补齐）。
func (w *Worker) applyFolderOrder(tx *sql.Tx, op syncspec.FolderOrderPayload) error {
	for i, vr := range op.Versions {
		if _, err := tx.Exec(`UPDATE note_folders SET sort_order = ?, version = ? WHERE sync_id = ? AND version <= ?`,
			i+1, vr.Version, vr.SyncID, vr.Version); err != nil {
			return err
		}
	}
	for i, sid := range op.Order {
		if i < len(op.Versions) {
			continue
		}
		if _, err := tx.Exec(`UPDATE note_folders SET sort_order = ? WHERE sync_id = ?`, i+1, sid); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) setCursor(old, new int64) error {
	tx, err := w.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := w.setCursorTx(tx, old, new); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *Worker) setCursorTx(tx *sql.Tx, old, new int64) error {
	cur, err := getState(w.db, "cursor")
	if err != nil {
		return err
	}
	_ = cur
	_, err = tx.Exec(`INSERT INTO sync_state (key, value) VALUES ('cursor', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(new, 10))
	return err
}

// batch 一轮推送的取样结果：ops（已合并）+ 对应 outbox seq + op_id→seq 索引。
