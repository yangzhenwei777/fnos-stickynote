package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stickynote/database"
	"stickynote/model"
)

// freshInstance 用真实 schema 初始化一个全新实例（会重指全局 database.DB），
// 灌 1 用户 / 1 笔记 + .jwt_secret + 主密钥/instance。调用方负责后续 Close。
func freshInstance(t *testing.T) (dataDir, uploadDir string) {
	t.Helper()
	dataDir = t.TempDir()
	uploadDir = filepath.Join(dataDir, "upload")
	if err := database.Init(filepath.Join(dataDir, "notepad.db"), "1.0.168"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	if _, err := database.DB.Exec(`INSERT INTO users(username,password_hash,role) VALUES('admin','x','admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO notes(user_id,title,content) VALUES(1,'t','hello')`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".jwt_secret"), []byte("s3cr3t\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateInstanceID(dataDir); err != nil {
		t.Fatal(err)
	}
	return dataDir, uploadDir
}

// TestRemoteDiscoveryTakeover 复现"清空/重装后接管历史备份"：
// 实例 A 备份到共享 WebDAV → 实例 B（全新库）导入 A 的主密钥 → DiscoverRemote 登记历史快照
// → 列表可见 + 下载字节与归档 sha 一致 + VerifyRemote 解密校验通过。
func TestRemoteDiscoveryTakeover(t *testing.T) {
	srv := newFakeWebDAV(t)
	base := strings.TrimRight(srv.URL, "/") + "/dav/"

	// ---- 实例 A：备份一份到 WebDAV ----
	dirA, upA := freshInstance(t)
	mA := NewManager(dirA, upA, "1.0.168", 1)
	tidA := makeTarget(t, mA, base, true)
	jobA, bid, err := mA.BeginJob(tidA, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := mA.RunBackupJob(tidA, "manual", jobA, bid); err != nil {
		t.Fatalf("A backup: %v", err)
	}
	bundle, err := mA.ExportKey()
	if err != nil {
		t.Fatalf("A export key: %v", err)
	}
	// 记下归档真实 sha（来自 A 的快照账本）
	var wantSha string
	for _, s := range snapsOf(t) {
		if s.BackupID == bid {
			wantSha = s.ArchiveSha256
		}
	}
	if wantSha == "" {
		t.Fatal("A 快照账本未含该备份")
	}

	// ---- 实例 B：全新库 + 导入 A 主密钥 + 发现远端 ----
	database.DB.Close()
	database.DB = nil
	dirB, upB := freshInstance(t)
	t.Cleanup(func() {
		if database.DB != nil {
			database.DB.Close()
			database.DB = nil
		}
	})
	mB := NewManager(dirB, upB, "1.0.168", 1)
	// 真实用户顺序：先建目标（用 B 当时主密钥加密凭据）→ 再导入 A 主密钥 → 发现。
	// 换钥必须重包目标凭据，否则 clientFor→DecryptSecret 会认证失败（真机抓到的 bug）。
	tidB := makeTarget(t, mB, base, true)
	if _, err := mB.ImportKey(bundle); err != nil {
		t.Fatalf("B import key: %v", err)
	}
	// B 全新库：发现前应无任何快照
	if snaps := snapsOf(t); len(snaps) != 0 {
		t.Fatalf("B 发现前应为空，得 %d", len(snaps))
	}
	added, err := mB.DiscoverRemote(tidB)
	if err != nil {
		t.Fatalf("DiscoverRemote: %v", err)
	}
	if added < 1 {
		t.Fatalf("发现数=%d want >=1", added)
	}
	// 列表可见
	var got *model.BackupSnapshot
	for _, s := range snapsOf(t) {
		if s.BackupID == bid {
			got = &s
		}
	}
	if got == nil {
		t.Fatal("B 发现后列表未含历史快照")
	}
	if got.Status != "DISCOVERED" {
		t.Fatalf("发现快照状态=%s want DISCOVERED", got.Status)
	}
	// placement 已 VERIFIED → 下载可用
	if placementStatus(t, bid, tidB) != "VERIFIED" {
		t.Fatal("发现后 placement 应 VERIFIED")
	}
	data, _, err := mB.DownloadArchive(bid)
	if err != nil {
		t.Fatalf("B 下载历史归档失败: %v", err)
	}
	if got2 := sha256Hex(data); got2 != wantSha {
		t.Fatalf("下载归档 sha 不符: %s != %s", got2[:12], wantSha[:12])
	}
	// 校验（需导入的主密钥解密）通过
	if err := mB.VerifyRemote(bid); err != nil {
		t.Fatalf("B VerifyRemote: %v", err)
	}
}

func snapsOf(t *testing.T) []model.BackupSnapshot {
	t.Helper()
	ss, err := model.ListBackupSnapshots(50)
	if err != nil {
		t.Fatalf("ListBackupSnapshots: %v", err)
	}
	return ss
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
