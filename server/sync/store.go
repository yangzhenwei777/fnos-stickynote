// Package sync —— 服务端同步底座（离线方案 v20-Project P0-D 第一刀，§21~§44）。
//
// P0-D 范围（最小闭环）：NOTE 实体 CREATE/UPDATE/DELETE/RESTORE 的 push（op_id 幂等 +
// base_version CAS + 实体/seq/ChangeLog 同事务，§24/§42）、pull（fixed watermark +
// (user_id,change_seq) 分页 + ASC 定序，§32/§38/§40）、state（epoch/seq/min_available
// 按用户口径，§7/§37）。FOLDER/FOLDER_ORDER、blob_ref 大载荷、Bootstrap/RESYNC、
// 附件 sha256 管线属 P1 后续刀（SOURCE-FACT §10 已登记）。
//
// 已内嵌的 ADR 决策：
//   - ADR-002：一批一实体一 op，违例 400 BATCH_PROTOCOL_ERROR 整批拒绝零执行；
//   - ADR-004 R2：push 撞物理删行 → ENTITY_GONE（区别于普通 409）；
//   - §8/§43：op 幂等按 (user_id, op_id, request_sha256) 双键，异 hash → OP_ID_REUSE_ERROR；
//   - §44：CREATE 第二层幂等（op 表无记录但 sync_id 已存在且属主一致 → 视为已创建）。
package sync

import (
	"database/sql"
	"errors"

	"stickynote/changelog"
	"stickynote/database"
)

// 错误码常量见 push.go（syncspec 别名，单一契约源）。
//
// P2 批次1 起 nextChangeSeq/appendChange/buildNotePayload 下沉到 changelog 包
// （Web 写路径成为第二生产者，防双源漂移）；此处保留薄委托，push.go 调用点不动。

// db 返回本包当前应使用的 DB 句柄：**注入优先，否则全局单例**。
//
// v21.166 计划项5（X3 销账）：新增包级注入（见 db.go）。生产未注入时
// 走 database.GetDB()，与此前行为逐字一致；测试可 SetDB 注入专用句柄，
// 在本进程内跑真服务端链路测试，摆脱"只能契约假面"的假绿风险。
// 抽出单一决策点：全包只有这里取句柄，杜绝第二个入口导致注入失效。
func db() *sql.DB {
	if h := injectedDB.Load(); h != nil {
		return h
	}
	return database.GetDB()
}

func nextChangeSeq(tx *sql.Tx) (int64, error) { return changelog.NextChangeSeq(tx) }

func currentChangeSeq() (int64, error) {
	var seq int64
	err := db().QueryRow(`SELECT current_change_seq FROM sync_meta WHERE id = 1`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

// appendChange 在调用方事务内写不可变事件（§23/§30：与实体更新、seq 递增同事务）。
func appendChange(tx *sql.Tx, seq, userID int64, entityType string, entitySyncID *string, operation string, serverVersion int, payloadJSON, deviceID string) error {
	return changelog.AppendChange(tx, seq, userID, entityType, entitySyncID, operation, serverVersion, payloadJSON, deviceID)
}

// lookupOperation §8：同 (user,op_id) 已存在 → 返回其登记信息。
type opRecord struct {
	RequestSha256 string
	ResultCode    string
	ResultVersion int64
	ResultPayload string
}

func lookupOperation(userID int64, opID string) (*opRecord, error) {
	var r opRecord
	err := db().QueryRow(`SELECT request_sha256, result_code, result_version, result_payload
		FROM sync_operations WHERE user_id = ? AND op_id = ?`, userID, opID).
		Scan(&r.RequestSha256, &r.ResultCode, &r.ResultVersion, &r.ResultPayload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// saveOperation 在 op 事务内登记结果（§42 步骤"记录 op_id 结果"与实体变更同提交）。
func saveOperation(tx *sql.Tx, userID int64, opID, reqSha, code string, version int64, payload string) error {
	_, err := tx.Exec(`INSERT INTO sync_operations (user_id, op_id, request_sha256, result_code, result_version, result_payload)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, op_id) DO UPDATE SET result_code = excluded.result_code,
			result_version = excluded.result_version, result_payload = excluded.result_payload`,
		userID, opID, reqSha, code, version, payload)
	return err
}

// minAvailableChangeSeq §7：该用户仍可获得的最早 Change Seq（按 user 计算，非全局 MIN）。
// 无历史 → current+1。
func minAvailableChangeSeq(userID int64) (int64, error) {
	var minSeq sql.NullInt64
	if err := db().QueryRow(`SELECT MIN(change_seq) FROM sync_changes WHERE user_id = ?`, userID).Scan(&minSeq); err != nil {
		return 0, err
	}
	if minSeq.Valid {
		return minSeq.Int64, nil
	}
	cur, err := currentChangeSeq()
	if err != nil {
		return 0, err
	}
	return cur + 1, nil
}
