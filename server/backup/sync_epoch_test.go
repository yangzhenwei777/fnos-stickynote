package backup

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ensureSyncMetaSeed 在 seedInstance 的最小库上补 sync_meta（模拟已跑 R0 迁移的实例）。
func ensureSyncMetaSeed(t *testing.T, dataDir string, epoch int64) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "notepad.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS sync_meta (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		server_epoch INTEGER NOT NULL DEFAULT 1,
		current_change_seq INTEGER NOT NULL DEFAULT 0,
		protocol_version INTEGER NOT NULL DEFAULT 1,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	mustExec(t, db, `DELETE FROM sync_meta`)
	mustExec(t, db, fmt.Sprintf(`INSERT INTO sync_meta (id, server_epoch) VALUES (1, %d)`, epoch))
}

// openManifestFromArchive 解档取内部 manifest（测试捷径，复用包内原语）。
func openManifestFromArchive(t *testing.T, dataDir string, res *Result) (*InternalManifest, string) {
	t.Helper()
	envBytes, err := os.ReadFile(res.SnbakPath)
	if err != nil {
		t.Fatal(err)
	}
	master, err := ReadMasterKey(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := EnvelopeOpen(envBytes, master, 0)
	if err != nil {
		t.Fatal(err)
	}
	manBytes := tarFile(t, payload, "manifest.json")
	man, err := ParseInternalManifest(manBytes)
	if err != nil {
		t.Fatal(err)
	}
	return man, string(manBytes)
}

// TestCreateBackupManifestEpochFromSnapshot R0（v20 §75）：归档声明的 server_epoch
// 必须来自冻结快照库（与包内 db 同一时间点），而不是事后读 live。
func TestCreateBackupManifestEpochFromSnapshot(t *testing.T) {
	dataDir, uploadDir := seedInstance(t, true)
	ensureSyncMetaSeed(t, dataDir, 7)

	env := Env{DataDir: dataDir, UploadDir: uploadDir, AppVersion: "1.0.175", SchemaVer: 2}
	res, err := CreateBackup(env, "aaaaaaaa-0000-0000-0000-000000000011", false)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	man, _ := openManifestFromArchive(t, dataDir, res)
	if man.ServerEpoch != 7 {
		t.Fatalf("manifest server_epoch=%d want 7（快照同源）", man.ServerEpoch)
	}
}

// TestCreateBackupLegacySnapshotNoEpoch（反向兼容）：无 sync_meta 的 legacy 库快照
// → manifest 代际为 0 且 JSON 中不出现 server_epoch 键（omitempty 线上形态），
// 恢复侧按 max(pre,0)+1 单调推进。同时锁定"老格式清单可被新代码解析"。
func TestCreateBackupLegacySnapshotNoEpoch(t *testing.T) {
	dataDir, uploadDir := seedInstance(t, true) // seedInstance 本就不建 sync_meta = legacy 形态

	env := Env{DataDir: dataDir, UploadDir: uploadDir, AppVersion: "1.0.175", SchemaVer: 2}
	res, err := CreateBackup(env, "aaaaaaaa-0000-0000-0000-000000000012", false)
	if err != nil {
		t.Fatalf("CreateBackup legacy 快照必须不受影响: %v", err)
	}
	man, raw := openManifestFromArchive(t, dataDir, res)
	if man.ServerEpoch != 0 {
		t.Fatalf("legacy 归档代际应为 0，得 %d", man.ServerEpoch)
	}
	if strings.Contains(raw, `"server_epoch"`) {
		t.Fatal("legacy 归档 manifest JSON 不应出现 server_epoch 键（omitempty 形态锁定）")
	}
}
