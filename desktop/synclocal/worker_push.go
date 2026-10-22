package synclocal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"stickynote/syncspec"
)

// worker_push.go —— Worker 的 **push 侧**（v21.166 计划项3 · 纯拆分）。
//
// 本文件自 worker.go 按职责切分而来，**零逻辑改动**（纯搬家）。
// 切分依据：worker 原文件同时承载「轮次驱动 + pull 应用 + push 排空」三件事，
// 文件超千行；push 侧（取样/合并/推送结果应用/重试对账）自成闭环，
// 独立成文件后 205 类跨切面问题（bootstrap 与 worker 共享资源）更易定位。

type batch struct {
	ops  []syncspec.PushOp
	ids  []int64
	byOp map[string]int64
}

// buildBatch PENDING → 每实体合并取最终 op（payload/operation 最新，base 取该实体最早未同步基点，§49）；
// SENDING/CONFLICT 实体不参与（§26/§41）。被合并的中间行标 ACKED(merged) 留审计。
//
// ⭐ 1.0.218（217 内测实锤）：**链头优先**——链头是 CREATE 且链尾非 DELETE/PURGE 时，不合并、
// 本批只推链头 CREATE（链尾留 PENDING 下轮续推）。定罪与收敛证明见函数内联注释。
func (w *Worker) buildBatch() (batch, error) {
	var b batch
	b.byOp = map[string]int64{}
	rows, err := w.db.Query(`SELECT seq, op_id, entity_type, entity_sync_id, operation, base_version, payload
		FROM sync_outbox WHERE status='PENDING' ORDER BY seq ASC LIMIT 200`)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	type agg struct {
		firstBase int64
		first     syncspec.PushOp
		firstSeq  int64
		last      syncspec.PushOp
		lastSeq   int64
		seqs      []int64
	}
	order := []string{}
	aggs := map[string]*agg{}
	for rows.Next() {
		var (
			seq, base sql.NullInt64
			op        syncspec.PushOp
			payload   string
		)
		if err := rows.Scan(&seq, &op.OpID, &op.EntityType, &op.EntitySyncID, &op.Operation, &base, &payload); err != nil {
			return b, err
		}
		op.Payload = json.RawMessage(payload)
		key := op.EntityType + "|" + op.EntitySyncID
		a, ok := aggs[key]
		if !ok {
			a = &agg{firstBase: base.Int64, first: op, firstSeq: seq.Int64}
			aggs[key] = a
			order = append(order, key)
		}
		a.last, a.lastSeq = op, seq.Int64
		a.seqs = append(a.seqs, seq.Int64)
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	// E4③（2026-09-23 真机账本 seq11 实锤）：NOTE/FOLDER_ORDER 引用「仍在同步链上的
	// 文件夹」（有未收账 outbox 行或 open 冲突）时，本轮必须缓推——否则服务端以
	// unknown folder_sync_id 拒收（BATCH_PROTOCOL_ERROR），依赖 op 空耗重试直至冲突人工面。
	// folder 就绪后（处置完成/ACK），被缓推的 op 留在 PENDING 自动续推，零人工介入。
	readyCache := map[string]bool{}
	folderReady := func(sid string) bool {
		if sid == "" {
			return true
		}
		if v, ok := readyCache[sid]; ok {
			return v
		}
		ok := false
		var n int
		_ = w.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox
			WHERE entity_sync_id = ? AND status IN ('PENDING','SENDING','FAILED')`, sid).Scan(&n)
		if n == 0 {
			_ = w.db.QueryRow(`SELECT COUNT(*) FROM sync_conflicts
				WHERE entity_sync_id = ? AND status='open'`, sid).Scan(&n)
			ok = n == 0
		}
		readyCache[sid] = ok
		return ok
	}
	dependsOnPendingFolder := func(etype, payload string) bool {
		switch etype {
		case "NOTE":
			var p struct {
				FolderSyncID *string `json:"folder_sync_id"`
			}
			if json.Unmarshal([]byte(payload), &p) == nil && p.FolderSyncID != nil {
				return !folderReady(*p.FolderSyncID)
			}
		case "FOLDER_ORDER":
			var p struct {
				Bases map[string]int64 `json:"bases"`
			}
			if json.Unmarshal([]byte(payload), &p) == nil {
				for sid := range p.Bases {
					if !folderReady(sid) {
						return true
					}
				}
			}
		}
		return false
	}
	for _, key := range order {
		if len(b.ops) >= syncspec.MaxOpsPerBatch {
			break
		}
		a := aggs[key]
		// ⭐ 1.0.218（217 内测实锤·离线新建链）：「链头优先」——链头是 CREATE 且链尾不是
		// DELETE/PURGE 时**不合并**，本批只推链头 CREATE（op_id/payload/base 保持原生身份）。
		//
		// 定罪（探针实测）：§49 合并「取链尾 op」把 [CREATE, UPDATE] 合成单条 UPDATE 推出去，
		// 而服务端 opCasUpdate 对不存在的实体回 ENTITY_GONE（协议的自愈道是 CREATE 重发：
		// sync_id 不存在→INSERT、已存在→IDEMPOTENT_REPLAY）⇒ 离线「新建/导入 + 任意后续编辑」
		// 的笔记永久卡成「远端该实体已被删除」冲突，永不收敛（用户截图三要素逐项吻合）。
		//
		// 收敛证明：链头 CREATE 先推（ACK 后 §50 把链尾 base 重定基）→ 下一轮链尾合并续推
		// UPDATE → CAS 必中。两轮内确定性收敛；CREATE 幂等保证「ACK 丢失重推」同样收敛。
		//
		// 例外：链尾是 DELETE/PURGE 时保持既有合并语义（v21.221：目标态"不存在"已达成即清账，
		// 「先建后删」不得在服务端复活一条笔记）。
		if a.first.Operation == "CREATE" && a.last.Operation != "DELETE" && a.last.Operation != "PURGE" {
			head := a.first
			if dependsOnPendingFolder(head.EntityType, string(head.Payload)) {
				continue // 缓推：父夹就绪后下一轮再推（行保持 PENDING）
			}
			bv := a.firstBase
			head.BaseVersion = &bv
			b.ops = append(b.ops, head)
			b.ids = append(b.ids, a.firstSeq)
			b.byOp[head.OpID] = a.firstSeq
			continue // 链尾行原样留 PENDING（不标 merged），下一轮自动合并续推
		}
		if dependsOnPendingFolder(a.last.EntityType, string(a.last.Payload)) {
			continue // 缓推：父夹就绪后下一轮自动入批（行保持 PENDING）
		}
		bv := a.firstBase
		kept := a.last
		kept.BaseVersion = &bv
		b.ops = append(b.ops, kept)
		b.ids = append(b.ids, a.lastSeq)
		b.byOp[kept.OpID] = a.lastSeq
		for _, sq := range a.seqs {
			if sq == a.lastSeq {
				continue
			}
			if _, err := w.db.Exec(`UPDATE sync_outbox SET status='ACKED', last_error='merged',
				updated_at=CURRENT_TIMESTAMP WHERE seq = ?`, sq); err != nil {
				return b, err
			}
		}
	}
	for _, id := range b.ids {
		if _, err := w.db.Exec(`UPDATE sync_outbox SET status='SENDING', updated_at=CURRENT_TIMESTAMP WHERE seq = ?`, id); err != nil {
			return b, err
		}
	}
	return b, nil
}

// rebaseOrderPayloads FOLDER_ORDER ACK 后，把队列中遗留 PENDING order op 的 payload.bases
// 重写为本地权威版本（否则后续 order op 必带陈旧 base 撞 409）。
func (w *Worker) rebaseOrderPayloads() error {
	rows, err := w.db.Query(`SELECT seq, payload FROM sync_outbox
		WHERE entity_type='FOLDER_ORDER' AND status='PENDING' ORDER BY seq`)
	if err != nil {
		return err
	}
	type item struct {
		seq int64
		pl  syncspec.FolderOrderPayload
	}
	var items []item
	for rows.Next() {
		var it item
		var raw string
		if err := rows.Scan(&it.seq, &raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(raw), &it.pl); err != nil {
			continue // 坏 payload 交由服务端 BATCH_PROTOCOL 处理，不在本地吞改
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, it := range items {
		changed := false
		for sid := range it.pl.Bases {
			var v int64
			if err := w.db.QueryRow(`SELECT version FROM note_folders WHERE sync_id = ?`, sid).Scan(&v); err == nil && v != it.pl.Bases[sid] {
				it.pl.Bases[sid] = v
				changed = true
			}
		}
		if changed {
			b, _ := json.Marshal(it.pl)
			// E4②：payload 改写必须同步换 op_id（不变量：op 请求身份 = 请求内容，
			// opRequestHash 含 payload；同 op_id 异 payload 重推 = REUSE → STALE）。
			if _, err := w.db.Exec(`UPDATE sync_outbox SET op_id = ?, payload = ?, updated_at=CURRENT_TIMESTAMP WHERE seq = ?`,
				randID(), string(b), it.seq); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *Worker) unmarkSending(ids []int64) {
	for _, id := range ids {
		_, _ = w.db.Exec(`UPDATE sync_outbox SET status='PENDING', updated_at=CURRENT_TIMESTAMP WHERE seq = ? AND status='SENDING'`, id)
	}
}

// applyResult §50/§51/ADR-004：ACK→回写版本+rebase 后续；CONFLICT/GONE→冲突记录+链转 CONFLICT；
// 其他失败→FAILED（下轮重试）。
func (w *Worker) applyResult(r syncspec.OpResult, b batch) (bool, error) {
	seq, ok := b.byOp[r.OpID]
	if !ok {
		return false, nil
	}
	var entity string
	if err := w.db.QueryRow(`SELECT entity_sync_id FROM sync_outbox WHERE seq = ?`, seq).Scan(&entity); err != nil {
		return false, err
	}
	switch r.Code {
	case syncspec.CodeAcked, syncspec.CodeIdempotentReplay:
		var etype string
		if err := w.db.QueryRow(`SELECT entity_type FROM sync_outbox WHERE seq = ?`, seq).Scan(&etype); err != nil {
			return false, err
		}
		if _, err := w.db.Exec(`UPDATE sync_outbox SET status='ACKED', updated_at=CURRENT_TIMESTAMP WHERE seq = ?`, seq); err != nil {
			return false, err
		}
		if r.ServerVersion > 0 {
			if etype == syncspec.EntityTypeFolder {
				// FOLDER ACK：回写权威版本 + 后续 PENDING folder op 重定基（§50 同构）。
				if _, err := w.db.Exec(`UPDATE note_folders SET version = ? WHERE sync_id = ? AND version < ?`,
					r.ServerVersion, entity, r.ServerVersion); err != nil {
					return false, err
				}
			} else {
				if _, err := w.db.Exec(`UPDATE notes SET version = ? WHERE sync_id = ? AND version < ?`,
					r.ServerVersion, entity, r.ServerVersion); err != nil {
					return false, err
				}
			}
			// §50：后续 PENDING 以最新服务端版本为 CAS 基点。
			// E4②：base 是 opRequestHash（sha256(整 PushOp)）的组成部分——base 改写必须
			// 同步换新 op_id，否则同 op_id 异内容重推必被服务端幂等防护判 REUSE →
			// STALE → 人工面（真机账本 seq8 实锤）。randomblob 逐行随机，不撞 UNIQUE。
			if _, err := w.db.Exec(`UPDATE sync_outbox
				SET op_id = lower(hex(randomblob(12))), base_version = ?
				WHERE entity_sync_id = ? AND status='PENDING'`,
				r.ServerVersion, entity); err != nil {
				return false, err
			}
		}
		// FOLDER_ORDER ACK：逐夹权威版本回写 + 队列中 folder op 基点重定基（§50 集合面对应物）。
		for _, vr := range r.OrderVersions {
			if _, err := w.db.Exec(`UPDATE note_folders SET version = ? WHERE sync_id = ? AND version < ?`,
				vr.Version, vr.SyncID, vr.Version); err != nil {
				return false, err
			}
			// E4②：同上——base 改写必须同步换 op_id（§50 集合面对应物）。
			if _, err := w.db.Exec(`UPDATE sync_outbox
				SET op_id = lower(hex(randomblob(12))), base_version = ?
				WHERE entity_sync_id = ? AND entity_type = 'FOLDER' AND status='PENDING'`,
				vr.Version, vr.SyncID); err != nil {
				return false, err
			}
		}
		if len(r.OrderVersions) > 0 {
			if err := w.rebaseOrderPayloads(); err != nil {
				return false, err
			}
		}
		return true, nil
	// ⭐⭐ v21.221（214 实机定罪）：**DELETE/PURGE 遇到「远端实体已不存在」= 幂等成功**，
	// 必须直接消解 outbox，**不得**转冲突。
	//
	// 214 实机坐实：g-000002 的 sync_outbox 里积压 **10 条全是 DELETE/PURGE**，
	// 对应的笔记用户早已在 Web 端删除（服务端自然也没有了）。
	// 服务端按幂等语义回 `CodeEntityGone`（"目标态'不存在'已达成"），
	// 客户端却把它当 `PUSH_CONFLICT` 塞进 sync_conflicts 并要求用户手动处置
	// ⇒ 10 条僵尸操作**永久**躺在 outbox 里，每轮都重试、每轮都失败。
	//
	// 语义澄清（服务端 push.go:339 已写明）：
	//「行不存在（已删过/幂等重放）→ IDEMPOTENT_REPLAY（**目标态"不存在"已达成**）」
	// 既然目标态已达成，客户端再留着这条删除操作纯属浪费，
	// 而留着 = 阻塞 outbox = 同步不推进 = 用户看到的"笔记一直是空的/一直不同步"。
	//
	// 处置：DELETE/PURGE + ENTITY_GONE ⇒ 从 outbox 删除该行（目标已达成，账清了）。
	// 非删除类操作（UPDATE/CREATE）的 ENTITY_GONE 仍需用户决策（本地改动无处可去），
	// 保持原冲突流程不变。
	case syncspec.CodeEntityGone:
		var oper string
		_ = w.db.QueryRow(`SELECT operation FROM sync_outbox WHERE seq = ?`, seq).Scan(&oper)
		if oper == "DELETE" || oper == "PURGE" {
			// 目标态（实体不存在）已达成 ⇒ 幂等成功，出账。
			if _, err := w.db.Exec(`DELETE FROM sync_outbox WHERE seq = ?`, seq); err != nil {
				return false, err
			}
			log.Printf("[sync] outbox 消解：%s 操作的目标实体服务端已不存在（幂等删除已达成），移除该条待推操作 seq=%d", oper, seq)
			return true, nil
		}
		// 非删除类：本地改动无处安放，仍需用户决策（另存副本 / 以本机为准重投 / 放弃）
		fallthrough
	case syncspec.CodeConflict, syncspec.CodeOpIDReuseError, syncspec.CodeIdentityConflict:
		kind := "PUSH_CONFLICT"
		switch r.Code {
		case syncspec.CodeEntityGone:
			kind = "ENTITY_GONE"
		case syncspec.CodeOpIDReuseError, syncspec.CodeIdentityConflict:
			kind = "STALE"
		}
		var localP, etype, oper string
		_ = w.db.QueryRow(`SELECT payload, entity_type, operation FROM sync_outbox WHERE seq = ?`, seq).Scan(&localP, &etype, &oper)
		tx, err := w.db.Begin()
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		if err := addConflict(tx, entity, etype, oper, kind, localP, "", 0, r.RemoteVersion); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`UPDATE sync_outbox SET status='CONFLICT', last_error=?, updated_at=CURRENT_TIMESTAMP
			WHERE seq = ? OR (entity_sync_id = ? AND status IN ('PENDING','FAILED'))`, r.Code, seq, entity); err != nil {
			return false, err
		}
		return false, tx.Commit()
	case syncspec.CodeAttachmentNotFound:
		// ADR-004 R3 自恢复：涉事文件账本重置 LOCAL（下轮预传自动重传）+ op FAILED
		// （reconcileOutboxRetries 下一轮升回 PENDING 原样重试；≥3 转 CONFLICT 人工面）。
		var payload struct {
			Missing []string `json:"missing"`
		}
		_ = json.Unmarshal([]byte(r.Error), &payload)
		if len(payload.Missing) > 0 {
			if err := ResetAttachmentsForReupload(w.db, payload.Missing); err != nil {
				return false, err
			}
		}
		_, err := w.db.Exec(`UPDATE sync_outbox SET status='FAILED', retry_count=retry_count+1, last_error=?,
			updated_at=CURRENT_TIMESTAMP WHERE seq = ?`, r.Code, seq)
		return false, err
	default: // INTERNAL / UNSUPPORTED / BATCH 拒绝等：本轮 FAILED，下轮再试
		_, err := w.db.Exec(`UPDATE sync_outbox SET status='FAILED', retry_count=retry_count+1, last_error=?,
			updated_at=CURRENT_TIMESTAMP WHERE seq = ?`, r.Code+":"+r.Error, seq)
		return false, err
	}
}

// reconcileOutboxRetries（P1 批次3，R3 上限承接）：FAILED 未达 3 次 → 升回 PENDING 重试；
// 达上限 → CONFLICT（人工面，P4 冲突中心处理）。CONFLICT/SENDING 链不动。
// 批次8-Alt 刀1：转 CONFLICT 的同时补记冲突资产（kind=RETRY_EXHAUSTED）——此前这批 op
// 会静默变成"既不在待推计数里、也没有任何冲突记录"的黑洞，用户无从得知。
func (w *Worker) reconcileOutboxRetries() error {
	if _, err := w.db.Exec(`UPDATE sync_outbox SET status='PENDING', updated_at=CURRENT_TIMESTAMP
		WHERE status='FAILED' AND retry_count < 3`); err != nil {
		return err
	}
	rows, err := w.db.Query(`SELECT seq FROM sync_outbox WHERE status='FAILED' AND retry_count >= 3`)
	if err != nil {
		return err
	}
	var exhausted []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err == nil {
			exhausted = append(exhausted, seq)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(exhausted) == 0 {
		return nil
	}
	tx, err := w.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, seq := range exhausted {
		if _, err := tx.Exec(`UPDATE sync_outbox SET status='CONFLICT', updated_at=CURRENT_TIMESTAMP
			WHERE seq = ?`, seq); err != nil {
			return err
		}
		if err := recordConflictFromOp(tx, seq, KindRetryExhausted); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ensureRefUploads 发送前确保批次内 NOTE op 引用的附件已到达服务端（账本驱动，ADR-003 R2/R5）：
//   - 账本 UPLOADED 且 sha 一致 → 跳过；
//   - 本地文件缺失或读失败 → 该 op 转 FAILED（下轮再试，文件可能被同步下载补回）；
//   - 上传成功 → 账本 UPLOADED；
//   - UPLOAD_HASH_MISMATCH / UPLOAD_IDENTITY_CONFLICT → 内容分歧，该实体链转 CONFLICT
//     从本批剔除（重传无法自愈，人工面 P4）；其他错误（网络/认证）上抛中止本轮。
func (w *Worker) ensureRefUploads(b *batch) error {
	if w.cl == nil {
		return nil // 无客户端（纯离线测试工装）：附件通道静默关闭。
	}
	keep := make([]syncspec.PushOp, 0, len(b.ops))
	for i, op := range b.ops {
		if op.EntityType != "NOTE" || (op.Operation != "CREATE" && op.Operation != "UPDATE") {
			keep = append(keep, op)
			continue
		}
		var p payloadIn
		if err := json.Unmarshal(op.Payload, &p); err != nil {
			keep = append(keep, op)
			continue
		}
		conflict := false
		var upErr error
		for _, r := range p.Refs {
			if r.Filename == "" {
				continue
			}
			sha := r.Sha256
			if attachmentUploaded(w.db, r.Filename, sha) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(w.AttachDir, filepath.Base(r.Filename)))
			if err != nil {
				upErr = fmt.Errorf("local attachment missing: %s", r.Filename)
				break
			}
			if sha == "" {
				h := sha256.Sum256(data)
				sha = hex.EncodeToString(h[:])
			}
			if uerr := w.cl.UploadAttachment(r.Filename, sha, data); uerr != nil {
				var sue *SyncUploadError
				if errors.As(uerr, &sue) && (sue.Code == syncspec.CodeUploadHashMismatch || sue.Code == syncspec.CodeUploadIdentityConflict) {
					conflict = true
					upErr = uerr
					break
				}
				return uerr // 网络/认证类：中止本轮（SENDING 由调用方回拨）
			}
			if err := markAttachmentUploaded(w.db, r.Filename, sha, int64(len(data))); err != nil {
				return err
			}
		}
		if conflict {
			if _, err := w.db.Exec(`UPDATE sync_outbox SET status='CONFLICT', last_error=?, updated_at=CURRENT_TIMESTAMP
				WHERE seq = ?`, "upload-conflict", b.ids[i]); err != nil {
				return err
			}
			if err := recordConflictFromOp(w.db, b.ids[i], KindAttachmentDiverged); err != nil {
				return err
			}
			delete(b.byOp, op.OpID)
			continue
		}
		if upErr != nil { // 本地缺件：本轮不发送该 op，FAILED 等下轮（可能被同步补回）
			if _, err := w.db.Exec(`UPDATE sync_outbox SET status='FAILED', retry_count=retry_count+1, last_error=?,
				updated_at=CURRENT_TIMESTAMP WHERE seq = ?`, upErr.Error(), b.ids[i]); err != nil {
				return err
			}
			delete(b.byOp, op.OpID)
			continue
		}
		keep = append(keep, op)
	}
	b.ops = keep
	return nil
}
