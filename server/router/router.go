package router

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"stickynote/appbase"
	"stickynote/handler"
	"stickynote/logger"
	"stickynote/middleware"
	"stickynote/static"
	syncsrv "stickynote/sync"
)

// trustedProxies（v18.6）：默认只信本机回环（fnOS 反代/网关与应用同机，
// 直连场景 RemoteAddr 即真实客户端，伪造的 XFF 被直接忽略）；
// TRUSTED_PROXIES 可配自定义代理（逗号分隔 IP/CIDR），
// 置 "none" 表示不信任任何代理（最严，反代后会以网关 IP 作为客户端 IP）。
func trustedProxies() []string {
	v := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if strings.EqualFold(v, "none") {
		return nil
	}
	if v == "" {
		return []string{"127.0.0.1", "::1"}
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func Setup(uploadDir string, webDir string) http.Handler {
	r := gin.New()

	// 可信代理显式收敛（v18.6）：loginGuard 等限流维度全部取 c.ClientIP()，
	// 而 gin 默认信任任意来源的 X-Forwarded-For——攻击者每请求换一个 XFF
	// 即可绕过登录/注册/找回密码的全部限流。
	if err := r.SetTrustedProxies(trustedProxies()); err != nil {
		logger.Error("invalid TRUSTED_PROXIES, fallback to loopback: %v", err)
		_ = r.SetTrustedProxies([]string{"127.0.0.1", "::1"})
	}

	r.Use(gin.Logger())
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())       // P3-15: X-Request-ID 注入（优先复用上游头，否则生成 UUID 短ID）
	r.Use(middleware.RestorationGate()) // v1.0.168 切片 3：在线恢复窗口内 503 短路（仅放行 /health）
	r.Use(middleware.SecureHeaders())
	r.Use(middleware.CORS())
	r.Use(middleware.CacheControl())
	r.Use(middleware.RequestLog()) // v19.3: 结构化请求日志（替代 gin.Logger，不记 body）
	// v20.8 审计优化：错误响应统一回写 X-Request-ID，方便用户反馈时定位日志。
	r.Use(func(c *gin.Context) {
		c.Next()
		if c.Writer.Status() >= 400 {
			if rid := c.GetString("requestID"); rid != "" {
				c.Header("X-Request-ID", rid)
			}
		}
	})
	// gzip：压缩文本类响应（JS 373KB→约 124KB）。图片本身已压缩，
	// 再 gzip 纯浪费 CPU，故按扩展名排除图片类型。
	r.Use(gzip.Gzip(gzip.DefaultCompression,
		gzip.WithExcludedExtensions([]string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".snbak"}),
		gzip.WithExcludedPaths([]string{"/api/backup/download"}), // 归档已加密压缩，二次 gzip 纯耗 CPU
	))

	r.GET("/health", handler.Health)

	// 上传文件受保护访问（v20.7）：替换旧 r.Static 公开路由
	r.GET("/uploads/:name", middleware.RequireAuth(), handler.ServeUpload)

	// B4：全局 API 限流（令牌桶 20/s + burst 40），外网暴露面加固。
	// 正常用户峰值 <5 req/s，安全冗余 4 倍；/health 已豁免（cmd/main healthcheck 依赖）。
	// v21.91（审计 P2-26）：/api/version 移出 public 组 → 需登录（见下方 auth 分组）。
	api := r.Group("/api", middleware.RateLimit(20, 40))
	// v21.52 P3 审计修复：请求体硬上限（MaxBytesReader，超限 413）。
	// v21.53 方案 B 分级：小接口（登录/注册/配置/删除/导出等）16MB；
	// 笔记保存（POST/PUT /notes）与导入（POST /notes/import）放宽到 100MB——
	// 对齐业务硬上限 checkContentLimit(100MB)，修复"业务允许 100MB 但技术层 16MB 误杀"矛盾。
	// （gin 中间件嵌套时 MaxBytesReader 取最外层限制，故大 body 路由必须独立分组）
	{
		// Public（16MB 小接口组）
		apiPublic := api.Group("", middleware.MaxBodySize(16<<20))
		apiPublic.POST("/auth/register", handler.Register)
		apiPublic.POST("/auth/login", handler.Login)
		apiPublic.POST("/auth/refresh", handler.Refresh)
		apiPublic.GET("/auth/setup-status", handler.GetSetupStatus)
		apiPublic.GET("/auth/security-question", handler.GetSecurityQuestion)
		apiPublic.POST("/auth/verify-answer", handler.VerifyAnswer)
		apiPublic.POST("/auth/forgot-password", handler.ForgotPassword)
		apiPublic.GET("/public-config", handler.GetPublicConfig)
		apiPublic.GET("/health", handler.Health)
		apiPublic.POST("/auth/verify-totp", handler.VerifyTOTP) // v20.2 TOTP 登录第二步

		// Authenticated（16MB 小接口组）
		auth := apiPublic.Group("", middleware.RequireAuth())
		// v21.107 P1-13：上传接口独立 body 组——网关限额对齐 UPLOAD_MAX_MB+4MB 编码余量，
		// 修复「业务允许 30MB 但 16~30MB 合法图片在传输层被掐断」的矛盾（审查实测）。
		// 必须挂在 api（不与 16MB 的 apiPublic 嵌套）：gin 嵌套组 MaxBytesReader 取最外层限制。
		// ⚠ 路径保持既有 /api/upload（原 auth 组前缀即为空），仅换组不改 URL——前端 api.post('/upload') 不变。
		uploadGrp := api.Group("", middleware.RequireAuth(), middleware.UploadBodySize())
		uploadGrp.POST("/upload", handler.Upload)
		// v21.91（审计 P2-26）：版本信息含 build_time/git_commit，未认证端点泄露构建指纹，
		// 且前端零调用（仅交付/运维核对版本注入用）→ 收敛到需登录。运维侧用 /api/health。
		auth.GET("/version", handler.GetVersion)
		// v1.0.175 R0/P0-D：同步协议三端点（state 探测 / pull 增量 / push 变更），需登录。
		uploadGrp.POST("/sync/upload", syncsrv.SyncUpload) // P1 批次3：桌面附件推送通道（ADR-003 R2）
		auth.GET("/sync/state", syncsrv.State)
		auth.GET("/sync/pull", syncsrv.Pull)
		auth.POST("/sync/push", syncsrv.Push)
		auth.GET("/sync/payload/:sha256", syncsrv.PayloadBlob) // §34/§35 P1-b：blob 大载荷下载（JWT+引用授权）
		// 批次5 P5：Bootstrap 快照面（本人请求/轮询/下载；admin 可轮询全部）
		auth.POST("/sync/snapshot", syncsrv.SnapshotCreate)
		auth.GET("/sync/snapshot/:id", syncsrv.SnapshotStatus)
		auth.GET("/sync/snapshot/:id/download", syncsrv.SnapshotDownload)
		auth.POST("/auth/logout", handler.Logout)
		auth.POST("/auth/change-password", handler.ChangePassword)
		auth.PUT("/auth/security-question", handler.UpdateSecurityQuestion)
		auth.GET("/notes", handler.ListNotes)
		auth.GET("/notes/tags", handler.GetAllTags)
		auth.GET("/notes/:id", handler.GetNote)
		auth.DELETE("/notes/:id", handler.DeleteNote)
		// v21.86 文件夹：默认"我的笔记"为虚拟项（folder_id IS NULL），自定义文件夹 CRUD
		auth.GET("/folders", handler.ListFolders)
		auth.POST("/folders", handler.CreateFolder)
		auth.PUT("/folders/:id", handler.UpdateFolder)
		auth.DELETE("/folders/:id", handler.DeleteFolder)
		// v2.3.0 文件夹访问锁：加密/解密/本会话解锁
		auth.PUT("/folders/:id/encrypt", handler.EncryptFolder)
		auth.DELETE("/folders/:id/encrypt", handler.DecryptFolder)
		auth.POST("/folders/:id/unlock", handler.UnlockFolder)
		// 回收站（软删除）。列表走 /trash/notes，避免与 /notes/:id 的通配符冲突。
		auth.GET("/trash/notes", handler.ListTrash)
		auth.GET("/trash/stats", handler.TrashStats)    // v18.8 回收站可视化（条数+引用图片占用）
		auth.DELETE("/trash/notes", handler.EmptyTrash) // v21.7 清空回收站
		auth.POST("/notes/:id/restore", handler.RestoreNote)
		auth.DELETE("/notes/:id/permanent", handler.PermanentDeleteNote)
		// Export（v17）：单篇 / 批量 / 全部打包 zip
		// POST 变体（v20.6+）：加密笔记导出密码走 body，避免密码入 URL/日志。
		// 前端加密导出走 POST；GET 保留兼容旧客户端/未加密笔记（window.open）。
		auth.GET("/notes/:id/export", handler.ExportNote)
		auth.POST("/notes/:id/export", handler.ExportNote)
		auth.POST("/notes/batch-export", handler.BatchExportNotes)
		auth.GET("/export/notes", handler.ExportAllNotes)
		// 批量操作（v20.7）
		auth.POST("/notes/batch-delete", handler.BatchDeleteNotes)
		// v21.87 笔记移动/复制到文件夹（body 仅 folder_id，属小接口组）
		auth.PUT("/notes/:id/folder", handler.MoveNoteToFolder)
		auth.POST("/notes/:id/copy", handler.CopyNoteToFolder)
		// 配额（v19.1）：返回当前用户的笔记数/存储占用/上限
		auth.GET("/quota", handler.GetQuota)
		// 笔记加密（v20.0）：加锁/解锁/验证密码
		auth.PUT("/notes/:id/encrypt", handler.EncryptNote)
		auth.DELETE("/notes/:id/encrypt", handler.DecryptNote)
		auth.POST("/notes/:id/verify-encrypt", handler.VerifyEncrypt)

		// v20.2 TOTP 两步验证
		auth.GET("/auth/totp-status", handler.TOTPStatus) // v21.151：个人中心真实启用态
		auth.POST("/auth/enable-totp", handler.EnableTOTP)
		auth.POST("/auth/confirm-totp", handler.ConfirmTOTP)
		auth.POST("/auth/disable-totp", handler.DisableTOTP)

		// Admin
		admin := auth.Group("", middleware.RequireAdmin())
		admin.GET("/users", handler.ListUsers)
		admin.POST("/users", handler.CreateUser)
		admin.PUT("/users/:id", handler.UpdateUser)
		admin.DELETE("/users/:id", handler.DeleteUser)
		admin.POST("/users/:id/disable-totp", handler.AdminDisableTOTP)
		admin.PUT("/users/:id/reset-password", handler.ResetPassword)
		admin.PUT("/users/:id/status", handler.ToggleUserStatus)
		admin.GET("/configs", handler.ListConfigs)
		admin.PUT("/configs/:key", handler.UpdateConfig)
		admin.GET("/logs", handler.GetAdminLogs) // v20.8

		// v1.0.167 系统备份（WebDAV）——路径约定沿用 /api/backup/...（非 /api/admin/...）
		admin.GET("/backup/status", handler.BackupStatus)
		admin.GET("/backup/targets", handler.ListBackupTargets)
		admin.POST("/backup/targets", handler.CreateBackupTarget)
		admin.PUT("/backup/targets/:id", handler.UpdateBackupTarget)
		admin.DELETE("/backup/targets/:id", handler.DeleteBackupTarget)
		admin.POST("/backup/targets/:id/test", handler.TestBackupTarget)
		admin.POST("/backup/targets/:id/run", handler.RunBackupNow)
		admin.GET("/backup/jobs", handler.ListBackupJobs)
		// v2.2.1：执行记录面板【清除记录】（只清终态记录，运行中保留）
		admin.DELETE("/backup/jobs", handler.ClearBackupJobs)
		admin.GET("/backup/jobs/:id", handler.GetBackupJob)
		admin.GET("/backup/snapshots", handler.ListBackupSnapshots)
		// P1 批次3（ADR-003 R3）：附件 sha256 回填进度（数据中心可见）。
		admin.GET("/system/uploads-hash", handler.AdminUploadsHashStatus)
		admin.POST("/backup/discover", handler.DiscoverSnapshots)
		admin.POST("/backup/snapshots/:backup_id/verify", handler.VerifyBackupSnapshot)
		admin.DELETE("/backup/snapshots/:backup_id", handler.DeleteBackupSnapshot)
		admin.GET("/backup/download/:backup_id", handler.DownloadBackup)
		admin.POST("/backup/key/export", handler.KeyExport)
		admin.POST("/backup/key/import", handler.KeyImport)
		admin.POST("/backup/key/regenerate", handler.KeyRegenerate)
		// v1.0.168 切片 3：在线全量恢复（异步 job + 恢复窗口进度）
		admin.POST("/backup/restore", handler.RestoreFromBackup)
		admin.GET("/backup/restore/progress", handler.RestoreProgress)

		// 大 body 组（v21.53 方案 B：笔记保存与导入放宽）。
		// v21.55：上限跟随前台【单篇笔记上限】配置动态计算（DynamicMaxBodySize），
		// 管理后台改 max_note_content_kb 后 ≤5s 生效，无需改代码。
		// 注意：必须独立分组（不与 16MB 组嵌套），否则 MaxBytesReader 取最外层限制。
		authLarge := api.Group("", middleware.RequireAuth(), middleware.DynamicMaxBodySize())
		authLarge.POST("/notes", handler.CreateNote)        // 保存含图/大富文本笔记
		authLarge.PUT("/notes/:id", handler.UpdateNote)     // 同上（加密/非加密共用）
		authLarge.POST("/notes/import", handler.ImportFile) // Markdown/HTML 导入
	}

	// SPA static files - v21.18：
	// - webDir 设置时（开发/CI）：磁盘优先，嵌入兜底
	// - webDir 未设置时（生产单二进制）：嵌入优先
	//
	// v2.1.2（移动端启动卡死根治）：新增**子路径挂载**支持。
	// fnOS Web 桌面用 `type: url` + `port` 直连站点根，手机 App / 远程访问则经网关
	// 以 `/app/<appname>/…` 子路径加载同一个应用。旧实现只认根路径 ⇒ 子路径下
	// 静态资源全落空（/assets/ 打到宿主根，飞牛 nginx 回 404+HTML）⇒ module script
	// 被 MIME 拒绝 ⇒ 永久卡启动占位。此处按「锚点反推前缀 → 还原应用内路径」处理，
	// 同时 SPA 回退时把挂载基址注入 index.html（供相对资源与前端路由使用）。
	distFS, distErr := fs.Sub(static.StaticFS, "dist")
	if distErr != nil {
		distFS = nil
	}

	var fileServer http.Handler
	if webDir != "" {
		if _, err := os.Stat(webDir); err == nil {
			fileServer = http.FileServer(http.Dir(webDir))
		}
	}
	if fileServer == nil {
		if distFS != nil {
			fileServer = http.FileServer(http.FS(distFS))
		} else if _, err := os.Stat("./static/dist"); err == nil {
			fileServer = http.FileServer(http.Dir("./static/dist"))
		}
	}

	if fileServer != nil {
		r.NoRoute(func(c *gin.Context) {
			// v2.1.2：路径已由 appBaseHandler 在**路由匹配前**还原，
			// 这里只取当时算出的前缀（供 SPA 回退注入 base 用）。
			prefix := appbase.FromContext(c.Request.Context())
			path := c.Request.URL.Path

			// /uploads 走到这里说明文件不存在（gin Static 打开失败时会转交 NoRoute）：
			// 缺失的上传文件必须返回 404，绝不能 SPA 回退——否则客户端拿到的是
			// 200 + HTML 首页的"坏图"（v18.5 随删用户清理文件暴露出的隐患）。
			if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/uploads") {
				c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
				return
			}

			// v2.1.3：旧多段管理路由 → 新单段路由。
			//
			// ⚠ 只在**前缀可判定**时才服务端 302（实测踩坑）：
			// 网关剥前缀时应用只看到 /admin/users，服务端算不出挂载点，
			// 此时拼出来的 Location 是根相对路径 /admin-users —— 浏览器会跳去
			// **宿主根**（飞牛主站），比不跳更糟。这种情况交给前端 redirect：
			// index.html 正常返回，内联脚本按地址栏钉好 <base>，Vue Router 再在
			// 挂载点内完成同样的跳转（探针 2 已证资源解析正确）。
			// 前缀可判定（不剥前缀 / 带 X-Forwarded-Prefix）时，服务端跳转不依赖 JS，
			// 对"老书签 + 内联脚本被 CSP 拦掉"这类极端情形更稳。
			if prefix != "" {
				if target, ok := legacyAdminRoute(path); ok {
					loc := prefix + target
					if q := c.Request.URL.RawQuery; q != "" {
						loc += "?" + q
					}
					c.Redirect(http.StatusFound, loc)
					return
				}
			}

			// v21.24：SPA fallback——前端路由（/login、/notes 等）磁盘无对应文件，
			// 直接 serve 会 404 且 body 被 gzip 压缩但缺 Content-Encoding 头 → 浏览器乱码。
			// 文件存在（真实静态资源）才 serve；带扩展名的缺失资源（如坏掉的 JS/CSS 引用）
			// 应返回 404 而非 index.html（否则浏览器把 HTML 当 JS 执行报语法错）；
			// 无扩展名的路径视为前端路由 → 回退 index.html。
			// （v21.18 删 unreachable code 时误删了此 fallback，本次恢复）
			// v21.47：webDir 默认 ./www 但本地/CI 无 www 目录时，磁盘检查失败必须回退 embed，
			// 否则 embed 里的真实静态资源（/assets/*）全部 404（此前仅 webDir=="" 才查 embed）
			if hasAppStaticFile(webDir, distFS, path) {
				// v2.1.2：交给 FileServer 的必须是**剥离前缀后**的路径
				c.Request.URL.Path = path
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
			if strings.Contains(path, ".") {
				c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
				return
			}
			// SPA fallback：返回注入了挂载基址的 index.html（v2.1.2）
			serveAppIndex(c, webDir, distFS, prefix)
		})
	}

	return &appBaseHandler{engine: r}
}

// appBaseHandler 在 gin 路由匹配**之前**把子路径还原成应用内路径。
//
// v2.1.2：为什么必须在这一层——
// gin 的 `r.Use()` 中间件运行在**路由匹配之后**（engine.handleHTTPRequest 先
// tree.getValue 定好 handlers 再执行链），所以在中间件里改 c.Request.URL.Path
// 对匹配结果毫无影响：`/app/stickynote/api/login` 早已被判成 NoRoute。
// 唯一干净的位置是包在 Engine 外层的 http.Handler。
//
// 还原后：API / uploads / 静态资源 / SPA 路由全部按站点根的正常语义匹配，
// 无需为子路径重复注册任何路由，也不会重复执行中间件（Logger/限流各只跑一次）。
type appBaseHandler struct {
	engine *gin.Engine
}

func (h *appBaseHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if prefix := appBasePrefix(req.URL.Path, req.Header); prefix != "" {
		req.URL.Path = stripAppBase(req.URL.Path, prefix)
		req.URL.RawPath = "" // 清空 RawPath，避免 gin 用原始转义路径匹配
		req = req.WithContext(appbase.WithPrefix(req.Context(), prefix))
	}
	h.engine.ServeHTTP(w, req)
}

// ---------------------------------------------------------------------------
// v2.1.2 子路径挂载支持
//
// 背景：应用有两条加载链路——fnOS Web 桌面直连站点根（type:url + port），
// 手机 App / 远程访问经网关以 /app/<appname>/ 子路径加载。后者在 2.1.1 之前完全
// 没被支持：HTML 里 /assets/… 的根绝对路径会被浏览器解析到宿主根，落到飞牛主站。
//
// 设计取舍：
//   - **不猜任意前缀**：只接受 X-Forwarded-Prefix 显式声明，或从固定资源锚点
//     （/assets/、/manifest.webmanifest 等）反推，或 fnOS 网关固定形态 /app/<name>。
//     前缀每段限 [A-Za-z0-9._-]、最多 3 段、拒绝 . 与 ..，避免被当成开放代理或目录穿越。
//   - **剥前缀后走原有安全语义**：/api 与 /uploads 缺失仍 404，带扩展名缺失仍 404，
//     绝不 SPA 回退成 HTML（否则浏览器把 HTML 当 JS 执行）。
//   - **只在 SPA 回退时改写 HTML**：静态资源仍由 http.FileServer 直出，零额外开销。
// ---------------------------------------------------------------------------

// appBasePrefix / normalizeAppPrefix / stripAppBase 三个薄包装保留在 router 包内，
// 便于本包测试直接断言；实现已下沉到 appbase 包（handler 侧设置 cookie Path 也要用，
// 放这里会形成 handler -> router 的反向依赖）。
func appBasePrefix(p string, h http.Header) string { return appbase.Prefix(p, h) }

func normalizeAppPrefix(v string) string { return appbase.Normalize(v) }

func stripAppBase(p, prefix string) string { return appbase.Strip(p, prefix) }

// hasAppStaticFile 判断应用内该路径是否存在**真实文件**（目录不算）。
//
// v2.1.2 踩坑：早先版本用 distFS.Open 判断，而 Open("/") 会成功返回根目录
// ⇒ 首页被误判成"静态文件已存在"⇒ 绕过 SPA 回退 ⇒ 注入不了挂载基址
// （实测 16.7b.2/16.7b.3 冒烟直接红）。必须 Stat 且排除目录。
// legacyAdminRoute 把 2.1.2 及更早版本的 /admin/<page> 深链接映射到 v2.1.3 单段路由。
//
// 为什么必须压成单段：子路径网关剥前缀时，浏览器按「地址栏去掉最后一段」解析相对引用，
// /app/x/admin/users 会把基址算成 /app/x/admin/ ⇒ ./assets/… 全部 404。
//
// 只做**精确匹配**，不做 /admin/* 通配——避免把将来可能存在的其他 /admin 路径一并吞掉。
func legacyAdminRoute(p string) (string, bool) {
	p = strings.TrimRight(p, "/")
	switch p {
	case "/admin/users":
		return "/admin-users", true
	case "/admin/logs":
		return "/admin-logs", true
	case "/admin/configs":
		return "/admin-configs", true
	case "/admin/limits":
		return "/admin-limits", true
	case "/admin/backup":
		return "/admin-backup", true
	}
	return "", false
}

func hasAppStaticFile(webDir string, distFS fs.FS, p string) bool {
	if webDir != "" {
		// v21.90 P2（审计低危项）：改用 filepath.Join 消除双斜杠拼接；
		// http.FileServer 本身拒绝 ..，此处为工程规范收敛。
		if info, err := os.Stat(filepath.Join(webDir, p)); err == nil && !info.IsDir() {
			return true
		}
	}
	if distFS != nil {
		name := strings.TrimPrefix(p, "/")
		if name == "" {
			return false
		}
		if info, err := fs.Stat(distFS, name); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// readIndexHTML 读取 index.html（webDir 优先，embed 兜底）。
func readIndexHTML(webDir string, distFS fs.FS) ([]byte, error) {
	if webDir != "" {
		if b, err := os.ReadFile(filepath.Join(webDir, "index.html")); err == nil {
			return b, nil
		}
	}
	if distFS != nil {
		return fs.ReadFile(distFS, "index.html")
	}
	return nil, os.ErrNotExist
}

// rewriteAppAssetPaths 在前缀**可判定**时，把产物里的 `./` 相对引用重写成
// 「带挂载前缀的绝对路径」，并注入 sn-app-base meta。
//
// 为什么不注入 `<base href>`（v2.1.2 实测踩坑）：
//   - 网关可能「剥前缀」转发，应用只看到 `/`，服务端反推不出前缀；
//   - 此时若注入 `<base href="/">`，会把 `./assets/…` 钉死到宿主根 ⇒ 全 404；
//   - 而 Vite 会把入口 `<script type="module">` 注入到 `<head>` 最前，
//     内联脚本即便写 `<base>` 也来不及影响它的 URL 解析。
//
// 所以采用「能算就重写成绝对路径 / 算不出就保持相对、由浏览器按地址栏解析」：
//   - 不剥前缀的网关（/app/x/…）：重写 → /app/x/assets/… ✅（深路由也正确）
//   - 剥前缀的网关（只看到 /）：不重写 → ./assets/… 按地址栏 → /app/x/assets/… ✅
//   - 站点根直连：不重写 → ./assets/… → /assets/… ✅（与历史行为等价）
//
// 代价（已登记为已知限制）：**剥前缀 + 多级深路由 + 整页刷新**三个条件同时成立时，
// 相对引用会多套一层路由段。单级路由与 SPA 内导航不受影响，且不剥前缀时完全正确。
//
// prefix 已由 normalizeAppPrefix 白名单校验（仅 [A-Za-z0-9._-] 与 /），无注入面。
func rewriteAppAssetPaths(html []byte, prefix string) []byte {
	if prefix == "" {
		return html
	}
	repl := prefix + "/"
	html = bytes.ReplaceAll(html, []byte(`src="./`), []byte(`src="`+repl))
	html = bytes.ReplaceAll(html, []byte(`href="./`), []byte(`href="`+repl))

	// 顺带提供 meta，供前端 detectBase() 首选（与前端 location.pathname 推断互为校验）
	tag := `<meta name="sn-app-base" content="` + prefix + `">`
	if idx := bytes.Index(bytes.ToLower(html), []byte("<head>")); idx >= 0 {
		end := idx + len("<head>")
		out := make([]byte, 0, len(html)+len(tag))
		out = append(out, html[:end]...)
		out = append(out, tag...)
		out = append(out, html[end:]...)
		return out
	}
	return html
}

// serveAppIndex 返回注入挂载基址的 index.html。
// 显式 no-cache：升级后必须立刻拿到新的资源引用，避免旧缓存引用已删 hash 资源导致白屏。
func serveAppIndex(c *gin.Context, webDir string, distFS fs.FS, prefix string) {
	html, err := readIndexHTML(webDir, distFS)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(rewriteAppAssetPaths(html, prefix))
}
