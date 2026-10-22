// Package bootstrap 桌面 Bootstrap / RESYNC 编排（离线方案 v20-Project P5，§73~§75/§89~§92）。
//
// 流程：请求快照 → 轮询 READY → 下载 zip（.part 原子）→ sha256 校验 → 解包（meta/冻结库/附件）
// → Activator.Activate 双代切换（lkg 先写 + 四相位 journal 由 localstore 承担）
// → 新代 cursor=watermark + epoch/instance 锚 → RESYNC Outbox 搬运重定基（§39/§90）。
//
// 红线（结构性保证）：
//   - Activate 之前任何失败 = 当代库零触碰、零破坏（下载/校验/解包全在 staging 区）；
//   - Outbox 搬运只读旧代 + 幂等写新代（op_id 去重），搬运失败旧代数据原样在盘；
//   - NOTE/FOLDER op 在新代找不到实体 → CONFLICT 转人工面，绝不猜 base 重放；
//   - FOLDER_ORDER 集合级 op 丢弃：快照序即服务端权威序，本地重排意图由下一次
//     用户移动动作自然再生成（防把已被他人重排的旧全序强推回去）。
package bootstrap

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stickynote/syncspec"

	"stickynote-desktop/localauth"
	"stickynote-desktop/localstore"
	"stickynote-desktop/synclocal"
)

var (
	ErrNotReady     = errors.New("bootstrap: server snapshot not ready")
	ErrSnapshotFail = errors.New("bootstrap: server snapshot build failed")
	ErrSHAMismatch  = errors.New("bootstrap: snapshot sha256 mismatch")
	ErrBadZip       = errors.New("bootstrap: snapshot zip invalid")
)

const (
	pollInterval = 2 * time.Second
	// pollTimeout 快照就绪轮询上限。**v21.200 定版 90s（原 10 分钟）**。
	//
	// 定版依据（2026-10-09 用户实机取证）：客户端点「登录/强制全量同步」后
	// `BootstrapNow` 是**同步阻塞**调用（sync_bindings.go finishRemoteLogin），
	// 前端 `await` 期间按钮永久 loading。原 10 分钟上限意味着**一次失败的网络/
	// 代理链路就把 UI 锁死 10 分钟**，用户只能杀进程 —— 实测日志精确吻合：
	//   18:15:34 [bootstrap] 本地无文件夹…执行一次快照全量同步
	//   （此后 2 分 14 秒日志完全空白，直到用户重启进程）
	//
	// 为什么 90s 够：服务端快照是 `VACUUM INTO` 同步产物（现场 33.3 断言实测
	// 轮询首个周期即 READY），正常路径是**秒级**；90s 已覆盖大库冷启动的量级，
	// 又把最坏等待压到用户可接受范围。超时按失败返回（ErrNotReady），
	// 由调用方保留 lkg、不换代（见 activate 之后的回滚收口）。
	pollTimeout = 90 * time.Second
)

// PollTimeout 导出的定版超时（v21.200）—— 供回归测试断言「不再是10 分钟量级」。
// 导出而非仅测试内可见：desktop 包的测试需要跨包断言该常量。
const PollTimeout = pollTimeout

// Meta 与 server/sync.SnapshotMeta 的 wire 契约（zip 内 meta.json）。
type Meta struct {
	SnapshotID   string `json:"snapshot_id"`
	ProtocolVer  int    `json:"protocol_version"`
	UserID       int64  `json:"user_id"`
	SourceUserID int64  `json:"source_user_id"`
	Username     string `json:"username"`
	ServerEpoch  int64  `json:"server_epoch"`
	Watermark    int64  `json:"watermark"`
	InstanceID   string `json:"instance_id"`
	CreatedAt    string `json:"created_at"`
	ExpiresAt    string `json:"expires_at"`
	FileCount    int    `json:"file_count"`
	DBSHA256     string `json:"db_sha256"`
}

type Runner struct {
	ProfileDir string
	AttachDir  string // profileDir/uploads（附件合并目标）
}

type Result struct {
	SnapshotID    string
	Generation    string
	Epoch         int64
	Watermark     int64
	Files         int
	Carried       int // RESYNC 搬运的 op 数
	CarryConflict int // 找不到实体转 CONFLICT
	CarryDropped  int // FOLDER_ORDER 丢弃
}

// Run 同步执行一次完整 Bootstrap（无取消能力；供测试与内部调用）。
// oldGenDB 可为 nil（全新装机）；非 nil 时执行 RESYNC Outbox 搬运（§89/§90）。
func (r *Runner) Run(sess *localauth.Session, oldGenDB *sql.DB) (*Result, error) {
	return r.RunCtx(context.Background(), sess, oldGenDB)
}

// RunCtx 是本体（v21.203 批次 3：ctx 全链贯通）。
//
// ⭐ 为什么必须可取消：整条链此前**没有任何 context** ——
// `RequestSnapshot` → `poll`（裸 `time.Sleep`）→ `DownloadSnapshot`（`Client.Timeout`
// 总时限）→ 解包 → 换代 → 后置步骤。用户点「强制全量同步」后唯一的"取消"手段
// 是杀进程，而**杀进程恰好落在换代窗口里** ⇒ 制造一次 SEALED 崩溃态（批次 1 修的
// 正是这个）。也就是说：网络卡住 → 用户杀进程 → 数据回退。
//
// 现在 ctx 从绑定层一路传到每个可阻塞点：HTTP 请求（NewRequestWithContext）、
// 轮询等待（select on ctx.Done + ticker 而非 Sleep）、下载（分阶段超时 + 看门狗）。
func (r *Runner) RunCtx(ctx context.Context, sess *localauth.Session, oldGenDB *sql.DB) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sess == nil || sess.ServerURL == "" || sess.AccessToken == "" {
		return nil, errors.New("bootstrap: live session required (login or token-exchange)")
	}
	// 换代前先探一次取消：避免在用户已取消的情况下还去建连接。
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	cl := synclocal.NewClient(sess.ServerURL, sess.AccessToken, sess.DeviceID)
	st := localstore.NewActivator(r.ProfileDir) // 布局与激活纪律全部下沉 localstore
	lay := localstore.Layout{ProfileDir: r.ProfileDir}
	if err := lay.EnsureDirs(); err != nil {
		return nil, err
	}

	// 1) 请求 + 2) 轮询
	// ⭐ v21.218：传入 ctx —— 原调用无 ctx，关机/换代取消信号在此丢失，
	// 只能等满 30s 客户端超时（实机日志「16:15:23 → 16:15:53」空窗即此）。
	SetPhase(PhaseRequest)
	snapID, err := cl.RequestSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("request snapshot: %w", err)
	}
	SetPhase(PhasePoll)
	sts, err := r.pollCtx(ctx, cl, snapID)
	if err != nil {
		return nil, err
	}

	// 3) 下载（.part 原子）+ 4) sha256（对 sealed 文件）
	SetPhase(PhaseDownload)
	zipPath := filepath.Join(lay.StagingDir(), snapID+".zip")
	if err := cl.DownloadSnapshotCtx(ctx, snapID, zipPath); err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	SetPhase(PhaseVerify)
	sha, err := localstore.SealSHA256(zipPath)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(sha, sts.Sha256) {
		_ = os.Remove(zipPath)
		return nil, fmt.Errorf("%w: want %s got %s", ErrSHAMismatch, sts.Sha256, sha)
	}

	// 5) 解包 staging：meta.json + notes.db + uploads/*
	meta, err := unpack(zipPath, lay.StagingDir(), filepath.Join(lay.StagingDir(), "boot-uploads"))
	if err != nil {
		return nil, err
	}
	if meta.Watermark != sts.Watermark || meta.ServerEpoch != sts.ServerEpoch {
		return nil, fmt.Errorf("%w: meta/ledger anchor mismatch (%d,%d) vs (%d,%d)",
			ErrBadZip, meta.ServerEpoch, meta.Watermark, sts.ServerEpoch, sts.Watermark)
	}
	stagingDB := filepath.Join(lay.StagingDir(), snapID+".db")
	manifest, _ := json.Marshal(map[string]any{
		"provenance": "bootstrap-v1", "snapshot_id": snapID, "epoch": meta.ServerEpoch,
		"watermark": meta.Watermark, "instance": meta.InstanceID, "user": meta.UserID,
	})

	// 6) 双代激活（lkg 先写 + journal 四相位；崩溃由 Recover 对账收敛）
	//
	// ⚠️ v21.200 换代原子性修复（用户实机取证：笔记消失 + 身份丢失）。
	// 定罪：`Activate` 只保证**自身**的四相位原子（失败回滚到 prev），但它之后
	// 还有 5 个可失败步骤（附件合并 /锚与游标 / carry outbox / 身份迁移 / 自检）。
	// 原实现在这些步骤失败时直接 `return nil, err` —— 此时**新代早已是active**，
	// 于��用户看到的是「上一代已同步的笔记全没了 + 账号与设备显示未登录」，
	// 且**没有任何回滚**（进程重启后读的正是这个残缺新代）。
	//
	// 实测日志吻合：18:15:04 `g-000001` → 18:17:48 `g-000002` +
	// `[auth] identity healed from g-000001`（身份是靠启动期heal 兜底才找回来的）。
	//
	// 现在：记录 prev，任一后续步骤失败即**把active 指针复位回 prev 并删除残缺新代**
	// （v21.203：残缺代必删 —— 否则反复换代会白占几十 GB，且用户手工打开会看到
	// 一份「有指针历史、内容不完整」的库）。
	prevGen, err := lay.Active()
	if err != nil {
		return nil, err
	}
	// v21.203 封相纪律：Activate 返回后 journal 停在 SEALED，全部后置步骤成功才
	// SealCommitted。封相前的任何失败（进程内 return，或被 kill 后下次启动）
	// 都由 Recover 走「回退 prev + 删残缺代」收敛 —— 崩溃态与业务失败态同一条路径。
	activated := false
	committed := false
	gen := ""
	defer func() {
		if committed {
			return
		}
		if activated {
			if rerr := lay.RollbackActiveDiscard(prevGen, gen); rerr != nil {
				log.Printf("[bootstrap] ⚠ 换代后置步骤失败且指针回退失败（需人工介入）：prev=%s err=%v", prevGen, rerr)
			} else {
				log.Printf("[bootstrap] 换代后置步骤失败，已回退 active → %s 并删除残缺代 %s（上一代数据完好）", prevGen, gen)
			}
		}
		lay.GC() // 失败路径的 staging 残留（zip/db/boot-uploads）一并回收
	}()

	SetPhase(PhaseActivate)
	g, aerr := st.Activate(stagingDB, manifest)
	if aerr != nil {
		return nil, fmt.Errorf("activate: %w", aerr)
	}
	gen, activated = g, true

	// 7) 附件合并（只增不删：同名同内容跳过；同名异内容保留本地并交给 sha256 自愈轮）
	SetPhase(PhasePostprocess)
	files, err := mergeAttachments(filepath.Join(lay.StagingDir(), "boot-uploads", "uploads"), r.AttachDir)
	if err != nil {
		return nil, err
	}

	// 8) 新代锚与游标：cursor=W（§75；W 后事件由 pull 补差）
	newDB, err := localstore.OpenGenDB(lay.GenDB(gen))
	if err != nil {
		return nil, err
	}
	defer newDB.Close()
	if err := synclocal.EnsureTables(newDB); err != nil { // 老快照缺表兜底（幂等）
		return nil, err
	}
	// v21.165 修复（桌面首装同步显示缺陷，204 轮走查定罪）：
	// `client_profile` 是 desktop 专属表，服务端快照**天然不含**。此前建表动作只存在于
	// 步骤 9.5 的 `CarryClientIdentity` 内层，而该步在 `if oldGenDB != nil` 分支内且
	// 发生在 `verifyIdentityCarried` 判定之后 —— 只要 carry 分支没走到（首装路径 /
	// 旧代无身份行 / 离线单机换代），新代就**永久缺这张表**。
	// 后果链（204 轮真机铁证）：新代缺表 → `bootstrapNowCtx` 的后续接管面
	// （含 database.SetDB(newDB)）未完成 → 前端继续读旧代句柄 → 库里明明有笔记，
	// UI 却恒显「共 0 条笔记」且交互无响应；journal 停在 SEALED。
	// 修复：建表提到步骤 8 与 synclocal 并列，**无条件**执行（幂等，零副作用），
	// 使「换代后新代 schema 完备」成为不变量，而不是 carry 分支的偶然副产品。
	if err := localauth.EnsureTables(newDB); err != nil {
		return nil, fmt.Errorf("client_profile tables: %w", err)
	}
	res := &Result{SnapshotID: snapID, Generation: gen, Epoch: meta.ServerEpoch,
		Watermark: meta.Watermark, Files: files}
	if err := setAnchor(newDB, "cursor", meta.Watermark); err != nil {
		return nil, err
	}
	if err := setAnchor(newDB, "server_epoch", meta.ServerEpoch); err != nil {
		return nil, err
	}
	if meta.InstanceID != "" {
		if _, err := newDB.Exec(`INSERT INTO sync_state(key,value) VALUES('server_instance_id',?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, meta.InstanceID); err != nil {
			return nil, err
		}
	}

	// 9) RESYNC Outbox 搬运（§39/§90；仅旧代存在时）
	if oldGenDB != nil {
		c, cf, dp, err := carryOverOutbox(oldGenDB, newDB)
		if err != nil {
			return nil, fmt.Errorf("carry outbox: %w", err)
		}
		res.Carried, res.CarryConflict, res.CarryDropped = c, cf, dp
		// 9.5) 客户端身份迁移（E4-C，2026-09-23 真机实锤）：client_profile（含
		// refresh/secret_sealed 登录凭据封存与 PIN verifier）是 desktop 专属表，
		// 服务端快照不含——换代后不迁移 = 凭据全丢 →「账号与设备」显示未登录、
		// 必须重新登录（用户实测第 2/3/4 条的根因）。
		if err := CarryClientIdentity(oldGenDB, newDB); err != nil {
			return nil, fmt.Errorf("carry client identity: %w", err)
		}
		// 9.6) 迁移结果自检（v21.153 R-1/W4 收口）：carry 曾观测到偶发未落且**静默**
		//（新代无 client_profile → 卡片「离线单机」、同步全败，直到重启 heal 兜底）。
		if verr := verifyIdentityCarried(oldGenDB, newDB); verr != nil {
			return nil, verr
		}
	}

	// 10) 清理（zip/staging 库由 Activate 后不再需要；保留 boot-uploads 至合并完成后删除）
	_ = os.Remove(zipPath)
	_ = os.Remove(stagingDB)
	_ = os.RemoveAll(filepath.Join(lay.StagingDir(), "boot-uploads"))
	res.Files = files
	// v21.203 封相：**只有走到这里**才允许落 COMMITTED（=「后置步骤已确认」）。
	// 顺序铁律：SealCommitted 必须在 defer 关闭回退之前完成，否则封相失败会让
	// defer 把一次成功的换代给回退掉（宁可保守回退，不可留残缺代）。
	if err := st.SealCommitted(gen); err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	committed = true // 全部后置步骤成功 → 关闭指针回退
	SetPhase(PhaseDone)
	log.Printf("[bootstrap] generation %s sealed+committed (journal cleared)", gen)
	lay.GC() // 换代成功：回收超出保留窗口的旧代（active/lkg/最近 2 代必留）
	return res, nil
}

func (r *Runner) poll(cl *synclocal.Client, id string) (*synclocal.SnapshotStatusResp, error) {
	return r.pollCtx(context.Background(), cl, id)
}

// pollCtx 轮询到 READY。**不用 `time.Sleep`**：裸 Sleep 期间 ctx 取消无法感知，
// 用户点了取消仍要干等一个轮次（2s，量大时更多）才有反应；更要紧的是关机时
// scheduler 收不到 Done。这里用 `select { ctx.Done / time.After }`。
func (r *Runner) pollCtx(ctx context.Context, cl *synclocal.Client, id string) (*synclocal.SnapshotStatusResp, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(pollTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("bootstrap 轮询已取消: %w", err)
		}
		st, err := cl.SnapshotStatus(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("poll: %w", err)
		}
		switch st.Status {
		case "READY":
			return st, nil
		case "FAILED", "EXPIRED", "INVALIDATED":
			return nil, fmt.Errorf("%w: %s (%s)", ErrSnapshotFail, st.Status, st.Error)
		}
		if time.Now().After(deadline) {
			return nil, ErrNotReady
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("bootstrap 轮询已取消: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// unpack zip → staging（notes.db 改名 <snapID>.db 供 Activate 消费；meta 解析；路径穿越拒绝）。
func unpack(zipPath, stagingDir, upDir string) (*Meta, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var meta *Meta
	dbDone := false
	for _, f := range zr.File {
		rel := filepath.ToSlash(f.Name)
		// v21.152 P0 修复：上传件条目 = `uploads/<文件名>`（服务端 add("uploads/"+r) 平铺写出）。
		// 旧判据 `filepath.Base(rel) == rel` 对任何 "uploads/x" 恒为假 → 整包被判非法
		// （含附件的快照一律 unpack 失败 → 首装 folders/历史实体永远拉不到；
		// 真机实证：`snapshot zip invalid: "uploads/2426b163-….png"`）。
		uploadName := strings.TrimPrefix(rel, "uploads/")
		safe := rel == "notes.db" || rel == "meta.json" ||
			(strings.HasPrefix(rel, "uploads/") && uploadName != "" && !strings.Contains(uploadName, "/"))
		if !safe || strings.Contains(rel, "..") {
			return nil, fmt.Errorf("%w: %q", ErrBadZip, f.Name)
		}
		src, err := f.Open()
		if err != nil {
			return nil, err
		}
		switch {
		case rel == "meta.json":
			b, _ := io.ReadAll(src)
			src.Close()
			meta = &Meta{}
			if err := json.Unmarshal(b, meta); err != nil {
				return nil, fmt.Errorf("%w: meta: %v", ErrBadZip, err)
			}
		case rel == "notes.db":
			tmp := filepath.Join(stagingDir, "notes.db.in")
			out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if err != nil {
				src.Close()
				return nil, err
			}
			if _, err := io.Copy(out, src); err != nil {
				out.Close()
				src.Close()
				return nil, err
			}
			_ = out.Sync()
			out.Close()
			src.Close()
			dbDone = true
		default: // uploads/<name>
			if err := os.MkdirAll(filepath.Join(upDir, "uploads"), 0700); err != nil {
				src.Close()
				return nil, err
			}
			dst := filepath.Join(upDir, rel)
			out, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if err != nil {
				src.Close()
				return nil, err
			}
			if _, err := io.Copy(out, src); err != nil {
				out.Close()
				src.Close()
				return nil, err
			}
			_ = out.Sync()
			out.Close()
			src.Close()
			if err := os.Rename(dst+".part", dst); err != nil {
				return nil, err
			}
		}
	}
	if meta == nil || !dbDone {
		return nil, fmt.Errorf("%w: missing meta.json or notes.db", ErrBadZip)
	}
	snapDB := filepath.Join(stagingDir, meta.SnapshotID+".db")
	if err := os.Rename(filepath.Join(stagingDir, "notes.db.in"), snapDB); err != nil {
		return nil, err
	}
	// 冻结库完整性（Activate 内还会再验一次——双保险，早失败少干活）
	if err := localstore.IntegrityCheckFile(snapDB); err != nil {
		return nil, err
	}
	return meta, nil
}

// mergeAttachments 只增不删合并；返回复制数。同名异内容保留本地（sha 自愈轮处理）。
func mergeAttachments(srcDir, dstDir string) (int, error) {
	if srcDir == "" {
		return 0, nil
	}
	ents, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return 0, err
	}
	copied := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		dst := filepath.Join(dstDir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // 已有即不动（含同名异内容——留给校验轮）
		}
		src, err := os.Open(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return copied, err
		}
		tmp := dst + ".boot-part"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			src.Close()
			return copied, err
		}
		if _, err := io.Copy(out, src); err != nil {
			src.Close()
			out.Close()
			_ = os.Remove(tmp)
			return copied, err
		}
		_ = out.Sync()
		src.Close()
		out.Close()
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return copied, err
		}
		copied++
	}
	return copied, nil
}

// carryOverOutbox §39/§90：旧代未推送 op 迁入新代并重定基。
// 返回 (搬运数, CONFLICT 数, FOLDER_ORDER 丢弃数)。幂等：op_id 已存在即跳过。
// ⚠ P5 绑定集成坐实的缺口修补：本地**未推送 CREATE** 的实体行不在服务端快照里，只搬 op
// 会"账在物失"（push 成功后 ACK 回写找不到行）——故 CREATE 引用的实体行若新代缺失，
// 按主键从旧代整行搬入（notes/note_folders 两表；附件由 mergeAttachments 已整体只增合并）。
func carryOverOutbox(oldDB, newDB *sql.DB) (carried, conflicts, dropped int, err error) {
	// ⭐ v21.203：**先一次性读全再逐条处理**（消除嵌套查询）。
	//
	// 定罪（审计 + 用户实机 2026-10-10 诊断包证据）：原实现在 `for rows.Next()`
	// 循环**内部**又发查询（`carryEntityRow` 对 oldDB 的 SELECT、对 newDB 的
	// SELECT/INSERT）。而 `OpenGenDB` 的 `MaxOpenConns(2)`（§93 纪律）：
	//   - `rows` 本身占住 oldDB 的**一条**连接不放；
	//   - 循环内对 oldDB 的 `carryEntityRow` SELECT 要**第二条** —— 池刚好 2 条，
	//     任何一侧再触发隐式取连接就会**自锁等待**（自己等自己释放的连接）。
	// 症状：换代期间诊断包 4 个 db.* 计数**全部 timeout**（业务连接被占满），
	// 且 carry 自身可能因拿不到连接而超时。
	//
	// 改法：一次 `Query` + 立即 `rows.Close()` + 收集到切片，再在**无游标占用**
	// 的状态下逐条处理。这样 oldDB/newDB 各自的取连接都是"用完即还"，
	// 与池大小无关。
	type outboxOp struct {
		opID, typ, sid, oper, status, payload string
		base                                  sql.NullInt64
	}
	var ops []outboxOp
	{
		rows, qerr := oldDB.Query(`SELECT op_id, entity_type, entity_sync_id, operation, base_version, payload, status
			FROM sync_outbox WHERE status IN ('PENDING','FAILED','CONFLICT')`)
		if qerr != nil {
			if strings.Contains(qerr.Error(), "no such table") { // 旧代早于 outbox 基建（理论不可达）
				return 0, 0, 0, nil
			}
			return 0, 0, 0, qerr
		}
		for rows.Next() {
			var o outboxOp
			var sid sql.NullString
			if serr := rows.Scan(&o.opID, &o.typ, &sid, &o.oper, &o.base, &o.payload, &o.status); serr != nil {
				rows.Close()
				return 0, 0, 0, serr
			}
			o.sid = sid.String
			ops = append(ops, o)
		}
		rows.Close()
		if rerr := rows.Err(); rerr != nil {
			return 0, 0, 0, rerr
		}
	}

	for _, o := range ops {
		if o.typ == syncspec.EntityTypeFolderOrder {
			dropped++
			continue
		}
		// base 全程 NullInt64：FOLDER_ORDER/规范 CREATE（§48 base=NULL）不再把搬运整链卡死
		// （批次 6 坐实的 batch-5 潜伏缺陷——NULL 扫进 int64 直接报错）。
		newBase := o.base
		if o.oper == "CREATE" {
			// 未推送实体行随 op 搬入（"账在物在"不变式；新代已有同 sync_id 则跳过）。
			if cerr := carryEntityRow(oldDB, newDB, o.typ, o.sid); cerr != nil {
				return carried, conflicts, dropped, cerr
			}
		} else { // UPDATE/DELETE/RESTORE：重定基到新代当前版本
			var v int64
			var q string
			switch o.typ {
			case syncspec.EntityTypeNote:
				q = `SELECT version FROM notes WHERE sync_id = ?`
			case syncspec.EntityTypeFolder:
				q = `SELECT version FROM note_folders WHERE sync_id = ?`
			}
			scanErr := newDB.QueryRow(q, o.sid).Scan(&v)
			if errors.Is(scanErr, sql.ErrNoRows) {
				newBase = sql.NullInt64{} // 标记 CONFLICT：新代无此实体，不猜
			} else if scanErr != nil {
				return carried, conflicts, dropped, scanErr
			} else {
				newBase = sql.NullInt64{Int64: v, Valid: true}
			}
		}
		// 直接落库（含 CONFLICT 判定；CREATE 的空 base 原样保留，push 面按 §48 处理）
		status := "PENDING"
		var baseVal any
		conflict := !newBase.Valid && o.oper != "CREATE"
		if conflict {
			status = "CONFLICT"
			baseVal = int64(0) // 冲突态统一落 0 基点（批 5 既有约定，消费端按 status 权威）
			conflicts++
		} else {
			if newBase.Valid {
				baseVal = newBase.Int64
			}
			carried++
		}
		if _, werr := newDB.Exec(`INSERT OR IGNORE INTO sync_outbox
			(op_id, entity_type, entity_sync_id, operation, base_version, payload, status, retry_count)
			VALUES (?,?,?,?,?,?,?,0)`, o.opID, o.typ, o.sid, o.oper, baseVal, o.payload, status); werr != nil {
			return carried, conflicts, dropped, werr
		}
	}
	return carried, conflicts, dropped, nil
}

func carryEntityRow(oldDB, newDB *sql.DB, typ, sid string) error {
	if sid == "" {
		return nil
	}
	var table, cols string
	switch typ {
	case syncspec.EntityTypeNote:
		table = "notes"
		cols = `user_id,title,content,content_text,tags,is_encrypted,encrypt_password_hash,
			encrypted_content,created_at,updated_at,deleted_at,version,folder_id,sync_id,trash_upload_bytes`
	case syncspec.EntityTypeFolder:
		table = "note_folders"
		cols = `user_id,name,sort_order,created_at,sync_id,version`
	default:
		return nil
	}
	var exists int
	if err := newDB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE sync_id=?`, sid).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	// 跨库无 ATTACH：SELECT 取值 → 参数化 INSERT 回写（两连接各自执行）。
	q := `SELECT ` + cols + ` FROM ` + table + ` WHERE sync_id=? AND user_id=1 LIMIT 1`
	row := oldDB.QueryRow(q, sid)
	ph := strings.TrimSuffix(strings.Repeat("?,", len(strings.Split(cols, ","))), ",")
	ins := `INSERT INTO ` + table + ` (` + cols + `) VALUES (` + ph + `)`
	var args []any
	switch typ {
	case syncspec.EntityTypeNote:
		// ⭐ v21.203：全部用 sql.Null* 承接。
		//
		// 定罪（测试抓出）：原实现对 title/content/content_text/tags/secret 等
		// 直接扫进 `string`/`int`。而这些列在真实库里**可以是 NULL**：
		//   - 加密便签的 content 为 NULL（内容在 encrypted_content）；
		//   - content_text 对纯密文便签为 NULL；
		//   - 老数据/外部导入的 tags 可能为 NULL；
		//   - is_encrypted 若是老行缺省值 0，但若列本身可空就是 NULL。
		// 任何一条命中即 `converting NULL to string is unsupported` ⇒
		// **整个 carryOverOutbox 失败** ⇒ 换代回滚 ⇒ 用户看到「笔记消失」。
		// 即：一条 NULL 的未推送笔记会让全部未推送笔记都保不住。
		var user sql.NullInt64
		var title, content, ct, tags sql.NullString
		var enc sql.NullInt64
		var eph, ec sql.NullString
		var ca, ua sql.NullTime
		var del sql.NullTime
		var ver sql.NullInt64
		var folder sql.NullInt64
		var sync sql.NullString
		var tub sql.NullInt64
		if err := row.Scan(&user, &title, &content, &ct, &tags, &enc, &eph, &ec, &ca, &ua, &del, &ver, &folder, &sync, &tub); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // 实体行已被本地清理：op 保留（push 后服务端幂等面处理）
			}
			return err
		}
		// 同步把可空值转回"可插入"的形态：NULL 原样传 nil（保留 NULL 语义），
		// 非 NULL 取值。时间列若为 NULL 用零值 Time 会被驱动拒绝，故传 nil。
		args = []any{nullInt(user), nullStr(title), nullStr(content), nullStr(ct), nullStr(tags),
			nullInt(enc), nullStr(eph), nullStr(ec), nullTime(ca), nullTime(ua), nullTime(del),
			nullInt(ver), nullInt(folder), nullStr(sync), nullInt(tub)}
	case syncspec.EntityTypeFolder:
		var user sql.NullInt64
		var name sql.NullString
		var sort sql.NullInt64
		var ca sql.NullString
		var sync sql.NullString
		var ver sql.NullInt64
		if err := row.Scan(&user, &name, &sort, &ca, &sync, &ver); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		args = []any{nullInt(user), nullStr(name), nullInt(sort), nullStr(ca), nullStr(sync), nullInt(ver)}
	}
	if res, err := newDB.Exec(ins, args...); err != nil {
		return fmt.Errorf("carry row %s %s: %w", table, sid, err)
	} else if ra, _ := res.RowsAffected(); ra != 1 {
		return fmt.Errorf("carry row %s %s: rows affected %d", table, sid, ra)
	}
	return nil
}

// nullInt / nullStr / nullTime：把 sql.Null* 还原成"可插入参数"。
//
// 语义关键：**NULL 原样传 nil**，不替换成零值 —— 零值会静默改变数据语义
// （例如把 NULL 的 content 写成 ""，服务端会当成"内容被清空"）。
// 非 NULL 才取 Valid 的值。
func nullInt(n sql.NullInt64) any {
	if n.Valid {
		return n.Int64
	}
	return nil
}

func nullStr(n sql.NullString) any {
	if n.Valid {
		return n.String
	}
	return nil
}

func nullTime(n sql.NullTime) any {
	if n.Valid {
		return n.Time
	}
	return nil
}

func setAnchor(db *sql.DB, key string, val int64) error {
	_, err := db.Exec(`INSERT INTO sync_state(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, fmt.Sprint(val))
	return err
}

// CarryOverOutbox 是 carryOverOutbox 的导出面（批次 6 portable 导入复用同一实现，禁双源）：
// 把旧库 PENDING/FAILED/CONFLICT op 迁入新库并重定基（§39/§90/§148；含"账在物在"
// CREATE 实体行搬入与 FOLDER_ORDER 丢弃），返回 (搬运数, CONFLICT 数, FOLDER_ORDER 丢弃数)。
func CarryOverOutbox(oldDB, newDB *sql.DB) (carried, conflicts, dropped int, err error) {
	return carryOverOutbox(oldDB, newDB)
}

// verifyIdentityCarried 换代身份自检（v21.153 R-1/W4 收口）：旧代确有身份行时，
// 新代必须验证 client_profile 在位；缺失则幂等重试一次 carry，仍缺失 → fail-closed
// （Run 失败、新代不接管；重启由 healIdentityAcrossGenerations 兜底修复）。
// 旧代无身份行/不可读 = 无可迁移，不判定失败（离线单机换代属正常路径）。
func verifyIdentityCarried(oldDB, newDB *sql.DB) error {
	countID := func(db *sql.DB) (int, error) {
		var n int
		qerr := db.QueryRow(`SELECT COUNT(*) FROM client_profile WHERE id=1`).Scan(&n)
		return n, qerr
	}
	oldN, oerr := countID(oldDB)
	if oerr != nil || oldN == 0 {
		return nil // 旧代无身份（含表缺失/旧代不可读）：无可迁移，交由调用链其他纪律
	}
	n, verr := countID(newDB)
	if verr != nil {
		return fmt.Errorf("carry verify: %w", verr)
	}
	if n == 0 {
		if rerr := CarryClientIdentity(oldDB, newDB); rerr != nil {
			return fmt.Errorf("carry retry: %w", rerr)
		}
		n, verr = countID(newDB)
		if verr != nil {
			return fmt.Errorf("carry verify: %w", verr)
		}
		if n == 0 {
			return errors.New("carry verify: client_profile missing after carry+retry")
		}
	}
	log.Printf("[bootstrap] identity carried & verified into new generation")
	return nil
}

// MergeAttachments 是 mergeAttachments 的导出面（portable 导入共用"只增不删、
// 同名异内容留给 sha256 自愈轮"语义），返回复制文件数。
func MergeAttachments(srcDir, dstDir string) (int, error) {
	return mergeAttachments(srcDir, dstDir)
}

// carryClientIdentity E4-C：旧代 client_profile 单例行迁入新代（幂等：新代已有身份行则跳过）。
// 新代来自服务端快照，无 desktop 专属表——先 EnsureTables 建表再复制整行
// （device_id/server_url/username/登录时刻/refresh_sealed/secret_sealed/pin_verifier）。
// CarryClientIdentity 导出面（v21.152 启动自愈路径复用同一实现，禁双源）：
// 把旧代 client_profile 单例行迁入目标库（幂等：目标已有身份行则跳过；旧代无表静默跳过）。
func CarryClientIdentity(oldDB, newDB *sql.DB) error {
	if err := localauth.EnsureTables(newDB); err != nil {
		return err
	}
	var n int
	if err := newDB.QueryRow(`SELECT COUNT(*) FROM client_profile`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil // 新代已有身份行（幂等），不覆盖
	}
	rows, err := oldDB.Query(`SELECT device_id, server_url, username, created_at, last_login_at,
		last_revalidate_at, refresh_sealed, secret_sealed, pin_verifier
		FROM client_profile WHERE id = 1`)
	if err != nil {
		// 旧代无此表（更老版本/测试场景）= 无身份可迁，静默跳过。
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	defer rows.Close()
	var deviceID, serverURL, username, createdAt, lastLoginAt, lastRevalidateAt string
	var refreshSealed, secretSealed []byte
	var pinVerifier *string
	found := false
	for rows.Next() {
		if err := rows.Scan(&deviceID, &serverURL, &username, &createdAt, &lastLoginAt,
			&lastRevalidateAt, &refreshSealed, &secretSealed, &pinVerifier); err != nil {
			return err
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if !found {
		return nil // 旧代从未登录：无可迁移身份
	}
	_, err = newDB.Exec(`INSERT INTO client_profile (id, device_id, server_url, username, created_at,
		last_login_at, last_revalidate_at, refresh_sealed, secret_sealed, pin_verifier)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		deviceID, serverURL, username, createdAt, lastLoginAt, lastRevalidateAt,
		refreshSealed, secretSealed, pinVerifier)
	return err
}
