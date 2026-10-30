// Command sticky-note-desktop —— 离线客户端 Windows 壳（离线方案 v20-Project P0-A）。
//
// 链路（§12）：Vue → LocalTransport(axios adapter，后续切片) → Wails Binding → App Facade
// → 本地 SQLite。**无 localhost HTTP 监听**（§13，红线 29）。
//
// P0-A 接线切片覆盖面：CRUD/搜索（ListNotes.search）、Folder、Encrypt（与 handler 同语义的
// app 层实现）、Upload（bytes 进 /uploads 出 + 资源 overlay 直读）。同步不做（§123）。
//
// 复用纪律（§121）：database/model/app 经 go.mod `replace stickynote => ../server` 引用，
// 禁止复制源码。骨架期本地库跑服务端全量迁移——§96（Desktop 不跑 server-only migration）
// 的 core/client 分层收敛属 P0-B/P2 待办。
package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"stickynote/app"
	"stickynote/database"
	"stickynote/model"

	"stickynote-desktop/synclocal"
)

//go:embed all:frontend/dist
var assets embed.FS

// desktopSPAFallback 桌面资源服务器的 SPA 兜底（v21.141 修正）。
//
// ⚠️ 为什么是「302 跳到根路径」而**不是**「直接吐 index.html」：
// Wails v2 **只在请求路径以 `/` 或 `/index.html` 结尾时**才做 runtime 注入——
// 见 wails/v2 `pkg/assetserver/assetserver.go`：`isRuntimeInjectionMatch()` 判定后走
// `bodyRecorder` + `processIndexHTML()`，注入 `<script src="/wails/runtime.js">` 与 IPC 脚本，
// 而**那才是 `window.go` 绑定桥的唯一来源**。
//
// v21.140 的本类实现对一个未命中的深链接（如 `/login`、`/profile`）直接返回 index.html：
// 路径不满足注入条件 → 走进 `else { handler.ServeHTTP }` 分支 → 页面渲染正常但
// **`window.go` 不存在** → 所有绑定调用抛「桌面桥不可用」。
// 真机实测触发链：退出登录 → `request.js` 的 401 分支 `window.location.replace('/login')`
// 整页导航 → 登录页无桥 → **无法再登录**。这比原来的 404 更糟：404 至少看得出坏了。
//
// 现改为：把原路径经 query 交给前端还原（302 → `/?_spa=%2Flogin`）。根路径必然被注入，
// 前端启动时读 `_spa` 用 `history.replaceState` + `router.replace` 还原真实路由
// （客户端导航，不触发文档重载，桥保持有效）。带扩展名的缺失资源照旧 404。
type desktopSPAFallback struct{}

func (desktopSPAFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 非 GET/HEAD：资源服务器对非 GET 请求一律转交本 Handler，这里明确拒绝，
	// 不静默回 200（避免把"打错方法的请求"伪装成成功）。
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// 带扩展名 = 静态资源引用缺失，必须 404；回 HTML 会让浏览器把 HTML 当 JS 执行（v21.24 同源教训）。
	if path.Ext(r.URL.Path) != "" {
		http.NotFound(w, r)
		return
	}
	target := "/"
	if p := r.URL.Path; p != "" && p != "/" {
		target = "/?_spa=" + url.QueryEscape(p)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// Version 桌面构建版本（批次 7 版本注入链）：构建脚本经
// -ldflags "-X main.Version=$(cat VERSION)" 注入；`sticky-note-desktop.exe -version`
// 回读（发布 gate 断言物）。禁止手改常量语义——dev 值仅出现在未注入的临时构建。
var Version = "0.0.0-dev"

// overlayFS 在嵌入前端之上叠加本地附件目录：请求路径尾段命中 uploads/<file> 时读磁盘，
// 其余透传给 embed。HTML 里 `/uploads/x.png` 桌面与 Web 完全同形（LocalTransport
// 未来只需处理 /api，不碰资源路径）。守卫：仅接受末段文件名，拒绝任何再嵌套。
type overlayFS struct {
	web       fs.FS
	uploadDir string
}

const uploadsMarker = "uploads/"

func (o overlayFS) Open(name string) (fs.File, error) {
	if i := strings.LastIndex(name, uploadsMarker); i >= 0 {
		base := name[i+len(uploadsMarker):]
		if base != "" && base == filepath.Base(base) && !strings.ContainsAny(base, `/\`) {
			return os.Open(filepath.Join(o.uploadDir, base))
		}
		return nil, fs.ErrNotExist
	}
	return o.web.Open(name)
}

// UploadResult binding 多值返回走结构体 DTO（wails 生成 TS class 更干净）。
type UploadResult struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

// LocalService Wails 绑定面：方法签名即 JS 侧 API（Go 元数据自动生成 TS，勿手改 d.ts）。
type LocalService struct {
	ctx             context.Context
	dir             string // 档案根（v21.196：诊断包/runtime 状态标记按此定位）
	notes           *app.NoteService
	folders         *app.FolderService
	uploader        *app.UploadService
	uploadDir       string // 回收站物理删除的孤儿附件清理用（local_extras.go）
	userID          int64  // P0-A 单用户开发档；P3 LocalAuth（DPAPI 设备会话）后注入
	deviceID        string // 批次 4 刀 2：client_profile 设备号（登录/恢复两路共写）
	sessionRestored bool   // 冷启动设备会话恢复成功（AuthStatus 可见性）
	unlockedSecret  []byte // 刀 3：PIN 解锁得到的 device_secret（仅内存）
}

func NewLocalService(profileDir string) *LocalService {
	up := filepath.Join(profileDir, "uploads")
	ls := &LocalService{
		dir:       profileDir,
		notes:     app.NewNoteService(100000),
		folders:   app.NewFolderService(),
		uploader:  app.NewUploadService(up),
		uploadDir: up,
		userID:    1,
	}
	lsInstance = ls // 刀 3：mountWorker 等包级路径回写会话态用
	// v2.3.0：远端把文件夹锁定后，本进程的解锁授权立即作废（fail-closed）。
	synclocal.FolderLockChangedHook = func(folderID int64, encrypted bool) {
		if encrypted {
			ls.forgetFolderUnlock(folderID)
		}
	}
	return ls
}

// lsInstance 最近构造的服务门面（本进程恒定单实例；测试经 NewLocalService 自动登记）。
var lsInstance *LocalService

func (l *LocalService) Startup(ctx context.Context) {
	l.ctx = ctx
	// 后台同步调度器随壳启动（批次8-Alt 刀3）。是否真跑由 Allow 门决定：未持票/离线单机态
	// 一律零副作用，所以"启动即挂调度器"不等于"启动即联网"。
	startScheduler(l)
}

// ---- 笔记 ----

func (l *LocalService) CreateNote(title, content, tags string, folderID *int64) (*model.Note, error) {
	if err := l.ensureFolderUnlocked(folderID); err != nil {
		return nil, err
	}
	n, err := l.notes.CreateNote(l.userID, title, content, tags, folderID)
	if err == nil {
		l.enqueueNote(n.ID, "CREATE")
		wakeScheduler() // v21.147 实时同步：变更入队后立刻触发同步
	}
	return n, err
}

func (l *LocalService) UpdateNote(id int64, title, content, tags string, baseVersion int64) (*model.Note, error) {
	n, err := l.notes.UpdateNote(id, l.userID, title, content, tags, baseVersion)
	if err == nil {
		l.enqueueNote(id, "UPDATE")
		wakeScheduler() // v21.147 实时同步：变更入队后立刻触发同步
	}
	return n, err
}

func (l *LocalService) GetNote(id int64) (*model.Note, error) {
	// P1-1：只读绑定统一超时护栏（详见 db_guard.go 顶部取舍说明）
	return dbReadGuard(func() (*model.Note, error) {
		locked, err := model.FolderLockedForNote(id, l.userID, l.folderGrantSet())
		if err != nil {
			return nil, err
		}
		if locked {
			return nil, model.ErrFolderLocked
		}
		return l.notes.GetNote(id, l.userID)
	})
}

func (l *LocalService) ListNotes(page, pageSize int, q, tag, sortBy string, folderID *int64) (*model.NoteListResponse, error) {
	return dbReadGuard(func() (*model.NoteListResponse, error) {
		return l.notes.ListNotes(l.userID, page, pageSize, q, tag, sortBy, folderID, l.folderGrantSet())
	})
}

func (l *LocalService) DeleteNote(id int64) error {
	if err := l.notes.DeleteNote(id, l.userID); err != nil {
		return err
	}
	l.enqueueNote(id, "DELETE")
	wakeScheduler() // v21.147 实时同步：变更入队后立刻触发同步
	return nil
}

func (l *LocalService) MoveNote(id int64, folderID *int64) (int64, error) {
	if err := l.ensureFolderUnlocked(folderID); err != nil {
		return 0, err
	}
	// 桌面本地路径：返回版本即当前本地版本（本地移动不动 version，v21.87 语义保持）
	// 返回值供 LocalTransport 复刻 handler 的 {message, version} 响应（开发纪律 A-15）。
	version, err := l.notes.MoveNoteToFolder(id, l.userID, folderID)
	if err != nil {
		return 0, err
	}
	l.enqueueNote(id, "UPDATE")
	wakeScheduler() // v21.147 实时同步：变更入队后立刻触发同步
	return version, nil
}

// ---- 加密（与 handler/note_encrypt.go 同语义，SOURCE-FACT §3 纪律逐条内嵌） ----

func (l *LocalService) EncryptNote(id int64, password, confirmPassword string) error {
	// 桌面本地路径：返回版本即当前本地版本（本地加密不 bump，§25 P0-1 语义保持）
	if _, err := l.notes.EncryptNote(id, l.userID, password, confirmPassword); err != nil {
		return err
	}
	l.enqueueNote(id, "UPDATE")
	wakeScheduler() // v21.147 实时同步：变更入队后立刻触发同步
	return nil
}

func (l *LocalService) DecryptNote(id int64, password string) (string, error) {
	p, err := l.notes.DecryptNote(id, l.userID, password)
	if err == nil {
		l.enqueueNote(id, "UPDATE")
		wakeScheduler() // v21.147 实时同步：变更入队后立刻触发同步
	}
	return p, err
}

func (l *LocalService) VerifyEncrypt(id int64, password string) (*model.Note, error) {
	return dbReadGuard(func() (*model.Note, error) { return l.notes.VerifyEncrypt(id, l.userID, password) })
}

func (l *LocalService) UpdateEncrypted(id int64, title, tags, plaintext, password string, baseVersion int64) (*model.Note, error) {
	n, err := l.notes.UpdateEncrypted(id, l.userID, title, tags, plaintext, password, baseVersion)
	if err == nil {
		l.enqueueNote(id, "UPDATE")
		wakeScheduler() // v21.147 实时同步：变更入队后立刻触发同步
	}
	return n, err
}

// ---- 文件夹 ----

func (l *LocalService) ListFolders() ([]model.Folder, error) {
	return dbReadGuard(func() ([]model.Folder, error) {
		return l.folders.List(l.userID, l.folderGrantSet())
	})
}

// 文件夹 binding 的 Outbox 入队（P1 批次2，§25 Dirty 规则；未登录零副作用同 enqueueNote）。
func (l *LocalService) enqueueFolder(folderID int64, operation string) {
	if workerPtr() == nil {
		return
	}
	if err := synclocal.EnqueueFolderOp(database.DB, folderID, operation); err != nil {
		log.Printf("[sync] enqueue %s folder %d failed: %v", operation, folderID, err)
	}
}

func (l *LocalService) CreateFolder(name string) (*model.Folder, error) {
	f, err := l.folders.Create(l.userID, name)
	if err == nil && f != nil && f.ID != nil {
		l.enqueueFolder(*f.ID, "CREATE")
	}
	return f, err
}

func (l *LocalService) RenameFolder(id int64, name string) error {
	if err := l.folders.Rename(id, l.userID, name); err != nil {
		return err
	}
	l.enqueueFolder(id, "UPDATE")
	return nil
}

func (l *LocalService) MoveFolder(id int64, position string) error {
	if err := l.folders.Move(id, l.userID, position); err != nil {
		return err
	}
	// 重排是集合级意图：入队单条 FOLDER_ORDER op（本地全序 + 逐夹 base，§55）。
	if workerPtr() != nil {
		if err := synclocal.EnqueueFolderOrderOp(database.DB); err != nil {
			log.Printf("[sync] enqueue FOLDER_ORDER failed: %v", err)
		}
	}
	return nil
}

func (l *LocalService) DeleteFolder(id int64) error {
	if err := l.ensureFolderUnlocked(&id); err != nil {
		return err
	}
	// 删除后行不存在——入队所需引用必须在物理删前捕获（含 base_version）。
	ref, ok := synclocal.GetFolderRef(database.DB, id)
	if err := l.folders.Delete(id, l.userID); err != nil {
		return err
	}
	l.forgetFolderUnlock(id)
	if ok && workerPtr() != nil {
		if err := synclocal.EnqueueFolderDeleteOp(database.DB, ref); err != nil {
			log.Printf("[sync] enqueue folder DELETE %s failed: %v", ref.SyncID, err)
		}
	}
	return nil
}

// ---- 附件（bytes 进，/uploads 出；预览经 overlayFS 直读） ----

func (l *LocalService) UploadBytes(originalName string, data []byte) (*UploadResult, error) {
	name, url, err := l.uploader.SaveBytes(l.userID, originalName, data)
	if err != nil {
		return nil, err
	}
	// P1 批次3：登记上传账本（push 预传据此免传/重传；sha 内存直算）。
	h := sha256.Sum256(data)
	if jerr := synclocal.MarkAttachmentLocal(database.DB, name, hex.EncodeToString(h[:]), int64(len(data))); jerr != nil {
		log.Printf("[sync] attachment journal mark failed %s: %v", name, jerr)
	}
	return &UploadResult{Filename: name, URL: url}, nil
}

// AppInfo 就绪探测与运行参数回传。
func (l *LocalService) AppInfo() map[string]any {
	return map[string]any{"desktop": true, "phase": "P0-A-wiring"}
}

// localDataDir §80 骨架子集：profiles/<profile>/ 下一切；开发档与生产隔离。
// portableRoot 便携（绿色）版数据根：exe 同目录存在 `portable.mode` 标记时启用。
// 用户需求（2026-09-23）：绿色版全部数据落在 exe 同目录 `<exeDir>\sticky-note-data\`，
// 删除该目录（连同 exe）后本机不留任何笔记/凭据残留，防数据泄露。
// 标记文件由 build-desktop.ps1 打进便携 zip；安装版（NSIS）不带标记 → 走 LOCALAPPDATA，
// 行为完全不变。返回空串 = 非便携模式。
func portableRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	if _, serr := os.Stat(filepath.Join(dir, "portable.mode")); serr != nil {
		return ""
	}
	return filepath.Join(dir, "sticky-note-data")
}

// cleanupStrayWebViewData 便携模式：清理 %APPDATA%\<exeBase> 下的 WebView2 壳数据。
// 背景（2026-09-23 实测）：Wails 的 WebviewUserDataPath 已把主体数据（Default/localStorage）
// 重定向到便携目录，但 WebView2 loader 层仍会在默认路径留下 BrowserMetrics/Crashpad
// ——崩溃转储可能含页面内容，违背绿色版「删除即无残留」语义。
// 策略：启动与退出各清一次（运行期间可能被重建；正常退出后终态干净；异常退出由下次启动兜底）。
func cleanupStrayWebViewData() {
	if portableRoot() == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		return
	}
	stray := filepath.Join(appdata, filepath.Base(exe))
	if _, serr := os.Stat(stray); serr != nil {
		return
	}
	if rerr := os.RemoveAll(stray); rerr != nil {
		log.Printf("[portable] clean stray webview data failed: %v", rerr)
		return
	}
	log.Printf("[portable] cleaned stray webview data: %s", stray)
}

func localDataDir() string {
	// E2E 走查/多实例隔离口：工装传 STICKYNOTE_PROFILE_DIR 指向沙箱档案。
	// ⚠️ 不得用重定向 LOCALAPPDATA 来隔离——实测会令 WebView2 渲染进程
	// kind 4/6 失败循环直至主进程退出（2026-09-22 L0 与 09-23 复现）。
	if p := os.Getenv("STICKYNOTE_PROFILE_DIR"); p != "" {
		return p
	}
	// 便携模式优先：数据随 exe 走（删除即无残留）。
	if root := portableRoot(); root != "" {
		return filepath.Join(root, "profile")
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".localappdata-fallback")
	}
	return filepath.Join(base, "StickyNoteDev", "profiles", "localdev")
}

// ensureLocalUser 本地单用户行：仅满足 FK（§98，不作离线认证权威）。
func ensureLocalUser() error {
	_, err := database.DB.Exec(
		`INSERT OR IGNORE INTO users (id, username, password_hash, role) VALUES (1, 'local', '!', 'user')`)
	return err
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-version" { // 发布 gate 断言面（不起窗）
		fmt.Println(Version)
		return
	}
	dir := localDataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Fatalf("mkdir local data: %v", err)
	}
	// v21.169 单实例互斥（用户实机：反复开关后掉登录、笔记消失）。
	// 两个进程并存时各自换票 —— 而服务端 refresh 是「一次一换 + 族重放吊销」，
	// 进程内的 exchangeMu 拦不住跨进程并发，必然有一方花掉另一方在用的封存票 ⇒ 整族吊销。
	// 按档案目录命名（绿色版与安装版数据目录不同，本就该并存，互不误伤）。
	if release, ok := acquireSingleInstance(dir); ok {
		defer release()
	} else {
		log.Printf("[single-instance] 已有实例在运行，本进程退出（避免并发换票触发族重放吊销）")
		return
	}
	// v21.196 O1：GUI 程序无控制台，日志只进stdout → 用户侧完全拿不到。
	// 落<档案>\logs\app-YYYY-MM-DD.log（保留 7 天），失败仅降级不阻断启动。
	// v21.198：新增落盘自检返回值——文件被创建但始终 0 字节（写入被 stdout 失效吞掉）
	// 的场景必须让它**在启动日志与诊断包summary 里可见**，不能静默。
	if closeLog, logSt, err := setupFileLogging(dir); err != nil {
		log.Printf("[log] file logging unavailable (stdout only): %v", err)
	} else {
		defer closeLog()
		if !logSt.Enabled {
			log.Printf("[log] ⚠ 文件日志已建立但落盘自检失败：size=%d err=%s", logSt.Size, logSt.Err)
		}
	}
	// 便携模式：启动即清理上次运行残留的 WebView2 壳数据（%APPDATA%\<exeBase>）。
	cleanupStrayWebViewData()
	// 批次 4 刀 1：本地库经 localstore 启动链接管（Recover→指针→OpenGenDB，首装自动
	// provision 首代并原子落盘；旧骨架库一次性快照迁移）。fail-closed：失败不起窗。
	//
	// v21.158 · N3 修法①（用户 2026-10-09 拍板）：损坏一律 fail-closed **不改**（不自动回落
	// lkg、不删任何数据），但**必须告诉用户发生了什么、接下来做什么**——此前只有一行
	// log.Fatalf 落到文件日志，GUI 用户侧表现为「双击没反应」，无从处置。
	// ⚠️ 为什么文案指向**日志文件**而非一键诊断包：ExportDiagnostics() 是 Wails 绑定，
	// 必须起窗才可调用，而损坏场景恰恰起不了窗 ⇒ 唯一可及的证据面是 logs/app-*.log
	//（本行的 setupFileLogging 已在 boot 之前就绪，见上）。
	gen, err := bootLocalstore(dir)
	if err != nil {
		log.Printf("[localstore] boot failed (fail-closed, data intact): %v", err)
		notifyNative(mbIconWarning, "Sticky note 本地库无法打开",
			"程序已停止启动，以避免在不完整的数据状态下继续运行。\n"+
				"你的数据没有被删除，也不会被自动清理。\n\n"+
				"建议按顺序尝试：\n"+
				"1. 升级到最新版本，覆盖安装后再试（多数情况可解决）；\n"+
				"2. 仍然失败 → 把日志文件发给技术支持：\n"+
				"   "+filepath.Join(dir, "logs")+"\\app-*.log\n"+
				"3. 请勿重装系统、请勿删除数据目录、请勿用外部工具改库文件。\n\n"+
				"技术信息："+err.Error())
		log.Fatalf("localstore boot: %v", err)
	}
	log.Printf("localstore active generation %s (profile=%s)", gen, dir)
	defer database.GetDB().Close() // 捕获时点句柄经锁读；启动后本进程不再换库

	// ⭐ v21.177：DB 连接池饱和看门狗——216 连接池死锁定案的观测面：
	// 同类「静默瘫痪」若再出现，必须可见（日志）+ 可取证（goroutine 转储落 logs/）。
	startDBWatchdog(dir)

	ls := NewLocalService(dir)
	if err := ls.restoreDeviceSession(dir); err != nil {
		log.Printf("[auth] device session restore probe failed (non-fatal): %v", err)
	}
	// E4-5：会话恢复路径同样触发 folders 空档的自动快照（此前只挂 RemoteLogin，
	// 「双击直接进已登录会话」的场景永远不触发 → 文件夹永不落地，真机实锤）。
	go ls.maybeAutoBootstrap()

	// ⭐⭐ v21.222（215 实机 + 用户直接质疑「为什么不能打开后立即联网」）：
	// **启动即换票**，不等调度器第一轮。
	//
	// 215 实机：每次重启都出现「恢复中」，且**点【立即重试】也无效**，日志显示
	//	`[shutdown] 换票仍在途（>16s），放弃等待` —— 说明换票锁被长时间持有，
	//	连用户手动触发的 SyncNow 都要排队等锁（它同样走 ensureAccess 抢锁）。
	// 用户网络一直正常（内网实测 /api/auth/refresh **3ms** 返回 401），
	// 所以「恢复中」**不是网络问题，是没人去触发换票**。
	//
	// 为什么之前没人触发：
	//   · `restoreDeviceSession` 只**挂空票 worker**（access 只在内存，重启必空）；
	//   · 换票由 `ensureAccess` 惰性完成，而它只在
	//     「调度器第一轮 / 用户手动 SyncNow / BootstrapNow / RevalidateNow」时才被调；
	//   · 调度器第一轮还要等 `FirstDelay`，且若上一轮把退避/熔断计数带进来会更晚。
	// ⇒ 表现为「明明有凭据、网络也好，却一直显示恢复中」。
	//
	// 修法：**启动完成前主动换一次票**（独立 goroutine，不阻塞起窗）。
	// 网络类失败照旧保留封存票（不丢凭据），下一轮调度器会再试；
	// 成功则 `workerHasToken` 立即为真，UI 直接显示「已连接」。
	//
	// ⚠ 为什么放 goroutine 而不是同步调用：换票最长 15s（+弱网重试），
	//   同步调用会把起窗拖到十几秒，用户看到的是「点了没反应」。
	go func() {
		if ok, err := ls.ensureAccessOnStartup(); err != nil {
			log.Printf("[auth] 启动换票未成功（保留本机凭据，将由后台重试）: %v", err)
		} else if ok {
			log.Printf("[auth] 启动换票成功，已连接服务器")
		}
	}()

	// v21.194 方案 A（包体积瘦身，用户拍板）：钉版 runtime 以 webview2-runtime.tar.xz
	// 随包分发（裁剪 Locales 至 zh-CN/en-US + 去 WidevineCdm + xz 高压缩，构建脚本产出），
	// 首次启动解压到 exe 同目录。必须在 wails.Run 之前完成（WebviewBrowserPath 配置期读取）。
	// v21.196 O4：解压约 7s/424MB，此前**完全静默**（用户以为程序没反应）→ 起窗前给原生提示；
	// O3：解压失败会静默降级系统 Evergreen（153.0.4234.48 是已证崩溃版）→ 落状态标记 + 弹窗告警。
	if runtimeFirstRunPending(dir) {
		notifyNative(mbIconInformation, "Sticky note 首次启动",
			"首次启动需要解压内置运行组件（约 7 秒 / 424 MB），用于摆脱系统浏览器内核的兼容问题。\n"+
				"点击「确定」继续，解压完成后自动进入主界面。")
	}
	st := ensureRuntimeExtracted(dir)
	writeRuntimeStatus(dir, st)
	if st.Degraded {
		log.Printf("[webview2] degraded to system Evergreen: %s", st.Error)
		notifyNative(mbIconWarning, "Sticky note 运行组件告警",
			"内置运行组件（WebView2）不可用，已回退系统浏览器内核。\n"+
				"系统内核版本可能存在渲染异常（表现为界面空白或崩溃）。\n"+
				"建议：重新安装本应用，或删除程序目录下的 webview2-runtime 文件夹后重新启动以触发解压。\n"+
				"技术信息："+st.Error)
	}

	if err := wails.Run(&options.App{
		Title:  "Sticky note",
		Width:  1200,
		Height: 800,
		Windows: func() *windows.Options {
			o := &windows.Options{
				// E1 钉版 WebView2 Fixed Version（2026-09-23 用户拍板）：exe 同目录
				// webview2-runtime\ 含 msedgewebview2.exe 即启用（随安装器/便携包分发）；
				// 缺省空串=系统 Evergreen。背景：Evergreen 升级到 153 后渲染进程
				// kind 4/6 崩溃循环（用户机+本机双实证），钉版 133.0.3065.92 彻底摆脱升级影响。
				WebviewBrowserPath: fixedWebview2Dir(dir),
			}
			// 便携模式：WebView2 用户数据同样落 exe 同目录（否则 %APPDATA% 残留
			// EBWebView 会留下登录态/缓存，违背"删除即无残留"的绿色版语义）。
			if root := portableRoot(); root != "" {
				o.WebviewUserDataPath = filepath.Join(root, "webview2")
			}
			return o
		}(),
		AssetServer: &assetserver.Options{
			Assets:  overlayFS{web: assets, uploadDir: filepath.Join(dir, "uploads")},
			Handler: desktopSPAFallback{},
		},
		OnStartup: ls.Startup,
		OnShutdown: func(context.Context) {
			// v21.203：关机收口必须走 gracefulShutdown（先取消 → 再停调度器），
			// 详见 shutdown.go 顶部的定罪。旧写法只调 stopScheduler()，
			// 放弃等待后调度/换代 goroutine 仍在跑，而 DB 与日志随后就被关掉
			// ⇒ "database is closed" 报错 + **故障证据丢失**。
			gracefulShutdown()
			// 换代链（BootstrapNow）不受调度器管辖：给它一个有界收尾窗口，
			// 让「后置步骤 + SealCommitted」跑完，避免每次正常关机都白等一次换代
			// （详见 shutdown.go 的 awaitInFlightGrace）。
			if !awaitInFlightGrace() {
				log.Printf("[shutdown] 换代在关机窗口内未收尾，交由 SEALED 崩溃态兜底（安全回退，不丢数据）")
			}
			// 便携模式：退出前清 WebView2 壳数据 —— 保证"删 exe + 数据目录后无残留"的终态。
			cleanupStrayWebViewData()
		},
		Bind: []any{ls},
	}); err != nil {
		log.Fatalf("wails run: %v", err)
	}
}

// runtimeFirstRunPending：钉版 runtime 缺失且压缩包在位 → 即将发生首启解压（用于起窗前提示）。
func runtimeFirstRunPending(profileDir string) bool {
	if fixedWebview2Dir(profileDir) != "" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(filepath.Dir(exe), "webview2-runtime.tar.xz"))
	return err == nil
}

// ensureRuntimeExtracted v21.194 方案 A（包体积瘦身，用户拍板）：钉版 runtime 以
// webview2-runtime.tar.xz 随包分发（构建脚本裁剪 Locales 至 zh-CN/en-US + 去
// WidevineCdm 后 xz 高压缩产出，558MB→约 150MB），首次启动解压到 exe 同目录
// （Windows 10 1803+ 自带 bsdtar 原生支持 xz，实测 7s/424MB）。解压失败降级系统
// Evergreen（fixedWebview2Dir 既有行为）。仅当钉版 runtime 不在位时才尝试。
// v21.196 O3：返回 RuntimeStatus 并由调用方落标记 + 弹窗告警——降级不再静默。
func ensureRuntimeExtracted(profileDir string) RuntimeStatus {
	st := RuntimeStatus{Source: "system"}
	if d := fixedWebview2Dir(profileDir); d != "" {
		st.Pinned, st.Dir, st.Source = true, d, "pinned"
		st.Version = pinnedRuntimeVersion(d)
		return st
	}
	exe, err := os.Executable()
	if err != nil {
		st.Error = "locate exe: " + err.Error()
		st.Degraded = true
		return st
	}
	dir := filepath.Dir(exe)
	xzPath := filepath.Join(dir, "webview2-runtime.tar.xz")
	if _, err := os.Stat(xzPath); err != nil {
		// 无压缩包：走 Evergreen 降级（fixedWebview2Dir 已记日志）
		st.Error = "webview2-runtime.tar.xz 缺失"
		st.Degraded = true
		return st
	}
	log.Printf("[webview2] first-run: extracting pinned runtime (one-time, ~10s)…")
	t0 := time.Now()
	// 显式用 System32 的 bsdtar（libarchive 含 xz）：PATH 上的 GNU tar 依赖 Git Bash
	// 环境，安装版用户的 PATH 不可假设；SystemRoot 为空（异常环境）则放弃降级 Evergreen。
	sysTar := filepath.Join(os.Getenv("SystemRoot"), "System32", "tar.exe")
	if _, err := os.Stat(sysTar); err != nil {
		st.Error = "system tar not found"
		st.Degraded = true
		log.Printf("[webview2] system tar not found, cannot extract runtime — falling back to system Evergreen")
		return st
	}
	out, err := exec.Command(sysTar, "-xf", xzPath, "-C", dir).CombinedOutput()
	st.Elapsed = time.Since(t0).Round(time.Second).String()
	if err != nil {
		st.Error = fmt.Sprintf("extract failed: %v (%s)", err, strings.TrimSpace(string(out)))
		st.Degraded = true
		log.Printf("[webview2] runtime extraction failed: %s — falling back to system Evergreen", st.Error)
		return st
	}
	rtDir := filepath.Join(dir, "webview2-runtime")
	if _, err := os.Stat(filepath.Join(rtDir, "msedgewebview2.exe")); err != nil {
		st.Error = "verification failed: msedgewebview2.exe missing after extract"
		st.Degraded = true
		log.Printf("[webview2] runtime extraction verification failed — falling back to system Evergreen")
		return st
	}
	st.Pinned, st.Dir, st.Source, st.Extracted = true, rtDir, "pinned", true
	st.Version = pinnedRuntimeVersion(rtDir)
	log.Printf("[webview2] pinned runtime extracted in %s", st.Elapsed)
	return st
}

// pinnedRuntimeVersion 从钉版目录的 <version>.manifest 文件名取版本号（取不到返回空）。
//
// ⚠️ v21.198 修正（用户实机实证）：原判定 `strings.Count(n, ".") == 2` **永不匹配**
// 真实文件名 `133.0.3065.92.manifest`——它有 **4 个点**（3 个版本分隔 + 1 个后缀）。
// 于是 runtime-status.json 的 `version` 恒为空、诊断包 summary 的
// `runtime.version=` 也空 ⇒ 排障时看不出钉版内核到底是哪个版本。
// 现按「剥掉 .manifest 后像四段版本号」判定，并保留原判定作兼容兜底。
func pinnedRuntimeVersion(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var fallback string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".manifest") {
			continue
		}
		v := strings.TrimSuffix(n, ".manifest")
		if looksLikeWebView2Version(v) {
			return v
		}
		if fallback == "" && strings.Count(n, ".") == 2 {
			fallback = v // 旧形状兜底（如 `<major>.<minor>.manifest`）
		}
	}
	return fallback
}

// looksLikeWebView2Version 判定 `a.b.c.d`（非空、无前导零段）四段版本号。
func looksLikeWebView2Version(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// fixedWebview2Dir 钉版 runtime 发现：优先 exe 同目录，其次 profileDir（开发态）。
// 目录须直接含 msedgewebview2.exe（Fixed Version 解包形状）。
func fixedWebview2Dir(profileDir string) string {
	candidates := []string{profileDir}
	if exe, err := os.Executable(); err == nil {
		candidates = append([]string{filepath.Dir(exe)}, candidates...)
	}
	for _, base := range candidates {
		dir := filepath.Join(base, "webview2-runtime")
		if st, err := os.Stat(filepath.Join(dir, "msedgewebview2.exe")); err == nil && !st.IsDir() {
			log.Printf("[webview2] using pinned fixed version runtime: %s", dir)
			return dir
		}
	}
	log.Printf("[webview2] no pinned runtime found, falling back to system Evergreen")
	return ""
}
