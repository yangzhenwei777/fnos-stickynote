// worker_payload.go —— §32 P1-b（2026-10-15）：桌面端 blob_ref 大载荷消费面。
//
// pull 拿到 payload_kind='blob_ref' 的 change → 流式下载（空闲看门狗 + 边下边算
// sha256）→ 对拍 ref → 流式解析（Decoder 直读文件，避免大载荷整块二次进内存）→
// 以展开后的 JSON 交回统一应用链。
//
// 逃逸纪律（218 "永不收敛"教训）：载荷不可得时本轮 pull 失败退避重试；连续
// payloadMissingEscapeLimit 轮仍缺失则跳过该 change（WARN 留痕，实体由全量重建自愈）
// ——任何等待都必须有界，绝不把 pull 链路卡死在单个 seq 上。
package synclocal

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"stickynote/syncspec"
)

// payloadMissingEscapeLimit 连续缺失逃逸阈值：达到即跳过该 change 推进 cursor。
// 取 5：短暂故障（几轮退避 30min 内）必然覆盖；真缺失时最多损失 5 轮同步时间。
const payloadMissingEscapeLimit = 5

// payloadMissingTracker 记录「因载荷缺失而失败」的 change_seq 连续失败次数。
// 内存态（worker 生命周期）：重启清零无害——重启后再给满额重试机会。
type payloadMissingTracker struct {
	mu     sync.Mutex
	counts map[int64]int
}

func (t *payloadMissingTracker) bump(seq int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = map[int64]int{}
	}
	t.counts[seq]++
	return t.counts[seq]
}

func (t *payloadMissingTracker) clear(seq int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, seq)
}

// notePayloadMissing 判断是否达到逃逸条件；未达到时计入失败并返回 false。
func (w *Worker) notePayloadMissing(seq int64) bool {
	return w.payloadMiss.bump(seq) >= payloadMissingEscapeLimit
}

// fetchBlobPayload 下载 + 校验 + 展开一条 blob_ref 载荷。
// 成功返回展开后的 payload JSON（与内联 payload_json 同形），并清除该 seq 的缺失计数。
func (w *Worker) fetchBlobPayload(ch syncspec.ChangeDTO) ([]byte, error) {
	if ch.PayloadRef == "" {
		return nil, fmt.Errorf("payload ref empty (seq=%d)", ch.ChangeSeq)
	}
	tmp, err := os.CreateTemp("", "snpayload-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName) // 展开读取后即弃（服务端有权威，不长期缓存）

	got, err := w.cl.DownloadPayloadCtx(nil, ch.PayloadRef, tmpName)
	if err != nil {
		return nil, err
	}
	// 截断/篡改件按"载荷不可得"处置（计入逃逸计数——坏件重试无意义；
	// 对齐附件通道"下载校验自愈"语义）。
	if got != ch.PayloadRef {
		return nil, fmt.Errorf("payload sha256 mismatch (want %s got %s): %w", ch.PayloadRef, got, ErrPayloadGone)
	}
	if st, serr := os.Stat(tmpName); serr == nil && ch.PayloadSize > 0 && st.Size() != ch.PayloadSize {
		return nil, fmt.Errorf("payload size mismatch (want %d got %d): %w", ch.PayloadSize, st.Size(), ErrPayloadGone)
	}
	f, err := os.Open(tmpName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(f).Decode(&raw); err != nil {
		return nil, fmt.Errorf("payload decode: %w", err)
	}
	w.payloadMiss.clear(ch.ChangeSeq)
	return raw, nil
}
