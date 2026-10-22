package main

// portable_bindings.go —— P6 便携备份 .snbak（v20 §106~§109/§148；批次 6 刀 3）。
// 安全姿态：备份口令由 UI 先行收集（内存过路、不落日志）；导出经原生保存对话框、
// 导入经原生打开对话框，Go 侧不接收任意路径写口；红线 29 零监听。
// 导入成功 = 双代激活 + 换库接管全局句柄 + worker 随迁（复刻 BootstrapNow 收尾链路；
// 离线导入 = 档案可开则空票占位工保记账，不可开则引导重登）。

import (
	"errors"
	"path/filepath"
	"time"

	"stickynote-desktop/localauth"
	"stickynote-desktop/localstore"
	"stickynote-desktop/portable"
	"stickynote/database"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// DesktopAppVersion 便携包 manifest 的产包版本标注：批次 7 版本注入链收口——
// 直读 main.Version（构建脚本 -ldflags 注入），未注入的临时构建显式标 dev。
func DesktopAppVersion() string { return Version }

// PortableExportSummary 便携导出（§148 A 侧）：active generation 快照 → 脱敏 → 附件收集
// （引用缺失 fail-closed）→ SNBK 同信封封装（口令派生 master）→ .snbak 落用户所选路径。
// 返回 cancelled=true 表示用户取消对话框（非错误）。
func (l *LocalService) PortableExport(password string) (map[string]any, error) {
	if l.ctx == nil {
		return nil, errors.New("DESKTOP_RUNTIME_NOT_READY")
	}
	savePath, err := wailsRuntime.SaveFileDialog(l.ctx, wailsRuntime.SaveDialogOptions{
		Title:           "导出便携备份（.snbak）",
		DefaultFilename: "stickynote-portable-" + time.Now().Format("20060102-1504") + ".snbak",
		Filters:         []wailsRuntime.FileFilter{{DisplayName: "Sticky note 便携备份 (*.snbak)", Pattern: "*.snbak"}},
	})
	if err != nil {
		return map[string]any{"error": err.Error()}, err
	}
	if savePath == "" {
		return map[string]any{"cancelled": true}, nil
	}
	res, err := portable.Export(portable.ExportOptions{
		ProfileDir: filepath.Dir(l.uploadDir), UploadDir: l.uploadDir,
		Password: password, OutPath: savePath, AppVersion: DesktopAppVersion(),
	})
	if err != nil {
		return map[string]any{"error": err.Error()}, err
	}
	return map[string]any{
		"path": savePath, "backup_id": res.BackupID, "backup_set_id": res.BackupSetID,
		"file_bytes": res.FileBytes, "out_bytes": res.OutBytes,
		"attachments": res.Attachments, "note_count": res.NoteCount,
		"outbox_pending": res.OutboxPending, "conflict_count": res.ConflictCount,
		"cursor": res.Cursor, "server_epoch": res.Epoch, "instance_id": res.InstanceID,
		"username": res.Username,
	}, nil
}

// PortableImport 便携导入（§148 B 侧；口令错/篡改/缺附件/非 PORTABLE 包均在激活前拒绝、
// 本机零破坏）。成功后换库接管 + worker 随迁；rebind_required=true 表示包来源与本机
// 绑定的服务器不同（§108 V1 拒绝自动进入同步态，须显式重登/重建）。
func (l *LocalService) PortableImport(password string) (map[string]any, error) {
	if l.ctx == nil {
		return nil, errors.New("DESKTOP_RUNTIME_NOT_READY")
	}
	inPath, err := wailsRuntime.OpenFileDialog(l.ctx, wailsRuntime.OpenDialogOptions{
		Title:   "选择便携备份（.snbak）",
		Filters: []wailsRuntime.FileFilter{{DisplayName: "Sticky note 便携备份 (*.snbak)", Pattern: "*.snbak"}},
	})
	if err != nil {
		return map[string]any{"error": err.Error()}, err
	}
	if inPath == "" {
		return map[string]any{"cancelled": true}, nil
	}
	profDir := filepath.Dir(l.uploadDir)
	// v21.203 换代互斥：Import 与 BootstrapNow 都会写**同一个** activation journal、
	// 同一份 active 指针与同一个代库。原先两者只各自 pause 调度器，彼此不互斥 ——
	// 用户「导入便携包」的同时自动重建到期（或反之），会产出交错写入的 journal
	// 与「A 以为 B 的残缺代是自己的」的指针终态（审计定罪：PortableImport 未走
	// bootstrapMu）。这里用 TryLock 与 BootstrapNow 同一把锁，锁被占即明确拒绝。
	if !bootstrapMu.TryLock() {
		return map[string]any{"error": "重建同步或导入正在进行中，请稍候", "bootstrap_busy": true},
			errors.New("重建同步或导入正在进行中，请稍候")
	}
	defer bootstrapMu.Unlock()
	pauseScheduler() // Import = 脱敏/激活/重植/并入的多步换库，中途不可有并发同步轮
	defer resumeScheduler()
	res, err := portable.Import(portable.ImportOptions{ // 旧代档案/Outbox 由 Import 内部按指针自取
		ProfileDir: profDir, UploadDir: l.uploadDir, InPath: inPath, Password: password,
	})
	if err != nil {
		return map[string]any{"error": err.Error()}, err
	}
	newDB, oerr := localstore.OpenGenDB(filepath.Join(profDir, "generations", res.Generation, "notes.db"))
	if oerr != nil {
		return map[string]any{"error": oerr.Error(), "generation": res.Generation}, oerr
	}
	if old := database.SetDB(newDB); old != nil && old != newDB {
		_ = old.Close()
	}
	// worker 随迁三态：持票 = 换绑库句柄票/URL/设备全留；档案可开 = 空票占位工
	// （批次 4 语义：同进程离线编辑不漏账）；皆不可 = 等重登（UI 据 relogin_required 引导）。
	remounted := "none"
	sess, _, serr := localauth.OpenSession(newDB) // 导入侧重植的本机档案（DPAPI 本机可解）
	switch {
	case workerHasToken(workerPtr()):
		workerPtr().RebindDB(newDB)
		remounted = "token"
		if serr == nil {
			l.deviceID = sess.DeviceID
		}
	case serr == nil:
		mountWorker(sess, "", l.uploadDir)
		remounted = "placeholder"
		l.deviceID = sess.DeviceID
	default:
		setWorker(nil)
	}
	return map[string]any{
		"generation": res.Generation, "backup_id": res.BackupID,
		"attachments_merged": res.AttachmentsMerged, "note_count": res.NoteCount,
		"outbox_pending": res.OutboxPending, "carried": res.Carried,
		"carry_conflicts": res.CarryConflict, "cursor": res.Cursor,
		"server_epoch": res.Epoch, "instance_id": res.InstanceID,
		"rebind_required":  res.HadProfile && !res.InstanceMatch, // §108 跨服务器
		"relogin_required": remounted == "none",
		"worker_remounted": remounted,
	}, nil
}
