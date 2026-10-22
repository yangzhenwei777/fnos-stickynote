// Package localauth 桌面设备会话（离线方案 v20 P3 · 批次 4 刀 2）。
//
// 姿态（§87 口径，不吹牛）：
//   - access token 只存内存（Session），永不落盘；
//   - refresh token 经 DPAPI（用户域 + 应用熵 + nonce 绑定）封存后存 client_profile 表——
//     防"拷文件走就能续会话"，不防同用户恶意进程（DPAPI 官方语义，与威胁模型一致）；
//   - device_secret：首登本机生成的 32B 随机设备密钥，同样 DPAPI 封存落库。它是
//     "设备会话"的锚（PIN/NPD1 封套离线解锁在刀 2 只落 schema 位，解封链归离线解锁收口）；
//   - 换机器 = DPAPI 解不开 → 必须重新联网登录（登录即重封，refresh 轮转随登录自然发生）。
//
// client_profile 随当前 generation 库走（localstore 纪律内）；旧 device_id 文件一次性
// 导入表内（文件原位保留作灾备，与刀 1 骨架库同策略）。
package localauth

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"stickynote-desktop/dpapi"
)

// ErrNoProfile 本机尚无会话档案（未登录过）。
var ErrNoProfile = errors.New("localauth: no profile")

// Profile 设备会话档案（单活动 profile 语义：换服务器/账号 = 覆盖，v1 刻意简单）。
type Profile struct {
	DeviceID      string    `json:"device_id"`
	ServerURL     string    `json:"server_url"`
	Username      string    `json:"username"`
	CreatedAt     time.Time `json:"created_at"`
	LastLoginAt   time.Time `json:"last_login_at"`
	LastRevalAt   time.Time `json:"last_revalidate_at"` // 刀 3 重验门控写点
	RefreshSealed []byte    `json:"-"`                  // DPAPI blob（§87 落盘件）
	SecretSealed  []byte    `json:"-"`                  // device_secret 的 DPAPI blob
}

// Session 内存态（登录/恢复成功后返回给绑定层挂 worker 用）。
type Session struct {
	DeviceID     string
	ServerURL    string
	Username     string
	AccessToken  string // 仅内存
	DeviceSecret []byte // 仅内存（本机会话期持明文）
	FromRestore  bool   // true = 未走登录，由落盘 refresh/secret 恢复
}

// EnsureTables 建 client_profile 单例表（id=1 恒一行；幂等）。
func EnsureTables(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS client_profile (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		device_id TEXT NOT NULL,
		server_url TEXT NOT NULL DEFAULT '',
		username TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		last_login_at TEXT NOT NULL DEFAULT '',
		last_revalidate_at TEXT NOT NULL DEFAULT '',
		refresh_sealed BLOB,
		secret_sealed BLOB,
		pin_verifier TEXT
	)`)
	if err != nil {
		return err
	}
	// 刀 3 增列（老库幂等迁移；pin_verifier = NPD1(base64(device_secret), PIN)）。
	if !pinColumnExists(db) {
		if _, err := db.Exec(`ALTER TABLE client_profile ADD COLUMN pin_verifier TEXT`); err != nil {
			return err
		}
	}
	return nil
}

func pinColumnExists(db *sql.DB) bool {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('client_profile')`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && n == "pin_verifier" {
			return true
		}
	}
	return false
}

func seal(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil // 无内容 = 留空列（区别于封存失败）
	}
	return dpapi.Protect(b)
}

func unseal(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	return dpapi.Unprotect(b)
}

// SignIn 登录并落盘：换票 → 生成/沿设备份 → refresh+device_secret DPAPI 封存入库。
// login 由调用方注入（解耦 synclocal，测试可替身）；返回内存 Session。
func SignIn(db *sql.DB, baseURL, username string, login func() (access, refresh string, err error)) (*Session, error) {
	return SignInState(db, baseURL, username, login)
}

// SignInState 是登录落盘的唯一实现（`SignIn` 是它的薄封装，禁双源）。
//
// ⭐ v21.203：回读失败不得把已落盘的登录报成失败。
//
// 定罪链：末尾的 UPSERT 已经**提交**（档案、封存 refresh、device_secret 全部写盘），
// 随后 `Load(db)` 读回。若此刻 DPAPI 解封瞬态失败（Windows 更新/用户上下文切换 —
// 项目里已有同样处置，见 restoreDeviceSession 的 500ms 重试）或 DB 短暂 locked，
// 旧实现 `return nil, err` ⇒ 绑定层报"登录失败" ⇒ **用户重登**，而档案其实已经是
// 登录态 ⇒ 越重登越乱（这与 202 版"登录成功被报失败"是同一族问题的两个入口）。
//
// 现在：回读失败时**不返回错误**，改用 UPSERT 时已知的入参构造 Session 返回
// （这些值就是刚写进去的字节，不存在"读到旧值"的可能）。仅当 DB 层面连 UPSERT
// 都失败时才上抛 —— 那才是真的没登录成功。
func SignInState(db *sql.DB, baseURL, username string, login func() (access, refresh string, err error)) (*Session, error) {
	if err := EnsureTables(db); err != nil {
		return nil, err
	}
	access, refresh, err := login()
	if err != nil {
		return nil, err
	}
	refreshBlob, err := seal([]byte(refresh))
	if err != nil {
		return nil, fmt.Errorf("seal refresh: %w", err)
	}

	var prevSealed []byte
	// 沿用既有 device_secret（设备身份跨登录稳定）；无档案行/无封存件都按新生成处理。
	if qerr := db.QueryRow(`SELECT secret_sealed FROM client_profile WHERE id=1`).Scan(&prevSealed); qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return nil, qerr
	}
	secret, err := unseal(prevSealed)
	if err != nil || len(secret) != 32 {
		secret = make([]byte, 32)
		if _, rerr := rand.Read(secret); rerr != nil {
			return nil, rerr
		}
	}
	secretBlob, err := seal(secret)
	if err != nil {
		return nil, fmt.Errorf("seal device_secret: %w", err)
	}

	// 设备号先备后插（首登生成；再登沿用表内既有值——INSERT 分支携带，UPDATE 分支不动列）。
	devID, err := ensureDeviceID(db)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(tsLayout)
	trimmed := strings.TrimRight(baseURL, "/")
	if _, err := db.Exec(`INSERT INTO client_profile (id, device_id, server_url, username, last_login_at, refresh_sealed, secret_sealed)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET server_url=excluded.server_url, username=excluded.username,
			last_login_at=excluded.last_login_at, refresh_sealed=excluded.refresh_sealed, secret_sealed=excluded.secret_sealed,
			device_id=CASE WHEN COALESCE(client_profile.device_id,'')='' THEN excluded.device_id ELSE client_profile.device_id END`,
		devID, trimmed, username, now, refreshBlob, secretBlob); err != nil {
		return nil, err
	}
	// v21.203：回读是**校验**而非**前提**。失败只记日志，用刚写入的权威值继续。
	s, lerr := Load(db)
	if lerr != nil {
		log.Printf("[auth] sign-in profile read-back failed after successful write; "+
			"continuing with in-hand values (login is NOT rolled back): %v", lerr)
		return &Session{DeviceID: devID, ServerURL: trimmed, Username: username,
			AccessToken: access, DeviceSecret: secret}, nil
	}
	return &Session{DeviceID: s.DeviceID, ServerURL: s.ServerURL, Username: s.Username,
		AccessToken: access, DeviceSecret: secret}, nil
}

// ensureDeviceID 设备号补齐（SignIn 首次插入时 device_id 可能为空串）。
func ensureDeviceID(db *sql.DB) (string, error) {
	var id string
	if err := db.QueryRow(`SELECT device_id FROM client_profile WHERE id=1`).Scan(&id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err // 无行=首登合法空态，按空串走生成路径
	}
	if id != "" {
		return id, nil
	}
	id = randDeviceID()
	if _, err := db.Exec(`UPDATE client_profile SET device_id=? WHERE id=1`, id); err != nil {
		return "", err
	}
	return id, nil
}

// Load 读档案并 DPAPI 解封 refresh/secret。无行返回 ErrNoProfile；解封失败原样上抛
// （ErrCorrupted/ErrUnsupported——换机场景调用方据此降级为"需重新登录"，不静默）。
func Load(db *sql.DB) (*Profile, error) {
	if err := EnsureTables(db); err != nil {
		return nil, err
	}
	p := &Profile{}
	var created, login, reval sql.NullString
	var rfNull, scNull bool
	err := db.QueryRow(`SELECT device_id, server_url, username, created_at, last_login_at, last_revalidate_at,
		refresh_sealed IS NULL, secret_sealed IS NULL, refresh_sealed, secret_sealed
		FROM client_profile WHERE id=1`,
	).Scan(&p.DeviceID, &p.ServerURL, &p.Username, &created, &login, &reval, &rfNull, &scNull, &p.RefreshSealed, &p.SecretSealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoProfile
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = parseTS(created)
	p.LastLoginAt = parseTS(login)
	p.LastRevalAt = parseTS(reval)
	if rfNull {
		p.RefreshSealed = nil
	}
	if scNull {
		p.SecretSealed = nil
	}
	return p, nil
}

// OpenSession 冷启动恢复：不落网络，从封存档位解出 device_secret 即视为设备会话成立。
// refresh 明文一并在内存中回传（调用方用于换 access；access 不存于此——每次都是新的）。
func OpenSession(db *sql.DB) (sess *Session, refreshToken string, err error) {
	p, err := Load(db)
	if err != nil {
		return nil, "", err
	}
	secret, err := unseal(p.SecretSealed)
	if err != nil {
		return nil, "", err
	}
	if len(secret) != 32 {
		return nil, "", dpapi.ErrCorrupted
	}
	refreshBlob, err := unseal(p.RefreshSealed)
	if err != nil {
		return nil, "", err
	}
	return &Session{DeviceID: p.DeviceID, ServerURL: p.ServerURL, Username: p.Username,
		DeviceSecret: secret, FromRestore: true}, string(refreshBlob), nil
}

// SaveRefresh 覆盖 refresh 封存件（refresh 轮转后必须立即调用——一次一换语义）。
// 空串拒绝（废票落盘不如不落）。无档案行报 ErrNoProfile。
//
// ⭐ v21.203：加 CAS（compare-and-swap）语义，防跨账号串写。
//
// 定罪：旧实现是无条件 `UPDATE ... WHERE id=1`。client_profile 是**单行**表，
// 而写入方有三个（登录 SignIn、换票 SaveRefresh、登出 DropRefreshSealed），
// 且 SaveRefresh 来自 `ensureAccess` 的后台路径。若用户在这条后台换票的
// 网络往返（最长 15s）窗口内切了账号（B 登录完成、档案已换成 B 的），
// 迟到的 A 的轮转票就会**覆盖 B 的封存件** ⇒ B 一换票就用 A 的票 ⇒ 服务端
// family 不匹配 → B 被判 AUTH_REVOKED → B 被迫重登，且 A 的票也一并作废。
//
// 现在：调用方传入本轮换票所依据的**期望旧值**（CAS 令牌），UPDATE 带
// `refresh_sealed = expectSealed` 条件；行数不符即说明档案已被别人换过
// （换账号/登出/换代）⇒ 报 ErrProfileChanged，调用方**丢弃这张票**而不是覆盖。
func SaveRefreshCAS(db *sql.DB, refreshToken string, expectSealed []byte) error {
	if refreshToken == "" {
		return errors.New("localauth: empty refresh token")
	}
	blob, err := seal([]byte(refreshToken))
	if err != nil {
		return fmt.Errorf("seal refresh: %w", err)
	}
	var res sql.Result
	if len(expectSealed) == 0 {
		// 期望旧态 = 空（首次建档/已登出）⇒ 只在当前确为空时写。
		res, err = db.Exec(`UPDATE client_profile SET refresh_sealed=? WHERE id=1
			AND (refresh_sealed IS NULL OR refresh_sealed = '')`, blob)
	} else {
		res, err = db.Exec(`UPDATE client_profile SET refresh_sealed=? WHERE id=1
			AND refresh_sealed=?`, blob, expectSealed)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProfileChanged
	}
	return nil
}

// ErrProfileChanged CAS 失败：档案在本轮操作期间被换账号/登出/换代改写。
// 调用方必须**丢弃**手上的新票（它属于另一个账号的服务端 family），绝不能强行写入。
var ErrProfileChanged = errors.New("localauth: profile changed concurrently")

// CurrentRefreshSealed 读当前封存 refresh 的**原始字节**（CAS 令牌用）。
// 无档案行 / 列为 NULL / 空串 ⇒ 返回 nil（表示"期望旧态为空"）。
func CurrentRefreshSealed(db *sql.DB) []byte {
	var s []byte
	if err := db.QueryRow(`SELECT refresh_sealed FROM client_profile WHERE id=1`).Scan(&s); err != nil {
		return nil
	}
	if len(s) == 0 {
		return nil
	}
	return s
}

// SaveRefresh 无条件覆盖（**仅供登录路径**使用：SignIn 已在本函数外持 exchangeMu，
// 且登录本身就是"换账号"动作，不存在 CAS 判据）。换票路径必须用 SaveRefreshCAS。
func SaveRefresh(db *sql.DB, refreshToken string) error {
	if refreshToken == "" {
		return errors.New("localauth: empty refresh token")
	}
	blob, err := seal([]byte(refreshToken))
	if err != nil {
		return fmt.Errorf("seal refresh: %w", err)
	}
	res, err := db.Exec(`UPDATE client_profile SET refresh_sealed=? WHERE id=1`, blob)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoProfile
	}
	return nil
}

// DropRefreshSealed 清除 refresh 封存件（登出/服务端明确吊销路径）。保留
// device_secret/PIN 封套——重登沿用设备身份（红线 1/6：本地身份与数据不因登出受损）。
func DropRefreshSealed(db *sql.DB) error {
	_, err := db.Exec(`UPDATE client_profile SET refresh_sealed=NULL WHERE id=1`)
	return err
}

// TouchRevalidate 记录一次成功重验时刻（刀 3 门控读取）。无档案行时报 ErrNoProfile（不静默造行）。
func TouchRevalidate(db *sql.DB, at time.Time) error {
	res, err := db.Exec(`UPDATE client_profile SET last_revalidate_at=? WHERE id=1`,
		at.UTC().Format(tsLayout))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoProfile
	}
	return nil
}

// ImportLegacyDeviceID 旧 device_id 文件一次性导入（幂等；表内已有非空 device_id 时跳过；
// 文件原位保留）。返回 didImport。
func ImportLegacyDeviceID(db *sql.DB, profileDir string) (bool, error) {
	if err := EnsureTables(db); err != nil {
		return false, err
	}
	var id string
	if err := db.QueryRow(`SELECT device_id FROM client_profile WHERE id=1`).Scan(&id); err == nil && id != "" {
		return false, nil // 已有权威设备号
	}
	b, err := os.ReadFile(profileDir + string(os.PathSeparator) + "device_id")
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	leg := strings.TrimSpace(string(b))
	if leg == "" {
		return false, nil
	}
	// 表可能不存在行（从未 SignIn）：upsert 单例。
	_, err = db.Exec(`INSERT INTO client_profile (id, device_id) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET device_id=excluded.device_id WHERE client_profile.device_id=''`, leg)
	return err == nil, err
}

func parseTS(s sql.NullString) time.Time {
	if !s.Valid || s.String == "" {
		return time.Time{}
	}
	for _, layout := range []string{tsLayout, time.RFC3339} {
		if t, err := time.Parse(layout, s.String); err == nil {
			return t
		}
	}
	return time.Time{}
}

const tsLayout = time.RFC3339 + ".000Z" // = RFC3339Milli 口径（列内文本统一毫秒）

func randDeviceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	const hexd = "0123456789abcdef"
	out := make([]byte, 16)
	for i, v := range b {
		out[2*i] = hexd[v>>4]
		out[2*i+1] = hexd[v&0x0f]
	}
	return string(out)
}
