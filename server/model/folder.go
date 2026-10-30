package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"stickynote/database"
)

// v21.86 文件夹：左侧菜单栏的自定义文件夹功能。
// 数据模型（方案 A）：note_folders 表存自定义文件夹；notes.folder_id 挂接归属；
// "我的笔记"为虚拟默认文件夹（folder_id IS NULL），不入库、不可删、不可重命名、永远排第一。

// DefaultFolderName 默认文件夹名（禁止自定义文件夹使用，避免歧义）
const DefaultFolderName = "我的笔记"

// MaxFoldersPerUser 单用户自定义文件夹上限
const MaxFoldersPerUser = 50

// MaxFolderNameLen 文件夹名长度上限（rune 计）
const MaxFolderNameLen = 32

var (
	ErrFolderNotFound   = errors.New("folder not found")
	ErrFolderNotEmpty   = errors.New("folder not empty")
	ErrFolderNameExists = errors.New("folder name exists")
	ErrFolderDefault    = errors.New("default folder is immutable")
)

// Folder 文件夹（API 视图）。ID 为 nil 表示虚拟默认文件夹"我的笔记"。
type Folder struct {
	ID        *int64 `json:"id"` // null = 默认文件夹（folder_id IS NULL）
	UserID    int64  `json:"-"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
	Version   int64  `json:"version"`    // P1 批次2（§18）：乐观锁/CAS 基点；虚拟默认文件夹恒为 0
	NoteCount int    `json:"note_count"` // 未删除笔记数（锁定且本会话未解锁时为 0）
	IsDefault bool   `json:"is_default"`
	// v2.3.0 文件夹访问锁：仅暴露锁定态，密码哈希绝不下发。
	IsEncrypted         bool      `json:"is_encrypted"`
	EncryptPasswordHash string    `json:"-"`
	CreatedAt           time.Time `json:"created_at"`
}

// ValidateFolderName 校验文件夹名：非空、≤32 字、不与默认文件夹重名。返回 trim 后的名称。
func ValidateFolderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("文件夹名称不能为空")
	}
	if utf8.RuneCountInString(name) > MaxFolderNameLen {
		return "", fmt.Errorf("文件夹名称不能超过 %d 个字", MaxFolderNameLen)
	}
	if name == DefaultFolderName {
		return "", fmt.Errorf("不能与默认文件夹【%s】重名", DefaultFolderName)
	}
	return name, nil
}

// ListFolders 返回当前用户全部文件夹：默认项在前，自定义项按 sort_order 升序。
// note_count 仅统计未删除笔记（deleted_at IS NULL）。
//
// ⭐⭐ v21.177（216 实机 P0·连接池死锁定案）：**两条查询必须串行、严禁互相嵌套**。
//
// 定罪（goroutine 转储 + 最小复现双重坐实）：原实现「先开 rows（持连接 1、defer 到函数尾
// 才释放）再发 countRows（要连接 2）」，而池上限 MaxOpenConns(2)。两个并发调用（前端多组件
// 同时加载必然并发发出多个）⇒ 各持 1 条、各等 1 条 ⇒ **永久互锁**（database/sql 等连接无
// 超时），整进程静默瘫痪：UI 列表永不加载（"笔记丢失"观感）/ 同步轮卡死并持着换票锁 /
// 关机「换票仍在途>16s」幽灵警告 +「调度器未在预期时限内退出」。
//
// 修法（本函数）：**先取完全部结果并释放连接，再发下一条查询**——countRows 先跑完即释放，
// 之后才开 rows。同型模式禁止重新引入（回归钉 folder_deadlock_regression_test.go 双保险）。
func ListFolders(userID int64, granted ...map[int64]struct{}) ([]Folder, error) {
	grant := firstGranted(granted)
	// 各文件夹笔记数（含回收站软删的不计，与删除校验口径一致）。
	// ⚠ v21.177 死锁修复点：本查询必须在 rows 打开**之前**完成并释放连接，勿调换顺序。
	countRows, err := database.DB.Query(
		"SELECT folder_id, COUNT(*) FROM notes WHERE user_id = ? AND folder_id IS NOT NULL AND deleted_at IS NULL GROUP BY folder_id",
		userID,
	)
	if err != nil {
		return nil, err
	}
	counts := map[int64]int{}
	for countRows.Next() {
		var fid int64
		var cnt int
		if err := countRows.Scan(&fid, &cnt); err != nil {
			countRows.Close()
			return nil, err
		}
		counts[fid] = cnt
	}
	countRows.Close()
	if err := countRows.Err(); err != nil {
		return nil, err
	}

	// 默认文件夹笔记数（folder_id IS NULL）
	var defaultCount int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM notes WHERE user_id = ? AND folder_id IS NULL AND deleted_at IS NULL",
		userID,
	).Scan(&defaultCount); err != nil {
		return nil, err
	}

	rows, err := database.DB.Query(
		"SELECT id, name, sort_order, version, is_encrypted, created_at FROM note_folders WHERE user_id = ? ORDER BY sort_order ASC, id ASC",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	// 虚拟默认文件夹不落库、不进事件（§55/批次2 范围），Version 恒 0。
	folders := []Folder{{
		ID:        nil,
		UserID:    userID,
		Name:      DefaultFolderName,
		SortOrder: 0,
		NoteCount: defaultCount,
		IsDefault: true,
		CreatedAt: now,
	}}

	for rows.Next() {
		var f Folder
		var id int64
		if err := rows.Scan(&id, &f.Name, &f.SortOrder, &f.Version, &f.IsEncrypted, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.ID = &id
		f.UserID = userID
		f.NoteCount = counts[id]
		if f.IsEncrypted {
			if _, ok := grant[id]; !ok {
				f.NoteCount = 0 // 锁定且本会话未解锁：不泄漏条数
			}
		}
		folders = append(folders, f)
	}
	return folders, nil
}

// GetFolder 取单个自定义文件夹（校验归属）。
func GetFolder(id, userID int64) (*Folder, error) {
	var f Folder
	var fid int64
	err := database.DB.QueryRow(
		"SELECT id, name, sort_order, version, is_encrypted, encrypt_password_hash, created_at FROM note_folders WHERE id = ? AND user_id = ?",
		id, userID,
	).Scan(&fid, &f.Name, &f.SortOrder, &f.Version, &f.IsEncrypted, &f.EncryptPasswordHash, &f.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrFolderNotFound
		}
		return nil, err
	}
	f.ID = &fid
	f.UserID = userID
	return &f, nil
}

// GetNoteFolderName 返回笔记当前归属的自定义文件夹名；
// 归属默认「我的笔记」（folder_id NULL）或文件夹已不存在时返回空串。
// v21.107 P2-9：供回收站恢复响应携带去向文件夹名。
func GetNoteFolderName(noteID int64) (string, error) {
	var fid sql.NullInt64
	if err := database.DB.QueryRow("SELECT folder_id FROM notes WHERE id = ?", noteID).Scan(&fid); err != nil {
		return "", err
	}
	if !fid.Valid || fid.Int64 == 0 {
		return "", nil
	}
	var name string
	if err := database.DB.QueryRow("SELECT name FROM note_folders WHERE id = ?", fid.Int64).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil // 理论不该发生（删文件夹时已清悬空引用），防御性回落默认文件夹
		}
		return "", err
	}
	return name, nil
}

// CreateFolder / RenameFolder / MoveFolder / DeleteFolder 的实现已于 P1 批次2 迁至
// folder_tx.go（*Tx 变体 + 自持事务兼容包装，服务端语义经 app.NewServerFolderService 补齐）。

// reorderIDs 纯函数：把 ids[idx] 按 position 移动后返回新切片，不修改入参。
// v21.101 修复：原 top 分支写作 append([]int64{ids[idx]}, append(ids[:idx], ids[idx+1:]...)...)，
// 而 Go 规范未定义「索引表达式」与「函数调用」之间的求值顺序——内层 append 会原地改写
// ids 的底层数组，外层再取 ids[idx] 拿到的是原 ids[idx+1]，导致目标元素丢失、
// 下一个元素重复。此处统一用「新切片 + 分段 append」，杜绝共享底层数组的副作用。
func reorderIDs(ids []int64, idx int, position string) []int64 {
	if idx < 0 || idx >= len(ids) {
		return ids
	}
	out := make([]int64, 0, len(ids))
	switch position {
	case "up":
		if idx == 0 {
			return ids
		}
		out = append(out, ids[:idx-1]...)
		out = append(out, ids[idx], ids[idx-1])
		out = append(out, ids[idx+1:]...)
	case "down":
		if idx == len(ids)-1 {
			return ids
		}
		out = append(out, ids[:idx]...)
		out = append(out, ids[idx+1], ids[idx])
		out = append(out, ids[idx+2:]...)
	case "top":
		if idx == 0 {
			return ids
		}
		out = append(out, ids[idx])
		out = append(out, ids[:idx]...)
		out = append(out, ids[idx+1:]...)
	case "bottom":
		if idx == len(ids)-1 {
			return ids
		}
		out = append(out, ids[:idx]...)
		out = append(out, ids[idx+1:]...)
		out = append(out, ids[idx])
	default:
		return ids
	}
	return out
}

// NormalizeFolderSortOrder 把某用户的自定义文件夹 sort_order 规范化为 1..n
// （次序按现有 sort_order、id 升序）。返回被修正的行数，0 表示已规范、无需写库。
// v21.101：历史 top 缺陷可能留下重复 sort_order（目标行未被重写），此函数用于自愈。
func NormalizeFolderSortOrder(userID int64) (int, error) {
	rows, err := database.GetDB().Query(
		"SELECT id, sort_order FROM note_folders WHERE user_id = ? ORDER BY sort_order ASC, id ASC",
		userID,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type folderOrder struct {
		id    int64
		order int
	}
	var list []folderOrder
	for rows.Next() {
		var fo folderOrder
		if err := rows.Scan(&fo.id, &fo.order); err != nil {
			return 0, err
		}
		list = append(list, fo)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// ⭐ v21.177：显式关闭读取游标（释放连接）后再进入 UPDATE 事务——「持 Rows 再取连接」
	// 是 216 死锁定案的成因模式（池上限 2），此处同禁（defer 保留为兜底，Close 幂等）。
	_ = rows.Close()

	fixed := 0
	for i, fo := range list {
		if fo.order != i+1 {
			fixed++
		}
	}
	if fixed == 0 {
		return 0, nil
	}

	tx, err := database.GetDB().Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for i, fo := range list {
		if _, err := tx.Exec(
			"UPDATE note_folders SET sort_order = ? WHERE id = ? AND user_id = ?",
			i+1, fo.id, userID,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return fixed, nil
}

// NormalizeAllFolderSortOrders 对所有用户执行一次排序自愈（幂等，供启动期调用）。
// 返回被修正的行数总计；任一用户失败即返回错误，已提交的部分不影响后续重试。
func NormalizeAllFolderSortOrders() (int, error) {
	rows, err := database.GetDB().Query("SELECT id FROM users")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var userIDs []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return 0, err
		}
		userIDs = append(userIDs, uid)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// ⭐ v21.177：读列表即释放连接——外层不得持 Rows 调内层查询（同族死锁模式防线）。
	_ = rows.Close()

	total := 0
	for _, uid := range userIDs {
		n, err := NormalizeFolderSortOrder(uid)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
