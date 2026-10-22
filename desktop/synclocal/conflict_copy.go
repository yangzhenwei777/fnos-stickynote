package synclocal

// conflict_copy.go —— 「另存副本」的服务端侧载荷解析（1.0.218）。
//
// 背景（217 内测：另存副本落"我的笔记"而非原文件夹）：原实现由前端直接复用
// CreateNote 绑定（folderID 写死 null），载荷里的 folder_sync_id 被整条丢弃。
// 现改为：**校验与解析下沉到 Go**（单一事实源；前端 conflictCopyCandidate 仅用于
// 按钮显隐，按钮点了之后 Go 仍独立拒绝一次——不信任 UI 的既有纪律）。
//
// 拒绝语义与前端门槛逐条一致（防"点了按钮才报错"与"两边说法不一致"）：
//   - 空载荷 / 非 JSON / 非笔记载荷（无 title/content 字段）/ 加密笔记（载荷内只有密文）
// 一律拒绝，Reason 为可直接上屏的中文。

import (
	"encoding/json"
	"strings"
)

// ConflictCopyPlan 另存副本的可落库计划。OK=false 时 Reason 必非空。
type ConflictCopyPlan struct {
	OK           bool
	Reason       string
	Title        string
	Content      string
	Tags         string
	FolderSyncID string // 空 = 落「我的笔记」（与 CreateNote nil 语义一致）
}

// PlanConflictCopy 解析冲突行的本机载荷（local_payload），产出另存计划。
func PlanConflictCopy(raw string) ConflictCopyPlan {
	if strings.TrimSpace(raw) == "" {
		return ConflictCopyPlan{Reason: "本机内容缺失，无法另存"}
	}
	var p struct {
		Title        *string `json:"title"`
		Content      *string `json:"content"`
		Tags         string  `json:"tags"`
		IsEncrypted  bool    `json:"is_encrypted"`
		FolderSyncID *string `json:"folder_sync_id"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return ConflictCopyPlan{Reason: "本机内容不是笔记载荷，无法另存"}
	}
	if p.Title == nil || p.Content == nil {
		// 文件夹（name）/排序（order）等载荷没有 title+content 对——拒绝并给原因，
		// 绝不"另存出一条空笔记"。
		return ConflictCopyPlan{Reason: "仅笔记支持另存副本（文件夹/排序冲突不适用）"}
	}
	if p.IsEncrypted {
		return ConflictCopyPlan{Reason: "加密笔记请在本机解锁后手动另存（载荷内只有密文）"}
	}
	plan := ConflictCopyPlan{OK: true, Title: *p.Title, Content: *p.Content, Tags: p.Tags}
	if p.FolderSyncID != nil {
		plan.FolderSyncID = *p.FolderSyncID
	}
	return plan
}
