package cmd

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"

	"stickynote/config"
	"stickynote/database"
	"stickynote/model"
)

func ExecuteCLI(args []string) {
	if len(args) == 0 {
		printUsage()
		return
	}

	// v21.95 H-03' 修复：backup-db 必须在 database.Init **之前**处理——
	// Init 内部的 runUpgrades 会迁移 schema，而 backup-db 的语义是
	// "升级前备份未迁移的旧库"（先迁移再备份就失去升级前快照意义）。
	// 此前 backup-db 分支在 switch 内、Init 之后执行，导致备份实际发生在迁移后
	// （钩子验收真机复现：upgrade_init 调 backup-db 时日志出现 "Upgrading"）。
	if args[0] == "backup-db" {
		// v21.112 修复（P1，2026-09-07 深度测试真机 192.0.2.10 双向复证）：
		// 不得调用 config.Load——其副作用 os.MkdirAll(cfg.DataDir, 0700)（默认 ./data，
		// 相对 cwd，见 config.go）在 cwd 不可写时（fnOS upgrade_init 钩子的 cwd 可能为 /
		// 等只读目录）直接 [FATAL] 退出，导致无 sqlite3 宿主机上升级前备份必败、
		// 升级被 v21.90 fail-closed 门禁误阻断（备份失败 exit 1）。
		// backup-db 语义 = 只读源库 + VACUUM INTO，无需创建任何目录；
		// 默认源路径保持 ./data/notepad.db（与旧 DBPath 语义一致），
		// upgrade_init 恒显式传第二参数（v21.95 H-03'），不受此默认值影响。
		backupDB("./data/notepad.db", args[1:])
		return
	}

	// v1.0.167 系统备份：backup / verify-backup / restore-backup 均在 database.Init 之前处理
	// （与 backup-db 同理——恢复要打开"待恢复的旧库"，先 Init 会先把线上库迁移，语义错乱）。
	// 三者只做只读校验/解包，不自动覆盖线上数据（换库=停服后由运维或切片 3 Web 恢复执行）。
	switch args[0] {
	case "backup", "verify-backup", "restore-backup":
		os.Exit(runBackupCLI(args))
	}

	cfg := config.Load(0, "", "", "", "")
	if err := database.Init(cfg.DBPath(), appVersion); err != nil {
		fmt.Printf("Failed to initialize database: %v\n", err)
		return
	}

	switch args[0] {
	case "recover-admin":
		recoverAdmin(args[1:])
	case "find-admin":
		findAdmin()
	case "list-users":
		listUsers()
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("Notepad CLI Tool")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  sticky-note                  Start the web server")
	fmt.Println("  sticky-note recover-admin [-yes]   Reset admin password (asks for username confirmation)")
	fmt.Println("  sticky-note find-admin       Show admin username")
	fmt.Println("  sticky-note list-users       List all users")
	fmt.Println("  sticky-note backup-db <target.db>   Create a consistent single-file DB snapshot (VACUUM INTO)")
	fmt.Println("  sticky-note backup <out.snbak> [source.db]   Create an encrypted backup archive (WebDAV-independent)")
	fmt.Println("  sticky-note verify-backup <in.snbak>         Verify an encrypted archive (GCM+hash+integrity, read-only)")
	fmt.Println("  sticky-note restore-backup <in.snbak> <dir>  Verify+extract archive to <dir> (does NOT overwrite live data)")
}

// backupDB 生成 SQLite 一致性单文件快照（v21.95 H-03'，审计 P2）。
// 原理：VACUUM INTO（SQLite 3.27+，modernc 支持）由 SQLite 自身事务语义拷贝，
// 产出包含已提交但未 checkpoint 的 WAL 内容的单文件一致性快照，等价 sqlite3 .backup，
// 且无需停止运行中的应用（与 WAL 兼容、不阻塞其他连接）。
// 注意：**不走 database.Init**——backup-db 的语义是"升级前备份未迁移的旧库"，
// Init 内部的 runUpgrades 会迁移 schema，先迁移再备份就失去了升级前快照的意义。
func backupDB(dbPath string, args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: sticky-note backup-db <target.db> [source.db]")
		return
	}
	target := args[0]
	if target == "" {
		fmt.Println("Error: target path is empty")
		return
	}
	// v21.95 H-03' 修正：允许显式传源库路径（第二参数）。
	// 升级/卸载备份场景（fnpack/cmd/upgrade_init）库位于 $TRIM_PKGVAR/data/，
	// 而 config.Load 默认 DBPath 是相对 cwd 的 ./data/notepad.db——若不传源路径
	// 会备份成空库（钩子验收真机复现：Backup created 但恢复无数据）。
	if len(args) > 1 && args[1] != "" {
		dbPath = args[1]
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Printf("Error: open database: %v\n", err)
		return
	}
	defer db.Close()
	// VACUUM INTO 要求目标文件不存在
	if _, err := os.Stat(target); err == nil {
		if err := os.Remove(target); err != nil {
			fmt.Printf("Error: cannot remove existing target %s: %v\n", target, err)
			return
		}
	} else if !os.IsNotExist(err) {
		fmt.Printf("Error: stat target %s: %v\n", target, err)
		return
	}
	// 目标路径进入 SQL 字面量：单引号转义（防路径注入到 SQL 串）
	escaped := strings.ReplaceAll(target, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
		fmt.Printf("Error: backup failed: %v\n", err)
		return
	}
	fmt.Printf("Backup created: %s\n", target)
}

// recoverAdmin 重置管理员密码。v18.8（P1-8）起强制二次确认：
// 必须输入目标 admin 用户名一致才继续（防误触/防执行到错误的数据目录）；
// 脚本化场景可用 -yes/--yes 跳过确认。
func recoverAdmin(flags []string) {
	skipConfirm := false
	for _, f := range flags {
		if f == "-yes" || f == "--yes" {
			skipConfirm = true
		}
	}

	admin, err := model.GetAdminUser()
	if err != nil {
		fmt.Println("Error: No admin user found")
		return
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("Admin username: %s\n", admin.Username)
	if !skipConfirm && !confirmAdminIdentity(reader, admin.Username, os.Stdout) {
		return
	}

	fmt.Print("Enter new password: ")
	password, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("Error reading password: %v\n", err)
		return
	}
	password = strings.TrimSpace(password)

	// v21.58 P5 审计修复：CLI 密码最低长度统一为 8（与 Web/UI min=8 一致），
	// 消除本地管理入口绕过统一密码基线的问题（此前 CLI 仅要求 6 位）。
	if len(password) < 8 {
		fmt.Println("Error: Password must be at least 8 characters")
		return
	}

	if err := model.UpdatePassword(admin.ID, password); err != nil {
		fmt.Printf("Error resetting password: %v\n", err)
		return
	}

	fmt.Println("Admin password reset successfully!")
}

// confirmAdminIdentity recover-admin 二次确认（v18.8，P1-8）：从 reader 读一行，
// 必须与目标 admin 用户名完全一致才放行。抽出为独立函数以便单测覆盖。
func confirmAdminIdentity(reader *bufio.Reader, username string, out io.Writer) bool {
	fmt.Fprintf(out, "This will RESET the admin password. Type the admin username '%s' to confirm: ", username)
	confirm, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(out, "Error reading confirmation: %v\n", err)
		return false
	}
	if strings.TrimSpace(confirm) != username {
		fmt.Fprintln(out, "Confirmation mismatch, aborted.")
		return false
	}
	return true
}

func findAdmin() {
	// v21.58 P4 审计修复：不再输出管理员用户名（防本地账户枚举）——
	// 本机有低权限 shell 访问者时，find-admin 不应直接暴露管理账号名称。
	// 需要交互式确认用户名时使用 recover-admin。
	_, err := model.GetAdminUser()
	if err != nil {
		fmt.Println("Error: No admin user found")
		return
	}
	fmt.Println("Admin account exists.")
}

func listUsers() {
	users, err := model.ListUsers()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	if len(users) == 0 {
		fmt.Println("No users found")
		return
	}

	fmt.Printf("%-5s %-20s %-10s %-20s\n", "ID", "Username", "Role", "Created At")
	fmt.Println(strings.Repeat("-", 60))
	for _, u := range users {
		fmt.Printf("%-5d %-20s %-10s %-20s\n",
			u.ID, u.Username, u.Role, u.CreatedAt.Format("2006-01-02 15:04:05"))
	}
}
