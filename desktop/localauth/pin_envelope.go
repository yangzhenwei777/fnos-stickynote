package localauth

// pin_envelope.go —— 批次 4 刀 3：PIN/NPD1 封套（方案 1，用户拍板 2026-09-20）。
//
// pin_verifier = NPD1( base64(device_secret), PIN )——**复用服务端权威 crypto.Encrypt**：
// 600k PBKDF2-SHA256 + AES-GCM + 自描述 magic（NPD1 冻结向量契约同源，Android 对拍一致）。
// 语义：
//   - SetPIN：把当前会话 device_secret 封进 verifier 列（DPAPI 层之外加"人"层）；
//   - UnlockPIN：离线解锁——只依赖库内 verifier，不触网、无监听（红线 29）；
//     错误 PIN 由 GCM tag 拒绝（fail-closed），进程内连错 MaxUnlockFailures 次熔断，
//     复位只能重登（在线动作）——600k 迭代 + 熔断双保险，不做服务端联动（本地锁语义）。
//   - RemovePIN：解除（需已解锁会话），verifier 置 NULL。

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"stickynote/crypto"
)

// ErrWrongPIN PIN 不符（含 GCM 篡改拒绝——不区分，防 oracle）。
var ErrWrongPIN = errors.New("localauth: wrong PIN")

// ErrLocked 熔断：连错次数超限。
var ErrLocked = errors.New("localauth: unlock locked, re-login required")

// MaxUnlockFailures 进程内连错上限（重启进程即清零——代价由 600k 迭代兜底）。
const MaxUnlockFailures = 10

// ⭐ v21.203：unlockFailures 原是**裸包级 int**，无同步。
//
// 定罪（审计）：`UnlockLocal` 是 Wails 绑定（前端可并发调用），而 `ResetUnlockFailures`
// 由登录/换票路径调用。两条路径并发时对同一个 int 做读-改-写（+1 / =0），
// 属于标准 data race —— `-race` 门禁一开必红。更实际的后果是计数被并发写丢：
// 连错 10 次本该熔断，并发下的读-改-写交错可能让计数停在中途甚至被清零。
var (
	unlockMu       sync.Mutex
	unlockFailures int
)

// unlockFailureCount 读当前连错计数（快照语义，供 UI/诊断）。
func unlockFailureCount() int {
	unlockMu.Lock()
	defer unlockMu.Unlock()
	return unlockFailures
}

// SetPIN 用 PIN 封存设备密钥。pin 至少 6 位（对齐产品口令下限口径 min=6 既定决策）。
func SetPIN(db *sql.DB, pin string, secret []byte) error {
	if len(pin) < 6 {
		return errors.New("localauth: PIN too short (min 6)")
	}
	if len(secret) != 32 {
		return errors.New("localauth: bad device secret length")
	}
	b64 := base64.StdEncoding.EncodeToString(secret)
	verifier, err := crypto.Encrypt(b64, pin)
	if err != nil {
		return fmt.Errorf("seal pin envelope: %w", err)
	}
	res, err := db.Exec(`UPDATE client_profile SET pin_verifier=? WHERE id=1`, verifier)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoProfile
	}
	return nil
}

// UnlockPIN 离线解锁，返回内存态 device_secret。
func UnlockPIN(db *sql.DB, pin string) ([]byte, error) {
	unlockMu.Lock()
	failures := unlockFailures
	unlockMu.Unlock()
	if failures >= MaxUnlockFailures {
		return nil, ErrLocked
	}
	var verifier sql.NullString
	err := db.QueryRow(`SELECT pin_verifier FROM client_profile WHERE id=1`).Scan(&verifier)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoProfile
	}
	if err != nil {
		return nil, err
	}
	if !verifier.Valid || verifier.String == "" {
		return nil, errors.New("localauth: PIN not enrolled")
	}
	b64, err := crypto.Decrypt(verifier.String, pin)
	if err != nil {
		recordUnlockFailure()
		return nil, ErrWrongPIN // NPD1 内部错误不外泄（格式/口令统一此哨兵）
	}
	secret, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(secret) != 32 || !allZeroTail(secret) {
		recordUnlockFailure()
		return nil, ErrWrongPIN
	}
	ResetUnlockFailures()
	return secret, nil
}

// recordUnlockFailure 原子累加连错计数（v21.203：原为裸 +1，并发下会丢计数）。
func recordUnlockFailure() {
	unlockMu.Lock()
	unlockFailures++
	unlockMu.Unlock()
}

// allZeroTail 形状哨兵：解封出的 secret 必须是合法 32B（base64 已保证），此函数仅占位
// 校验非全零（全零 = 伪造行，视同错误 PIN）。
func allZeroTail(s []byte) bool { return !bytes.Equal(s, make([]byte, 32)) }

// ResetUnlockFailures **用户显式完成认证**时复位熔断（登录成功 / 正确 PIN 解锁）。
//
// ⚠ v21.203 纪律：只允许挂在「用户刚证明了自己是谁」的路径上，**禁止**挂在
// 后台换票成功路径（`ensureAccess` 的 Refresh 成功）。后者是机器行为不是人的行为：
// 用户连错 9 次 PIN 后，一个碰巧成功的后台换票就把熔断清零 ⇒ 600k 迭代的暴力
// 防护被无限削弱（10 次上限形同虚设）。原实现正是挂在 ensureAccess 上的。
func ResetUnlockFailures() {
	unlockMu.Lock()
	unlockFailures = 0
	unlockMu.Unlock()
}

// UnlockFailureCount 当前连错次数（诊断面）。
func UnlockFailureCount() int { return unlockFailureCount() }

// RemovePIN 解除 PIN 封套（调用方须已持有解锁会话）。
func RemovePIN(db *sql.DB) error {
	_, err := db.Exec(`UPDATE client_profile SET pin_verifier=NULL WHERE id=1`)
	return err
}

// PINEnrolled 数据中心可见性。
func PINEnrolled(db *sql.DB) bool {
	var s sql.NullString
	if err := db.QueryRow(`SELECT pin_verifier FROM client_profile WHERE id=1`).Scan(&s); err != nil {
		return false
	}
	return s.Valid && s.String != ""
}

var _ = rand.Read // 预留：verifier 若升级携带盲化盐再启用，避免 import 反复横跳
