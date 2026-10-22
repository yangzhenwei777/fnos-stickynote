package sync

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"stickynote/model"
	"stickynote/syncspec"
)

// pull.go / state.go —— GET /api/sync/pull 与 GET /api/sync/state（§32~§40、§7、§37）。
//
// Fixed Watermark 语义（§32/§39）：客户端先 state 取 W=current_change_seq，随后
// pull cursor<seq<=W；服务端把客户端 watermark 钳制到不超过当前值（防御时钟/竞态）。
// 分页默认 200、上限 500（§33），ORDER BY change_seq ASC 定序（§40，禁 created_at）。

const (
	defaultPullLimit = 200
	maxPullLimit     = 500
)

type ChangeDTO = syncspec.ChangeDTO

func Pull(c *gin.Context) {
	userID, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}
	uid := userID.(int64)
	cursor, _ := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
	if cursor < 0 {
		cursor = 0
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(defaultPullLimit)))
	if limit <= 0 {
		limit = defaultPullLimit
	}
	if limit > maxPullLimit {
		limit = maxPullLimit
	}
	cur, err := currentChangeSeq()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "sync state unavailable"})
		return
	}
	watermark, _ := strconv.ParseInt(c.Query("watermark"), 10, 64)
	if watermark <= 0 || watermark > cur {
		watermark = cur // 钳制：以当前值为上界（§39 本轮新增进入下一轮）
	}

	// §32 P1-b：payload_ref/payload_size 随行返回；blob_ref 行 payload_json 恒空串
	// → DTO.Payload = nil（JSON null），内容经 /api/sync/payload/:sha256 下载。
	rows, err := db().Query(`SELECT change_seq, entity_type, entity_sync_id, operation, server_version,
			payload_kind, payload_json, payload_ref, payload_size, device_id, created_at
		FROM sync_changes WHERE user_id = ? AND change_seq > ? AND change_seq <= ?
		ORDER BY change_seq ASC LIMIT ?`, uid, cursor, watermark, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pull failed"})
		return
	}
	defer rows.Close()

	changes := make([]ChangeDTO, 0, 16)
	for rows.Next() {
		var d ChangeDTO
		var payload string
		if err := rows.Scan(&d.ChangeSeq, &d.EntityType, &d.EntitySyncID, &d.Operation,
			&d.ServerVersion, &d.PayloadKind, &payload, &d.PayloadRef, &d.PayloadSize, &d.DeviceID, &d.CreatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "pull scan failed"})
			return
		}
		if d.PayloadKind == "blob_ref" {
			d.Payload = nil // §32：blob_ref 行不携带内联载荷
		} else {
			d.Payload = json.RawMessage(payload)
		}
		changes = append(changes, d)
	}
	next := cursor
	if len(changes) > 0 {
		next = changes[len(changes)-1].ChangeSeq
	}
	minAvail, _ := minAvailableChangeSeq(uid)
	epoch, _ := model.GetServerEpoch()
	c.JSON(http.StatusOK, gin.H{
		"changes":                  changes,
		"next_cursor":              next,
		"watermark":                watermark,
		"has_more":                 next < watermark,
		"server_epoch":             epoch,
		"min_available_change_seq": minAvail,
	})
}

// StatePayload 组装 GET /api/sync/state 响应（§37 全字段；handler 与 sync 包共用）。
func StatePayload(uid int64) (gin.H, error) {
	cur, err := currentChangeSeq()
	if err != nil {
		return nil, err
	}
	minAvail, err := minAvailableChangeSeq(uid)
	if err != nil {
		return nil, err
	}
	epoch, err := model.GetServerEpoch()
	if err != nil {
		return nil, err
	}
	return gin.H{
		"protocol_version":         model.SyncProtocolVersion,
		"server_instance_id":       instanceID(),
		"server_epoch":             epoch,
		"current_change_seq":       cur,
		"min_available_change_seq": minAvail,
	}, nil
}

func State(c *gin.Context) {
	userID, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}
	payload, err := StatePayload(userID.(int64))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sync state unavailable"})
		return
	}
	c.JSON(http.StatusOK, payload)
}
