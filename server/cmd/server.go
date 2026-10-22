package cmd

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"stickynote/auth"
	"stickynote/backup"
	"stickynote/changelog"
	"stickynote/config"
	"stickynote/database"
	"stickynote/handler"
	"stickynote/logger"
	"stickynote/model"
	"stickynote/restore"
	"stickynote/router"
	syncsrv "stickynote/sync"
)

var appVersion = "dev"

func StartServer(port int, dataDir, webDir, uploadDir, shareDirs, socketPath string) {
	// 生产模式（v18.6）：GIN_MODE 未显式设置时默认 release，
	// 避免 debug 模式的冗余控制台输出与调试警告页。
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	cfg := config.Load(port, dataDir, webDir, uploadDir, shareDirs)

	if err := logger.Init(cfg.LogDir()); err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		return
	}
	defer logger.Close()

	logger.Info("Starting Notepad server on port %d", cfg.Port)
	if socketPath != "" {
		logger.Info("Gateway mode (v2.2.0): unix socket %s (TCP dual-track retained)", socketPath)
	} else {
		logger.Info("Gateway mode: unix socket disabled (TCP only)")
	}
	logger.Info("JWT_SECRET source: %s (env=env var / file=.jwt_secret / generated=new random key)", cfg.JWTSecretSource)

	uploadPath := cfg.UploadDir
	if uploadPath == "" {
		uploadPath = filepath.Join(cfg.DataDir, "upload")
	}

	// v1.0.168 切片 3：在线全量恢复器。崩溃恢复必须是**打开任何 DB 之前**的纯文件级动作，
	// 否则会把半恢复态的库当正常库迁移/服务。JWT=env 的部署据 Options 拒绝在线整库恢复。
	restoreCtl := restore.New(restore.Options{
		DataDir: cfg.DataDir, UploadDir: uploadPath, DBPath: cfg.DBPath(),
		AppVersion:     strings.TrimPrefix(appVersion, "v"),
		JWTSecretIsEnv: cfg.JWTSecretSource == "env",
	})
	if err := restoreCtl.StartupRecover(); err != nil {
		// fail-closed（审计 GPT-04）：崩溃恢复/回滚失败或日志损坏时，DB 可能处于半恢复态，
		// 绝不能继续开库对外服务（否则会把可自动修复的问题演变成用户继续写入后的数据分叉）。
		// 停止启动，等人工介入（CLI restore-backup 或清理 journal/.pre-restore）。
		logger.Error("Restore startup recovery failed, refusing to start (manual intervention required): %v", err)
		return
	}

	if err := database.Init(cfg.DBPath(), appVersion); err != nil {
		logger.Error("Failed to initialize database: %v", err)
		return
	}
	// v1.0.175 P0-D：同步端点的实例身份来源（.instance_id 所在 DataDir，§22 单一权威）。
	syncsrv.Configure(cfg.DataDir)
	syncsrv.SetAttachmentsDir(uploadPath)                                    // P1 批次3：/api/sync/upload 落盘目录
	syncsrv.ConfigureSnapshot(cfg.DataDir, uploadPath)                       // 批次5 P5：Bootstrap 快照目录（DATA_DIR/sync-snapshots）
	changelog.ConfigurePayloads(filepath.Join(cfg.DataDir, "sync-payloads")) // §34 P1-b：blob 大载荷目录
	// S-1 加固（2026-10-08）：启动期清扫超龄在途快照（上次进程卡死/崩溃遗留的
	// PENDING/BUILDING 行会永久占住每用户生成帽——真机实证 fnOS 卷上发生）。
	if n := syncsrv.ReclaimStaleSnapshots(); n > 0 {
		log.Printf("[snapshot] startup: reclaimed %d stale in-flight build(s)", n)
	}
	restore.OnEpochCommitted = syncsrv.InvalidateBootstrapSnapshots // 批次5：恢复换代 → 在途/已就绪快照作废

	// v21.93 P1：DB 就绪后再初始化登录防爆破——此前该动作挂在 handler 包级 init()
	// 中，执行早于 database.Init，导致「从 DB 恢复未过期失败记录」从未真正执行。
	handler.InitLoginGuard()

	// v21.101：启动期一次性排序自愈——历史「移动至顶部」缺陷可能留下重复 sort_order，
	// 在此按 (sort_order, id) 规范化为 1..n。幂等，无修正时零写入。
	if fixed, err := model.NormalizeAllFolderSortOrders(); err != nil {
		logger.Error("Failed to normalize folder sort_order: %v", err)
	} else if fixed > 0 {
		logger.Info("Normalized %d folder sort_order rows (self-heal)", fixed)
	}

	auth.Init(cfg.JWTSecret)

	// v1.0.168 切片 3：崩溃于"已换库未收尾"（DONE/FINALIZING）的恢复，在库已打开后补做收尾
	// （会话吊销，幂等）。无待收尾时 NeedsStartupFinalize 返回空串。
	if rid := restoreCtl.NeedsStartupFinalize(); rid != "" {
		if err := restoreCtl.Finalize(rid); err != nil {
			logger.Error("Restore startup finalize failed id=%s: %v", rid, err)
		} else {
			logger.Info("Completed finalize for interrupted restore id=%s", rid)
		}
	}

	handler.SetUploadDir(uploadPath)

	// P1 批次3（ADR-003 R2/R4）：事件载荷的附件哈希保证位——BuildNotePayload 遇 '' 行
	// 经此钩子即时补算落库（协议保证桌面收到的任何 attachment_ref sha256 非空）。
	changelog.HashProvider = func(filename string) (string, error) {
		// 只读计算（事务安全）；'' 行的权威落库由下方回填任务收敛。
		return model.FileSHA256(filepath.Join(uploadPath, filepath.Base(filename)))
	}
	// P1 批次3（ADR-003 R3）：低优先级历史回填——每批 50、批间 sleep 2s 让出写锁；
	// 天然断点续传（'' 即进度标记），收敛后任务自然退出；重启幂等重跑。
	go func() {
		for {
			done, remaining, err := model.BackfillUploadHashesOnce(uploadPath, 50)
			if err != nil {
				logger.Error("uploads sha256 backfill round failed: %v", err)
			}
			if remaining <= 0 {
				if done > 0 {
					logger.Info("uploads sha256 backfill complete (%d rows this run)", done)
				}
				return
			}
			time.Sleep(2 * time.Second)
		}
	}()

	// v1.0.167 系统备份：进程级 Manager + 调度器（HTTP 常驻不拆，见 v4.3-final 恢复设计）。
	// 上传附件真实目录用 uploadPath（可能与 cfg.UploadDir 同），DataDir 存主密钥/instance/快照。
	backupMgr := backup.NewManager(cfg.DataDir, uploadPath, strings.TrimPrefix(appVersion, "v"), backupSchemaVersion)
	handler.SetBackupManager(backupMgr)
	handler.SetRestoreController(restoreCtl) // v1.0.168：在线恢复复用同一运行互斥锁
	// v1.0.170：一次性清理历史 prune 遗留的孤儿快照行（有记录无远端副本 → 下载必失败）。
	// 放在调度器启动前，确保无并发在途备份被误删；幂等（configs 旗标控制，仅首次生效）。
	if purged, perr := model.PurgeOrphanBackupSnapshotsOnce(); perr != nil {
		logger.Error("PurgeOrphanBackupSnapshots failed (will retry next start): %v", perr)
	} else if purged > 0 {
		logger.Info("Purged %d orphan backup snapshot rows (legacy prune leftovers)", purged)
	}
	backupStop := make(chan struct{})
	backupMgr.StartScheduler(backupStop)

	// uploads 存量回填与无归属旧文件清理（v18.5，一次性，旗标控制）：
	// 升级后首次启动把存量笔记引用的文件回填归属，并清掉无归属超期旧文件。
	if err := database.BackfillUploads(uploadPath); err != nil {
		logger.Error("BackfillUploads failed: %v", err)
	}

	// 回收站过期自动清理（P2-6）：根据 trash_retention_days 配置清理超期笔记。
	database.CleanExpiredTrash(uploadPath)

	// v21.66 P1-3：孤儿附件 GC——清理超过 24h 宽限期的 orphan 状态上传文件。
	database.CleanOrphanUploads(uploadPath)
	// v21.67 P1-02：staged 附件 GC——清理超过 24h 未绑定笔记的上传文件。
	database.CleanStagedUploads(uploadPath)
	// v21.67 P1-03：删除用户残留文件兜底重试。
	database.CleanPendingUploads(uploadPath)

	// P2-4 定时扫描：每 6 小时重跑一次清理，不再只依赖启动时一次性执行。
	// trash_retention_days=0（默认）时 CleanExpiredTrash 内部直接返回，零开销。
	// v21.97（审计 P-03 整改）：启动时先执行一次清理，避免「首次 6 小时」窗口内
	// 过期会话堆积（此前仅等定时器首次 tick）。
	go func() {
		database.CleanExpiredTrash(uploadPath)
		database.CleanOrphanUploads(uploadPath)
		database.CleanStagedUploads(uploadPath)
		database.CleanPendingUploads(uploadPath)
		syncsrv.ScanSnapshots() // 批次5：快照 TTL/孤儿文件扫描（§46/§48）
		// N-04'（v1.0.134 审计整改）：清理过期 refresh 会话，
		// 防止 refresh_sessions 表随轮转无限增长（此前该函数全仓无调用点）。
		if n, err := model.CleanupExpiredRefreshSessions(); err != nil {
			logger.Error("CleanupExpiredRefreshSessions: %v", err)
		} else if n > 0 {
			logger.Info("CleanupExpiredRefreshSessions: removed %d expired sessions (startup)", n)
		}
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			database.CleanExpiredTrash(uploadPath)
			database.CleanOrphanUploads(uploadPath)
			database.CleanStagedUploads(uploadPath)
			database.CleanPendingUploads(uploadPath)
			if ne, nf := syncsrv.ScanSnapshots(); ne > 0 || nf > 0 {
				logger.Info("ScanSnapshots: expired=%d orphan-files=%d", ne, nf)
			}
			if n, err := model.CleanupExpiredRefreshSessions(); err != nil {
				logger.Error("CleanupExpiredRefreshSessions: %v", err)
			} else if n > 0 {
				logger.Info("CleanupExpiredRefreshSessions: removed %d expired sessions", n)
			}
		}
	}()

	r := router.Setup(uploadPath, cfg.WebDir)

	// 优雅关闭（v20.8 审计优化）：监听 SIGTERM/SIGINT，
	// 收到信号后关闭 HTTP Server 与数据库连接，避免 WAL/SHM 脏文件。
	// v21.90 P0（N-03/M-01 审计核对）：补齐 HTTP Server 超时——原实现仅 Addr/Handler，
	// 慢连接（Slowloris）可长期占用 worker；SQLite MaxOpenConns=2 更易被打满。
	// v2.2.0 统一网关模式：双监听并存（硬性兼容约束，方案 §2 #1）——
	// TCP（WebDAV 第三方客户端 / 局域网直连 / 桌面端）+ unix socket（飞牛网关转发），
	// 两个 listener 共享同一 router（appBaseHandler 已按请求前缀自适应，无需区分来源）。
	// v21.90 P0（N-03/M-01 审计核对）：HTTP Server 超时对两条监听同时生效。
	srv := &http.Server{
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,  // 头部读取超时（Slowloris 主防线）
		ReadTimeout:       60 * time.Second,  // 请求体读取（v21.93 注释同步：大导入上限已由 256MB 收敛为 128MB，60s 足够）
		WriteTimeout:      120 * time.Second, // 响应写出（导出 ZIP/大笔记放宽）
		IdleTimeout:       120 * time.Second, // 空闲连接回收
		MaxHeaderBytes:    1 << 20,           // 1MB 头上限
	}

	tcpLn, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		logger.Error("Failed to listen TCP :%d: %v", cfg.Port, err)
		return
	}

	var unixLn net.Listener
	if socketPath != "" {
		unixLn, err = listenUnixSocket(socketPath)
		if err != nil {
			// fail-closed：网关模式是 fnOS 官方唯一入口，静默降级（仅 TCP）=
			// 「进程活着但官方入口打不开」的静默瘫痪形态，宁可启动失败让 supervisor 重试。
			logger.Error("Failed to listen unix socket %s, refusing to start: %v", socketPath, err)
			_ = tcpLn.Close()
			return
		}
	}

	serveErr := make(chan error, 2)
	go func() { serveErr <- srv.Serve(tcpLn) }()
	if unixLn != nil {
		go func() { serveErr <- srv.Serve(unixLn) }()
	}

	// 优雅关闭（v20.8 审计优化）：监听 SIGTERM/SIGINT，
	// 收到信号后关闭 HTTP Server 与数据库连接，避免 WAL/SHM 脏文件。
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
		sig := <-quit
		logger.Info("Received signal %v, shutting down gracefully...", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("Server forced to shutdown: %v", err)
		}
		cleanupUnixSocket(socketPath) // v2.2.0：优雅退出摘除 socket 文件（幂等，方案 §1.1 #4）
		close(backupStop)             // 停调度循环（在跑的备份 job 靠 .backup.lock/幂等在下次启动收敛）
		database.Close()
		logger.Close()
		logger.Info("Server exited")
	}()

	// 主 goroutine 等 TCP serve 结束：Shutdown 关闭全部 listener 后返回 ErrServerClosed；
	// TCP 端口被占等启动期失败也从此通道冒出 → 整进程退出，supervisor 3s 重试。
	if err := <-serveErr; err != nil && err != http.ErrServerClosed {
		logger.Error("Server failed: %v", err)
	}
}

func SetVersionInfo(version, buildTime, gitCommit string) {
	appVersion = version
	handler.Version = version
	handler.BuildTime = buildTime
	handler.GitCommit = gitCommit
}
