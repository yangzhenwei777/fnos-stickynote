package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"stickynote/database"

	"modernc.org/sqlite"
)

// sqliteConstraintUnique = SQLITE_CONSTRAINT_UNIQUE（SQLite 结果码）。
// 取自 modernc.org/sqlite v1.34.5 的 lib/sqlite_<platform>.go（各平台恒为 2067）。
// 未直接 import modernc.org/sqlite/lib/sqlite3：该包在本模块未被 require（lib/ 目录
// 在上游是独立 module），引入会新增一条重量级依赖；SQLite 结果码属冻结的 C ABI 常量，
// 本地声明与引用上游等价，且免去依赖漂移风险。升级驱动时应重新核对此值。
const sqliteConstraintUnique = 2067

type User struct {
	ID                 int64  `json:"id"`
	Username           string `json:"username"`
	PasswordHash       string `json:"-"`
	SecurityQuestion   string `json:"security_question"`
	SecurityAnswerHash string `json:"-"`
	Role               string `json:"role"`
	TokenVersion       int64  `json:"-"`
	Status             string `json:"status"`
	TotpEnabled        bool   `json:"totp_enabled"`
	TotpSecret         string `json:"-"`
	// RefreshJTI 当前有效 refresh token 的 jti（v21.93 P1：一次性轮转）。
	// 为空表示存量用户尚未轮转过（升级后首次 refresh 接受旧票并写入新 jti）。
	RefreshJTI string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func CreateUser(username, password, securityQuestion, securityAnswer, role string) (*User, error) {
	// 检查是否已有用户，如果没有则默认设为管理员
	hasUsers := false
	if err := database.DB.QueryRow("SELECT COUNT(*) > 0 FROM users").Scan(&hasUsers); err != nil {
		return nil, fmt.Errorf("check existing users: %w", err) // v21.58 F-16：错误不再忽略
	}
	if !hasUsers {
		role = "admin"
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	var answerHash string
	if securityAnswer != "" {
		normalized := strings.ToLower(strings.TrimSpace(securityAnswer))
		hash, err := bcrypt.GenerateFromPassword([]byte(normalized), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		answerHash = string(hash)
	}

	result, err := database.DB.Exec(
		"INSERT INTO users (username, password_hash, security_question, security_answer_hash, role) VALUES (?, ?, ?, ?, ?)",
		username, string(passwordHash), securityQuestion, answerHash, role,
	)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &User{
		ID:               id,
		Username:         username,
		SecurityQuestion: securityQuestion,
		Role:             role,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}, nil
}

// ErrAccountDisabled 账号被禁用（v21.58 P3 审计修复）：
// 登录接口对外统一返回"用户名或密码错误"（防账户状态枚举），
// 真实原因通过哨兵错误 + 审计日志保留，不损失管理员审计能力。
var ErrAccountDisabled = errors.New("account disabled")

func AuthenticateUser(username, password string) (*User, error) {
	user := &User{}
	err := database.DB.QueryRow(
		"SELECT id, username, password_hash, security_question, role, token_version, status, totp_enabled, totp_secret, refresh_jti, created_at, updated_at FROM users WHERE username = ?",
		username,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.SecurityQuestion, &user.Role, &user.TokenVersion, &user.Status, &user.TotpEnabled, &user.TotpSecret, &user.RefreshJTI, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("用户名或密码错误")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("用户名或密码错误")
	}

	if user.Status == "disabled" {
		// 对外统一文案（防枚举）；调用方用 errors.Is 识别并写审计日志
		return nil, fmt.Errorf("用户名或密码错误: %w", ErrAccountDisabled)
	}

	return user, nil
}

func GetUserByID(id int64) (*User, error) {
	user := &User{}
	err := database.DB.QueryRow(
		"SELECT id, username, security_question, role, token_version, status, totp_enabled, totp_secret, refresh_jti, created_at, updated_at FROM users WHERE id = ?",
		id,
	).Scan(&user.ID, &user.Username, &user.SecurityQuestion, &user.Role, &user.TokenVersion, &user.Status, &user.TotpEnabled, &user.TotpSecret, &user.RefreshJTI, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// BumpTokenVersion 使该用户全部已签发 refresh token 立即失效（全局吊销会话）。
// 修改/重置密码、登出、TOTP 变更时调用；当前 access token 最长 2 小时后自然过期。
//
// v21.93 P1：同时清空 refresh_jti——token_version 递增后旧 refresh 一律失效，
// 残留 jti 会让下一轮换票把「全新签发的票」误判为重放（用户被无辜踢下线）。
//
// v21.94 P3-7：一并撤销该用户全部 refresh_sessions 活跃会话（多设备模型下
// 全局吊销=全设备下线，与「登出=全端退出」既有语义一致）。
func BumpTokenVersion(id int64) error {
	if _, err := database.DB.Exec(
		"UPDATE users SET token_version = token_version + 1, refresh_jti = '' WHERE id = ?", id); err != nil {
		return err
	}
	return RevokeAllUserRefreshSessions(id)
}

// UpdateRefreshJTI 记录当前有效 refresh token 的 jti（一次性轮转的落库动作）。
// v21.93 P1：Refresh 签发新票后写入；后续用旧票换票时 jti 不匹配即判定为重放。
func UpdateRefreshJTI(id int64, jti string) error {
	_, err := database.DB.Exec("UPDATE users SET refresh_jti = ? WHERE id = ?", jti, id)
	return err
}

func GetUserByUsername(username string) (*User, error) {
	user := &User{}
	err := database.DB.QueryRow(
		"SELECT id, username, security_question, security_answer_hash, role, created_at, updated_at FROM users WHERE username = ?",
		username,
	).Scan(&user.ID, &user.Username, &user.SecurityQuestion, &user.SecurityAnswerHash, &user.Role, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func ListUsers() ([]User, error) {
	rows, err := database.DB.Query(
		"SELECT id, username, security_question, role, status, totp_enabled, created_at, updated_at FROM users ORDER BY created_at",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.SecurityQuestion, &u.Role, &u.Status, &u.TotpEnabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

func UpdateUserRole(id int64, role string) error {
	_, err := database.DB.Exec("UPDATE users SET role = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", role, id)
	return err
}

func UpdatePassword(id int64, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	// 同时递增 token_version：改密后旧 refresh token 全部失效
	_, err = database.DB.Exec("UPDATE users SET password_hash = ?, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?", string(hash), id)
	if err != nil {
		return err
	}
	// v21.95 B-3（审计 P3）：除 token_version 外显式吊销 refresh_sessions，
	// 与 BumpTokenVersion 语义对齐（改密=全设备下线），避免会话表残留活跃行。
	return RevokeAllUserRefreshSessions(id)
}

// UpdateUserTx 在事务内更新用户角色与密码（v21.66 P2：消除「角色已改、密码失败」的半成功状态）。
// role/password 为空表示不修改对应字段。
func UpdateUserTx(id int64, role, password string) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	passwordChanged := false
	if role != "" {
		if _, err := tx.Exec("UPDATE users SET role = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", role, id); err != nil {
			return err
		}
	}
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			"UPDATE users SET password_hash = ?, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
			string(hash), id,
		); err != nil {
			return err
		}
		passwordChanged = true
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// v21.95 B-3（审计 P3）：密码变更时事务外显式吊销 refresh_sessions
	//（不占事务连接、幂等：revoked_at IS NULL 条件）。
	if passwordChanged {
		return RevokeAllUserRefreshSessions(id)
	}
	return nil
}

func UpdatePasswordByUsername(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result, err := database.DB.Exec("UPDATE users SET password_hash = ?, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE username = ?", string(hash), username)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("用户不存在")
	}
	// v21.95 B-3（审计 P3）：显式吊销该用户 refresh 会话（找回密码重置后全设备下线）
	if u, err := GetUserByUsername(username); err == nil {
		return RevokeAllUserRefreshSessions(u.ID)
	}
	return nil
}

// UpdateUserStatus 切换用户启用/禁用状态（v20.4）。
// 禁用时递增 token_version 使已登录 token 立即失效。
func UpdateUserStatus(id int64, status string) error {
	result, err := database.DB.Exec(
		"UPDATE users SET status = ?, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		status, id,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("用户不存在")
	}
	// v21.95 B-3（审计 P3）：禁用/启用切换后显式吊销 refresh 会话（禁用=强制下线）
	return RevokeAllUserRefreshSessions(id)
}

// AdminResetPassword 管理员重置用户密码（v20.4）。
func AdminResetPassword(id int64, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result, err := database.DB.Exec(
		"UPDATE users SET password_hash = ?, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		string(hash), id,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("用户不存在")
	}
	// v21.95 B-3（审计 P3）：管理员重置密码后显式吊销该用户全部 refresh 会话
	return RevokeAllUserRefreshSessions(id)
}

func DeleteUser(id int64) error {
	_, err := database.DB.Exec("DELETE FROM users WHERE id = ?", id)
	return err
}

// RegisterUser 单语句原子注册（v19.5 A/B 优化）：角色判定与 INSERT 合为一条 SQL，
// 不再依赖 SetMaxOpenConns(1) 的事务串行——CASE WHEN 在同一语句内完成 COUNT(*) 判定，
// WHERE NOT EXISTS 保护用户名唯一性（UNIQUE 约束兜底）。
// bcrypt 慢速哈希仍在 SQL 外执行，不占连接。
func RegisterUser(username, password, securityQuestion, securityAnswer string) (*User, error) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	var answerHash string
	if securityAnswer != "" {
		normalized := strings.ToLower(strings.TrimSpace(securityAnswer))
		hash, err := bcrypt.GenerateFromPassword([]byte(normalized), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		answerHash = string(hash)
	}

	result, err := database.DB.Exec(`
		INSERT INTO users (username, password_hash, security_question, security_answer_hash, role)
		SELECT ?, ?, ?, ?,
			CASE WHEN (SELECT COUNT(*) FROM users) = 0 THEN 'admin' ELSE 'user' END
		WHERE NOT EXISTS (SELECT 1 FROM users WHERE username = ?)
	`, username, string(passwordHash), securityQuestion, answerHash, username)
	if err != nil {
		// v21.91（审计 P2-20）：UNIQUE 约束冲突改用结构化错误码判断——
		// 原 strings.Contains(err.Error(), "UNIQUE") 是字符串匹配，驱动升级或错误文案
		// 变化即静默失效（退化为用户看到 500 而非"用户名已存在"）。
		// modernc.org/sqlite 的 *sqlite.Error 暴露 Code()，直接与 SQLITE_CONSTRAINT_UNIQUE 比对。
		// 注：SQL 中 WHERE NOT EXISTS 已先挡一道，此处仅拦并发竞态下 UNIQUE 索引兜底触发的冲突。
		var dbErr *sqlite.Error
		if errors.As(err, &dbErr) && dbErr.Code() == sqliteConstraintUnique {
			return nil, fmt.Errorf("用户名已存在")
		}
		return nil, err
	}
	// v21.58 F-16：错误不再忽略（RowsAffected / LastInsertId / role 读取）
	n, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("get rows affected: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("用户名已存在")
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get inserted user id: %w", err)
	}
	var role string
	if err := database.DB.QueryRow("SELECT role FROM users WHERE id = ?", id).Scan(&role); err != nil {
		// v21.67 P2：role 查询失败显式上报，不再以空 role 继续返回（首管理员可能因此丢 admin）
		return nil, fmt.Errorf("load created user role: %w", err)
	}
	return &User{
		ID:               id,
		Username:         username,
		SecurityQuestion: securityQuestion,
		Role:             role,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}, nil
}

func CountUsers() (int, error) {
	var count int
	err := database.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

func VerifySecurityAnswer(username, answer string) (bool, error) {
	user, err := GetUserByUsername(username)
	if err != nil {
		return false, fmt.Errorf("用户不存在")
	}

	if user.SecurityAnswerHash == "" {
		return false, fmt.Errorf("未设置安全问题")
	}

	normalized := strings.ToLower(strings.TrimSpace(answer))
	err = bcrypt.CompareHashAndPassword([]byte(user.SecurityAnswerHash), []byte(normalized))
	return err == nil, nil
}

func UpdateSecurityQuestion(id int64, question, answer string) error {
	var answerHash string
	if answer != "" {
		normalized := strings.ToLower(strings.TrimSpace(answer))
		hash, err := bcrypt.GenerateFromPassword([]byte(normalized), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		answerHash = string(hash)
	}
	_, err := database.DB.Exec("UPDATE users SET security_question = ?, security_answer_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", question, answerHash, id)
	return err
}

func GetAdminUser() (*User, error) {
	user := &User{}
	err := database.DB.QueryRow(
		"SELECT id, username, role, created_at FROM users WHERE role = 'admin' ORDER BY id LIMIT 1",
	).Scan(&user.ID, &user.Username, &user.Role, &user.CreatedAt)
	if err != nil {
		return nil, err
	}
	return user, nil
}
