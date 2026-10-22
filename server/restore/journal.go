package restore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// 写前日志（Write-Ahead Journal, WAJ）——在线全量恢复的崩溃锚点（v1.0.168 切片 3）。
//
// 破坏性动作（换 DB / 换 jwt / 换附件目录 / 采纳新主密钥）之前先把"计划 + 已发生的改名"
// 落盘并 fsync 到位；每完成一个不可逆阶段就推进 State。进程若在任意点崩溃，
// 重启时 Recover 依据 State + 改名表把 .pre-restore 原件移回，达到崩溃可恢复。
//
// 关键纪律：本文件里所有跨平台落盘用 tmp→fsync(file)→rename→fsync(dir)；
// Windows 目录 fsync 为 no-op（见 fsyncdir_*），真正的崩溃一致性以 fnOS/Linux 为准。

// State 是恢复事务的相位标记。
type State string

const (
	StateIntent      State = "INTENT"       // 仅落计划，未做任何破坏性改名
	StateSwapped     State = "SWAPPED"      // 原件已移作 .pre-restore，正式位待写入
	StateExtracted   State = "EXTRACTED"    // 归档已解包写入正式位
	StateMigrated    State = "MIGRATED"     // 新库已重开并跑完迁移（含 restore_generation 落子）
	StateDone        State = "DONE"         // 已重开+auth 复位，等待会话吊销收尾
	StateFinalizing  State = "FINALIZING"   // 会话吊销进行中（幂等由 restore_finalizations 保证）
	StateRollingBack State = "ROLLING_BACK" // 崩溃后重启时进入回滚
)

// RenameOp 记录一次"把 original 移作 preRestore"的可逆操作（file 或 dir）。
type RenameOp struct {
	Kind       string `json:"kind"` // "file" | "dir"
	Original   string `json:"original"`
	PreRestore string `json:"pre_restore"`
	Created    bool   `json:"created"` // true=original 此前不存在（回滚时应删除而非移回）
}

// Journal 是落盘的恢复事务记录。
//
// R0 代际三字段（v20 §85/§86）：INTENT 时作为"计划"落盘；成功路径 new_epoch 在
// commitReady 中持久化到**新库**后才允许推进 DONE。关键不变式：
//
//	State ≥ DONE ⇔ new_epoch 已在新库中持久化（DONE 之前的任何失败 → 回滚 → epoch 不变）
//
// 崩溃恢复据此判定：MIGRATED 及以前一律回滚（新库连同其中的 epoch 一起废弃）；
// DONE/FINALIZING 保留结果、补收尾。legacy 归档无代际（0=omitempty 不落盘）。
type Journal struct {
	RestoreID       string     `json:"restore_id"`
	BackupID        string     `json:"backup_id"`
	ArchiveInstance string     `json:"archive_instance_id"`
	PreRestoreEpoch int64      `json:"pre_restore_epoch,omitempty"`
	RestoredEpoch   int64      `json:"restored_epoch,omitempty"`
	NewEpoch        int64      `json:"new_epoch,omitempty"`
	State           State      `json:"state"`
	Ops             []RenameOp `json:"ops"`
	StageDir        string     `json:"stage_dir"`
	JournalPath     string     `json:"-"` // 内存态，不落盘
}

// journalPath 恢复日志固定名（单实例串行，故唯一）。
func journalPath(dataDir string) string { return filepath.Join(dataDir, ".restore_journal") }

// loadJournal 读取当前日志；不存在返回 (nil, nil)。
func loadJournal(dataDir string) (*Journal, error) {
	b, err := os.ReadFile(journalPath(dataDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read restore journal: %w", err)
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("restore journal corrupt: %w", err)
	}
	j.JournalPath = journalPath(dataDir)
	return &j, nil
}

// write 原子落盘并推进 State（fsync 到位）。
func (j *Journal) write() error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := durableWrite(j.JournalPath, b); err != nil {
		return err
	}
	return nil
}

func clearJournal(dataDir string) error {
	if err := os.Remove(journalPath(dataDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
