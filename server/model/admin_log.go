package model

import (
	"time"

	"stickynote/database"
	"stickynote/logger"
)

// AdminLog 管理员操作日志
type AdminLog struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Username  string    `json:"username"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// LogAdminAction 记录管理员操作日志。
func LogAdminAction(userID int64, username, action, target, detail, ip string) {
	if _, err := database.DB.Exec(
		"INSERT INTO admin_logs (user_id, username, action, target, detail, ip) VALUES (?, ?, ?, ?, ?, ?)",
		userID, username, action, target, detail, ip,
	); err != nil {
		logger.Error("LogAdminAction insert failed: %v", err) // v21.58 F-16：错误不再忽略
	}
}

// ListAdminLogs 分页查询操作日志，按时间倒序。
// action/username/startDate/endDate 为空时不过滤对应维度（向后兼容）。
func ListAdminLogs(page, pageSize int, action, username, startDate, endDate string) ([]AdminLog, int, error) {
	where := ""
	args := []interface{}{}
	if action != "" {
		where += " AND action = ?"
		args = append(args, action)
	}
	if username != "" {
		where += " AND username LIKE ?"
		args = append(args, "%"+username+"%")
	}
	if startDate != "" {
		where += " AND created_at >= ?"
		args = append(args, startDate+" 00:00:00")
	}
	if endDate != "" {
		where += " AND created_at <= ?"
		args = append(args, endDate+" 23:59:59")
	}

	countSQL := "SELECT COUNT(*) FROM admin_logs WHERE 1=1" + where
	var total int
	rowArgs := make([]interface{}, len(args))
	copy(rowArgs, args)
	if err := database.DB.QueryRow(countSQL, rowArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// v21.58 F-15：分页 page 极端值钳制（handler 层只 clamp 了 pageSize，此处补齐 page<1）
	// v21.66 P2：补 page 上限，防极端 offset
	const maxPage = 100000
	if page < 1 {
		page = 1
	} else if page > maxPage {
		page = maxPage
	}
	offset := (page - 1) * pageSize
	querySQL := "SELECT id, user_id, username, action, target, detail, ip, created_at FROM admin_logs WHERE 1=1" + where + " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, pageSize, offset)
	rows, err := database.DB.Query(querySQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []AdminLog
	for rows.Next() {
		var l AdminLog
		if err := rows.Scan(&l.ID, &l.UserID, &l.Username, &l.Action, &l.Target, &l.Detail, &l.IP, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		logs = append(logs, l)
	}
	return logs, total, nil
}
