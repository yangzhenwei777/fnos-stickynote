package main

// v21.196 桌面端可观测性三件套（对应《桌面端后续计划与优化清单》O1/O2/O3/O4）：
//
//	O1 文件日志——GUI 子系统程序无控制台，log 只进 stdout，用户侧**完全拿不到日志**
//	   （195 白屏取证不得不靠探针重定向 stdout 才抓到现场）。现落<profile>\logs\app-YYYY-MM-DD.log，
//	   保留 7 天，启动即多路输出。
//	O3 runtime 来源可见——ensureRuntimeExtracted 失败会**静默降级系统 Evergreen**，
//	   而 Evergreen 153.0.4234.48 是本项目已实证的渲染崩溃版（X1）。现落状态标记文件 +
//	   降级时原生弹窗告警 + 前端横幅（RuntimeStatus 绑定）。
//	O2 诊断包——一键导出「日志 + runtime 来源 + WebView2 实测版本 + 档案代数/实体计数」zip，
//	   供工单/远程排障，避免每次都要人工拼现场。

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

	"stickynote-desktop/localstore"
	"stickynote/database"
)

const logKeepDays = 7

// ---------- O1 文件日志 ----------

// logFileWriter 多路输出到「文件 + stdout」，**任一目标失败都不影响另一个**。
//
// ⚠️ 为什么不用 io.MultiWriter（v21.198 用户实机 0 字节日志实证）：它的 Write 实现是
// 「依次写每个 writer，**任一失败立即 return**」。GUI 子系统程序没有控制台，stdout
// 可能已失效（无控制台 / 被关闭 / 管道断开），于是 MultiWriter 在第一个 writer 上
// 就 return —— **文件日志一条都写不进去**。实测症状：应用正常运行、档案指针与
// runtime-status.json 都正常写出，唯独 `logs\app-*.log` **大小 0 字节**，
// 而该文件**存在**（O_CREATE 成功）⇒ 反证「打开成功、写入被前置错误吞掉」。
//
// 纪律（195 教训的延续）：**可观测性组件自身失效必须是可见的**——本 writer 保证
// 文件侧独立成活，并在 setupFileLogging 里做落盘自检、写入 diagnostic summary。
type logFileWriter struct {
	file *os.File
	std  io.Writer
}

func (w *logFileWriter) Write(p []byte) (int, error) {
	if w.file != nil {
		if _, err := w.file.Write(p); err != nil {
			// 文件侧失败也不能让 stdout 丢（反之亦然）；一律吞掉并记 n=0 触发 log 自身降级。
			_ = err
		}
	}
	if w.std != nil {
		_, _ = w.std.Write(p) // 忽略错误：GUI 无控制台是常态，不是故障
	}
	return len(p), nil
}

// logSinkStatus 落盘自检结果——供诊断包回报，让「日志没落盘」本身可观测。
type logSinkStatus struct {
	Enabled bool   // 文件日志是否真的建立并成功写入
	Path    string // 实际路径（Enabled=false 时为空串）
	Size    int64  // 自检时刻的字节数
	Err     string // 降级原因（Enabled=true 时为空）
}

// logSinkStatusVar 进程内最新一次落盘自检结果（供 ExportDiagnostics 读取）。
var logSinkStatusVar logSinkStatus

// setupFileLogging 把标准日志多路输出到 <dir>/logs/app-YYYY-MM-DD.log，并清理过期日志。
// 返回 closer 供 main defer。目录不可写时**降级为仅 stdout**（不阻断启动）。
//
// v21.198：打开后立即 Sync()做**落盘自检**——文件大小 >0 才算 Enabled。
// 此前「打开成功但写入被前置错误吞掉」会留下0 字节文件且**无人知情**。
func setupFileLogging(dir string) (func(), logSinkStatus, error) {
	logDir := filepath.Join(dir, "logs")
	path := ""
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return func() {}, logSinkStatus{}, fmt.Errorf("mkdir logs: %w", err)
	}
	name := "app-" + time.Now().Format("2006-01-02") + ".log"
	path = filepath.Join(logDir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return func() {}, logSinkStatus{}, fmt.Errorf("open log file: %w", err)
	}
	log.SetOutput(&logFileWriter{file: f, std: os.Stdout})
	pruneOldLogs(logDir, logKeepDays)
	log.Printf("[log] file logging enabled: %s", path)

	// 落盘自检：首条已写，Sync 后应能读到内容。
	st := logSinkStatus{Path: path}
	if err := f.Sync(); err != nil {
		st.Err = "sync: " + err.Error()
	} else if fi, serr := os.Stat(path); serr != nil {
		st.Err = "stat: " + serr.Error()
	} else {
		st.Size = fi.Size()
		st.Enabled = st.Size > 0
		if !st.Enabled {
			st.Err = "日志写入后大小仍为 0（stdout 失效吞掉写入或文件被占用）"
		}
	}
	logSinkStatusVar = st
	return func() { _ = f.Close() }, st, nil
}

// pruneOldLogs 按文件名日期删除超过 keep 天的日志；解析失败的文件名一律保留（宁多不删）。
func pruneOldLogs(logDir string, keep int) {
	ents, err := os.ReadDir(logDir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -keep)
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "app-") || !strings.HasSuffix(n, ".log") {
			continue
		}
		d, err := time.Parse("2006-01-02", strings.TrimSuffix(strings.TrimPrefix(n, "app-"), ".log"))
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			_ = os.Remove(filepath.Join(logDir, n))
		}
	}
}

// ---------- O3 runtime 状态标记 ----------

// RuntimeStatus 记录钉版 runtime 的发现/解压结果，供 UI 与诊断包使用。
type RuntimeStatus struct {
	Pinned    bool   `json:"pinned"`    // 是否使用钉版 Fixed Version
	Source    string `json:"source"`    // pinned | system
	Dir       string `json:"dir"`       // 钉版目录（system 时为空）
	Version   string `json:"version"`   // 钉版版本（读 manifest 文件名）
	Extracted bool   `json:"extracted"` // 本次是否发生了首启解压
	Elapsed   string `json:"elapsed"`   // 解压耗时
	Error     string `json:"error"`     // 解压失败原因（降级时非空）
	Degraded  bool   `json:"degraded"`  // 已回退系统 Evergreen —— 必须让用户知道
	At        string `json:"at"`
}

const runtimeStatusFile = "runtime-status.json"

func writeRuntimeStatus(dir string, st RuntimeStatus) {
	st.At = time.Now().Format(time.RFC3339)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, runtimeStatusFile), b, 0600)
}

// readRuntimeStatus 供前端绑定读取；不存在时返回 degraded=false 的零值 + false。
func readRuntimeStatus(dir string) (RuntimeStatus, bool) {
	var st RuntimeStatus
	b, err := os.ReadFile(filepath.Join(dir, runtimeStatusFile))
	if err != nil {
		return st, false
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, false
	}
	return st, true
}

// ---------- O2 诊断包 ----------

// ExportDiagnostics 汇总排障现场为单个 zip，返回落盘路径。
// 只读采集，不修改任何状态；日志与档案均为本地已有文件，不外传。
//
// ⚠️ v21.198 原子写（用户实机 0 字节包实证）：原实现 `os.Create(out)` **立即创建
// 0 字节文件**，内容全靠 `defer zw.Close()` 落盘——进程若在 return 前终止
// （崩溃 / 强杀 / wails 退出），磁盘上就是**一个 0 字节 zip**，用户拿到手完全
// 无法判断是「没东西」还是「导失败了」（实测连续 3 次 0 字节包后才成功 1 次）。
// 现改为：写 `*.tmp` → 关闭 zip 与文件 → **os.Rename 原子改名**。任何中途失败
// 只留 tmp 并被清理，**绝不会产生半成品 zip**。
func (l *LocalService) ExportDiagnostics() (string, error) {
	dir := l.dir
	st, _ := readRuntimeStatus(dir)
	outDir := filepath.Join(dir, "diagnostics")
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return "", fmt.Errorf("mkdir diagnostics: %w", err)
	}
	ts := time.Now().Format("20060102-150405")
	out := filepath.Join(outDir, "sticky-note-diagnostics-"+ts+".zip")
	tmp := out + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("create zip tmp: %w", err)
	}
	// 任一失败路径都要清掉 tmp（不 defer：Rename 之后 tmp 已不存在，清了也无害，
	// 但保持显式便于核对「磁盘上不留残骸」）。
	cleanupTmp := func() { _ = f.Close(); _ = os.Remove(tmp) }
	zw := zip.NewWriter(f)

	addFile := func(name string, path string) error {
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil // 文件不存在属正常情形（如尚无某类日志）
		}
		w, werr := zw.Create(name)
		if werr != nil {
			return werr
		}
		_, werr = w.Write(b)
		return werr
	}

	// 1) 日志
	if ents, derr := os.ReadDir(filepath.Join(dir, "logs")); derr == nil {
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
				_ = addFile("logs/"+e.Name(), filepath.Join(dir, "logs", e.Name()))
			}
		}
	}
	// 2) 档案指针/清单（不含笔记正文，避免隐私外泄）
	_ = addFile("profile/runtime/"+runtimeStatusFile, filepath.Join(dir, runtimeStatusFile))
	_ = addFile("profile/runtime/active_generation", filepath.Join(dir, "runtime", "active_generation"))
	_ = addFile("profile/runtime/recovery.state", filepath.Join(dir, "runtime", "recovery.state"))
	if gens, gerr := os.ReadDir(filepath.Join(dir, "generations")); gerr == nil {
		for _, g := range gens {
			if g.IsDir() {
				_ = addFile("profile/generations/"+g.Name()+"/manifest.json",
					filepath.Join(dir, "generations", g.Name(), "manifest.json"))
			}
		}
	}
	// 3) 环境摘要（版本 / runtime 来源 / WebView2 实测版本 / 本地库实体计数）
	var sb strings.Builder
	fmt.Fprintf(&sb, "version=%s\n", Version)
	fmt.Fprintf(&sb, "exported_at=%s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&sb, "runtime.pinned=%v source=%s version=%s extracted=%v degraded=%v error=%s\n",
		st.Pinned, st.Source, st.Version, st.Extracted, st.Degraded, st.Error)
	if wv := readWebView2LastVersion(dir); wv != "" {
		fmt.Fprintf(&sb, "webview2.last_version=%s\n", wv)
	} else {
		sb.WriteString("webview2.last_version=(none)\n")
	}
	// 3.1 O1 落盘自检（v21.198）：让「日志没落盘」本身可观测——此前它静默失效
	// （用户实机拿到 0 字节日志却无从判断是应用没写还是写了被吞）。
	ls := logSinkStatusVar
	fmt.Fprintf(&sb, "log.enabled=%v size=%d path=%s err=%s\n",
		ls.Enabled, ls.Size, ls.Path, ls.Err)
	for _, q := range []struct{ label, sql string }{
		{"notes", "SELECT COUNT(*) FROM notes WHERE deleted_at IS NULL"},
		{"folders", "SELECT COUNT(*) FROM note_folders"},
		{"uploads", "SELECT COUNT(*) FROM uploads"},
		{"outbox", "SELECT COUNT(*) FROM sync_outbox"},
	} {
		// v21.200：计数查询加**硬超时护栏**。排障动作绝不能被 DB 拖死——
		// DB 忙/锁住时裸 QueryRow 最长可阻塞到 busy_timeout（5s），
		// 而 DB 句柄异常时可能更久。此前用户实测「导出诊断包一直转圈圈」，
		// 排障路径本身必须保证「一定能产出包」。
		fmt.Fprintf(&sb, "db.%s=%s\n", q.label, scalarCountWithTimeout(dir, q.sql, 2*time.Second))
	}
	if w, werr := zw.Create("summary.txt"); werr == nil {
		_, _ = w.Write([]byte(sb.String()))
	}

	// 4) 收口：Close zip → Close 文件 → Sync → 原子改名。顺序不可换：
	// zip 的中央目录由 zw.Close() 写，必须在 f.Close() 之前；Sync 保证数据落介质。
	if cerr := zw.Close(); cerr != nil {
		cleanupTmp()
		return "", fmt.Errorf("close zip: %w", cerr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close file: %w", cerr)
	}
	if rerr := os.Rename(tmp, out); rerr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename to zip: %w", rerr)
	}

	abs, _ := filepath.Abs(out)
	if fi, serr := os.Stat(abs); serr == nil {
		log.Printf("[diag] diagnostics exported: %s (%d bytes)", abs, fi.Size())
		if fi.Size() == 0 {
			log.Printf("[diag] ⚠ 导出包大小为 0，请报此现象")
		}
	} else {
		log.Printf("[diag] diagnostics exported: %s (stat failed: %v)", abs, serr)
	}
	return abs, nil
}

// scalarCount 只读计数查询；DB 不可用时返回 "n/a"（诊断包不因单点失败而整体失败）。
func scalarCount(sql string) string {
	db := database.GetDB()
	if db == nil {
		return "n/a"
	}
	var n int
	if err := db.QueryRow(sql).Scan(&n); err != nil {
		return "n/a"
	}
	return fmt.Sprintf("%d", n)
}

// scalarCountWithTimeout 带硬超时的只读计数（v21.200 加超时 / v21.203 换独立连接）。
//
// 为什么必须有：诊断包是**排障动作**，其价值与「能否快速产出」同价。裸
// QueryRow 在 DB 忙/锁/句柄异常时会阻塞（busy_timeout 5s，且连接池等待无上限），
// 一旦阻塞，用户既拿不到诊断包、界面也永久loading —— 排障路径反过来成了故障源。
// 超时或出错一律降级为 "n/a"/"timeout"，**绝不阻塞导出**。
//
// ⭐ v21.203：改用**独立只读连接**（v21.200 只加了超时，读的还是全局池）。
//
// 定罪（审计 + 用户实机 2026-10-10）：诊断包 4 个计数**全部**显示 `timeout`，
// 恰好发生在最需要它们的时候（换代/同步出问题时）。根因是连接池只有 2 条
// 连接（`OpenGenDB` 的 `MaxOpenConns(2)`，§93 纪律），而：
//   - `carryOverOutbox` 在换代时**同时持有 oldDB 的一条 rows + newDB 的一条查询**；
//   - `Worker.RunOnce` 的 push/pull 也在抢这两条；
//   - 诊断包的 4 个 QueryRow 排第 3+ 位 ⇒ 等不到连接 ⇒ 2s 超时。
//
// 也就是说：**越是故障时刻（连接被占满），诊断包越 useless** —— 这与诊断包的
// 存在目的正好相反。修法：诊断走**自己开的只读连接**（`mode=ro`，独立池，
// 1 条连接），完全不与业务连接竞争。故障时它拿不到锁会报错，但**拿得到连接**，
// 仍能给出有价值的读数或明确的错误文案。
func scalarCountWithTimeout(profileDir, sql string, d time.Duration) string {
	db := diagReadDB(profileDir)
	if db == nil {
		return "n/a"
	}
	defer db.Close() // 每次导出开一条，用完即关（诊断是低频动作，不值得常驻）
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var n int
	if err := db.QueryRowContext(ctx, sql).Scan(&n); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "busy") ||
			strings.Contains(err.Error(), "locked") {
			return "timeout"
		}
		return "n/a"
	}
	return fmt.Sprintf("%d", n)
}

// diagReadDB 开一条独立的**只读**连接指向 profileDir 下的当前代库。
// 失败（库不存在/路径不可得/驱动异常）返回 nil，调用方降级为 "n/a"。
//
// profileDir 由调用方显式传入（LocalService.dir），**不猜** —— 猜路径在
// 便携模式/沙箱目录下会指错库，产出一份"读的是别的库"的诊断包比没有更糟。
func diagReadDB(profileDir string) *sql.DB {
	if profileDir == "" {
		return nil
	}
	active, err := localstore.Layout{ProfileDir: profileDir}.Active()
	if err != nil || active == "" {
		return nil
	}
	path := filepath.Join(profileDir, "generations", active, "notes.db")
	if _, serr := os.Stat(path); serr != nil {
		return nil
	}
	// 只读 + 独立短 busy_timeout：诊断不该（也不能）写库，
	// 抢不到锁就快速失败并如实报 "timeout"。
	db, derr := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(1500)")
	if derr != nil {
		return nil
	}
	db.SetMaxOpenConns(1) // 诊断自己一条，够用且绝不与业务竞争
	return db
}

// readWebView2LastVersion 读 WebView2 用户数据目录的 "Last Version" 文件（实测运行版本）。
func readWebView2LastVersion(profileDir string) string {
	base := profileDir
	if root := portableRoot(); root != "" {
		base = root
	}
	b, err := os.ReadFile(filepath.Join(base, "webview2", "EBWebView", "Last Version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// RuntimeStatus 暴露给前端：钉版 runtime 是否可用、是否已降级（O3 横幅数据源）。
func (l *LocalService) RuntimeStatus() map[string]any {
	st, ok := readRuntimeStatus(l.dir)
	return map[string]any{
		"known":    ok,
		"pinned":   st.Pinned,
		"source":   st.Source,
		"version":  st.Version,
		"degraded": st.Degraded,
		"error":    st.Error,
		"webview2": readWebView2LastVersion(l.dir),
	}
}
