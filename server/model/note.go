package model

import (
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"stickynote/database"
)

type Note struct {
	ID                  int64      `json:"id"`
	UserID              int64      `json:"user_id"`
	Title               string     `json:"title"`
	Content             string     `json:"content"`
	Tags                string     `json:"tags"`
	IsEncrypted         bool       `json:"is_encrypted"`
	EncryptPasswordHash string     `json:"-"`
	EncryptedContent    string     `json:"-"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	DeletedAt           *time.Time `json:"deleted_at,omitempty"`
	Highlight           string     `json:"highlight,omitempty"` // P2-2: FTS5 snippet 高亮片段
	Version             int64      `json:"version"`             // v21.72：并发编辑乐观锁版本号
	FolderID            *int64     `json:"folder_id"`           // v21.86：所属文件夹（null = 默认"我的笔记"）
}

type NoteListResponse struct {
	Notes []Note `json:"notes"`
	Total int    `json:"total"`
}

// syncNoteTagsTx 将逗号分隔的 tags 字符串同步到 note_tags 关联表（P3-11）。
// v21.58 P2 审计修复：在**传入事务**内执行且检查全部错误（此前忽略错误 + 非事务），
// 由调用方决定提交/回滚；CreateNote 与 UpdateNote 共用。
func syncNoteTagsTx(tx *sql.Tx, noteID int64, tags string) error {
	if _, err := tx.Exec("DELETE FROM note_tags WHERE note_id = ?", noteID); err != nil {
		return err
	}
	if tags == "" {
		return nil
	}
	for _, t := range strings.Split(tags, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO note_tags (note_id, tag) VALUES (?, ?)", noteID, t); err != nil {
			return err
		}
	}
	return nil
}

// syncNoteTags 兼容包装：独立事务执行（供 UpdateNote 等无配额场景调用）。
func syncNoteTags(noteID int64, tags string) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := syncNoteTagsTx(tx, noteID, tags); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrQuotaExceeded 笔记数量配额超限（v21.58 P1 审计修复：事务内判断，杜绝并发绕过）。
var ErrQuotaExceeded = errors.New("note quota exceeded")

// CreateNote 事务内完成「配额检查 + 插入 + 标签同步」：
// v21.58 P1 审计修复——此前 handler 先 CountActiveNotes 再单独 INSERT（非原子），
// 并发请求可突破 max_notes_per_user；现在 COUNT 与 INSERT 在同一事务，
// 上限 maxNotes（<=0 表示不限）。
func CreateNote(userID int64, title, content, tags string, maxNotes int) (*Note, error) {
	// v21.86：保留原签名（导入/历史调用默认文件夹），文件夹归属走 CreateNoteInFolder
	return CreateNoteInFolder(userID, title, content, tags, maxNotes, nil)
}

// CreateNoteInFolder / UpdateNote / DeleteNote 的实现已于 P2 批次1 迁至 note_tx.go
// （*Tx 变体 + 自持事务兼容包装，供 app Facade 持事务调用）。

func GetNote(id, userID int64) (*Note, error) {
	note := &Note{}
	err := database.DB.QueryRow(
		"SELECT id, user_id, title, content, tags, is_encrypted, encrypt_password_hash, encrypted_content, created_at, updated_at, version, folder_id FROM notes WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		id, userID,
	).Scan(&note.ID, &note.UserID, &note.Title, &note.Content, &note.Tags, &note.IsEncrypted, &note.EncryptPasswordHash, &note.EncryptedContent, &note.CreatedAt, &note.UpdatedAt, &note.Version, &note.FolderID)
	if err != nil {
		return nil, err
	}
	return note, nil
}

// ListNotes 笔记列表。v21.86 新增 folderID 过滤：
// nil = 全部笔记；指向 0 = 默认"我的笔记"（folder_id IS NULL）；>0 = 指定自定义文件夹。
func ListNotes(userID int64, page, pageSize int, search, tag, sortBy string, folderID *int64) (*NoteListResponse, error) {
	// v21.66 P2：page 上限（防极端 offset 整数溢出/大 offset 扫描）
	const maxPage = 100000
	if page < 1 {
		page = 1
	} else if page > maxPage {
		page = maxPage
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	// v21.66 P2：搜索词长度上限（防超长 q 拖垮 FTS/LIKE）
	if utf8.RuneCountInString(search) > 200 {
		search = string([]rune(search)[:200])
	}
	offset := (page - 1) * pageSize

	var total int
	var err error
	var conditions string
	var args []interface{}
	var useFTS bool
	var ftsMatchQ string

	conditions = "WHERE user_id = ? AND deleted_at IS NULL"
	args = append(args, userID)

	if search != "" {
		// 混合检索（v17）：查询词 ≥3 字走 FTS5 trigram 索引（中文子串可命中）；
		// <3 字时 trigram 无法索引，回退 LIKE 全表扫（实测约束，非拍脑袋）。
		if database.FTS5Enabled && utf8.RuneCountInString(strings.TrimSpace(search)) >= 3 {
			useFTS = true
			ftsMatchQ = `"` + strings.ReplaceAll(search, `"`, `""`) + `"`
			// FTS 分支改用 JOIN + snippet（P2-2）：conditions 里不追加子查询，
			// JOIN 在最终 SQL 构造时处理
		} else {
			// 扫 content_text（剥标签后的纯文本，v18.4）：扫 HTML 原文会
			// 命中标签/属性，搜 "img"/"http" 这类词会误命中一大批笔记。
			conditions += " AND (title LIKE ? OR content_text LIKE ?)"
			likeSearch := "%" + search + "%"
			args = append(args, likeSearch, likeSearch)
		}
	}
	if tag != "" {
		// 精确匹配（v18.7）：tags 是逗号分隔串，逗号包裹后匹配完整标签
		conditions += " AND (',' || REPLACE(tags, ' ', '') || ',') LIKE ?"
		args = append(args, "%,"+strings.ReplaceAll(tag, " ", "")+",%")
	}

	// v21.86：文件夹过滤（列名统一带 notes. 前缀——FTS 分支会把 WHERE 替换为 WHERE notes.）
	if folderID != nil {
		if *folderID == 0 {
			conditions += " AND notes.folder_id IS NULL"
		} else {
			conditions += " AND notes.folder_id = ?"
			args = append(args, *folderID)
		}
	}

	// COUNT 查询：FTS 分支用 JOIN，非 FTS 用普通条件
	countSQL := "SELECT COUNT(*) FROM notes " + conditions
	if useFTS {
		countSQL = "SELECT COUNT(*) FROM notes JOIN notes_fts ON notes_fts.rowid = notes.id " +
			strings.Replace(conditions, "WHERE", "WHERE notes.", 1) +
			" AND notes_fts MATCH ?"
		countArgs := append([]interface{}{}, args...)
		countArgs = append(countArgs, ftsMatchQ)
		err = database.DB.QueryRow(countSQL, countArgs...).Scan(&total)
	} else {
		err = database.DB.QueryRow(countSQL, args...).Scan(&total)
	}
	if err != nil {
		return nil, err
	}

	queryArgs := append([]interface{}{}, args...)

	orderClause := "ORDER BY updated_at DESC"
	if sortBy == "title" {
		orderClause = "ORDER BY title ASC"
	} else if sortBy == "created_at" {
		orderClause = "ORDER BY created_at DESC"
	}

	queryArgs = append(queryArgs, pageSize, offset)

	var rows *sql.Rows
	if useFTS {
		// FTS 分支：JOIN notes_fts + snippet() 高亮（P2-2）
		// 参数顺序：conditions 参数 → MATCH → LIMIT → OFFSET
		queryArgs = append([]interface{}{}, args...)
		queryArgs = append(queryArgs, ftsMatchQ, pageSize, offset)
		rows, err = database.DB.Query(
			"SELECT notes.id, notes.user_id, notes.title, notes.content, notes.tags, notes.is_encrypted, notes.encrypt_password_hash, notes.encrypted_content, notes.created_at, notes.updated_at, notes.version, notes.folder_id, "+
				"snippet(notes_fts, 1, '<mark>', '</mark>', '…', 16) as highlight "+
				"FROM notes JOIN notes_fts ON notes_fts.rowid = notes.id "+
				strings.Replace(conditions, "WHERE", "WHERE notes.", 1)+
				" AND notes_fts MATCH ? "+orderClause+" LIMIT ? OFFSET ?",
			queryArgs...,
		)
	} else {
		rows, err = database.DB.Query(
			"SELECT id, user_id, title, content, tags, is_encrypted, encrypt_password_hash, encrypted_content, created_at, updated_at, version, folder_id FROM notes "+conditions+" "+orderClause+" LIMIT ? OFFSET ?",
			queryArgs...,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []Note
	for rows.Next() {
		var n Note
		if useFTS {
			var hl sql.NullString
			if err := rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Content, &n.Tags, &n.IsEncrypted, &n.EncryptPasswordHash, &n.EncryptedContent, &n.CreatedAt, &n.UpdatedAt, &n.Version, &n.FolderID, &hl); err != nil {
				return nil, err
			}
			n.Highlight = hl.String
		} else {
			if err := rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Content, &n.Tags, &n.IsEncrypted, &n.EncryptPasswordHash, &n.EncryptedContent, &n.CreatedAt, &n.UpdatedAt, &n.Version, &n.FolderID); err != nil {
				return nil, err
			}
		}
		// v21.90 P2（审计 L-06）：列表路径统一调用 MaskEncryptedNote——
		// 当前加密时 content 已清空，此调用为防御纵深（未来存储策略变化不回归）。
		MaskEncryptedNote(&n)
		notes = append(notes, n)
	}
	if notes == nil {
		notes = []Note{}
	}

	return &NoteListResponse{Notes: notes, Total: total}, nil
}

// CountActiveNotes 统计用户正常笔记数（回收站不计入，v18.4 配额校验用）。
func CountActiveNotes(userID int64) (int, error) {
	var count int
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM notes WHERE user_id = ? AND deleted_at IS NULL",
		userID,
	).Scan(&count)
	return count, err
}

// ErrVersionConflict 并发编辑冲突（v21.72 P2-08）：版本号不匹配，笔记已被其他设备修改。
var ErrVersionConflict = errors.New("version conflict")

// UpdateNote / DeleteNote 实现已迁至 note_tx.go（P2 批次1）。

// ListTrashedNotes 回收站列表：按删除时间倒序，分页返回（v18.7）。
// 回收站永久保留、只增不减，全量返回迟早撑不住——分页参数与 ListNotes 对齐。
func GetAllTags(userID int64) ([]string, error) {
	// P3-11: 优先 note_tags 关联表，不存在则回退旧逗号串逻辑。
	rows, err := database.DB.Query(
		"SELECT DISTINCT nt.tag FROM note_tags nt JOIN notes n ON n.id = nt.note_id WHERE n.user_id = ? AND n.deleted_at IS NULL ORDER BY nt.tag",
		userID,
	)
	singleTagMode := err == nil
	if err != nil {
		rows, err = database.DB.Query("SELECT DISTINCT tags FROM notes WHERE user_id = ? AND tags != '' AND deleted_at IS NULL", userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tagSet := make(map[string]bool)
	for rows.Next() {
		var val string
		if err := rows.Scan(&val); err != nil {
			return nil, err
		}
		if singleTagMode {
			val = strings.TrimSpace(val)
			if val != "" {
				tagSet[val] = true
			}
		} else {
			for _, tag := range splitTags(val) {
				if tag != "" {
					tagSet[tag] = true
				}
			}
		}
	}

	var tags []string
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	return tags, nil
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, part := range strings.Split(s, ",") {
		t := strings.TrimSpace(part)
		if t != "" {
			result = append(result, t)
		}
	}
	return result
}

// ListAllNotesForExport 导出用：取用户全部正常笔记（不分页，按创建时间升序）。
// 回收站中的笔记不导出。
func ListNotesByIDs(ids []int64, userID int64) ([]Note, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, userID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := "SELECT id, user_id, title, content, tags, is_encrypted, encrypt_password_hash, encrypted_content, created_at, updated_at FROM notes WHERE user_id=? AND deleted_at IS NULL AND id IN (" + strings.Join(placeholders, ",") + ") ORDER BY created_at ASC"
	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Content, &n.Tags, &n.IsEncrypted, &n.EncryptPasswordHash, &n.EncryptedContent, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, nil
}
