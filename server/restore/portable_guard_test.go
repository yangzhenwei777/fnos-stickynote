package restore

import (
	"database/sql"
	"encoding/json"
	_ "modernc.org/sqlite"
	"path/filepath"
	"strings"
	"testing"

	"stickynote/backup"
)

// TestReadMemberManifestRejectsPortable 反向验证（门禁纪律：注入假失败必被阻断）：
// 手工产一个 BackupType=PORTABLE 的合法信封归档（服务端归档永远不写该类型），
// 预检读取成员 manifest 必须拒绝——便携包无 .jwt_secret 且是全凭据脱敏单用户态，
// 一旦被当成整站灾备恢复 = 红线 14/17 级事故。
func TestReadMemberManifestRejectsPortable(t *testing.T) {
	staging := t.TempDir()
	dbPath := filepath.Join(staging, "notepad.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	bid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	man := backup.InternalManifest{FormatVersion: 1, BackupID: bid,
		BackupType: backup.BackupTypePortable, InstanceID: "inst-X"}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := backup.BuildArchive(backup.ArchiveInput{ManifestBytes: mb, DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.EnvelopeSeal(payload, bid, make([]byte, 32), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := readMemberManifest(payload, backup.Env{}); err == nil ||
		!strings.Contains(err.Error(), "便携备份") {
		t.Fatalf("PORTABLE archive must be refused by restore preflight, got %v", err)
	}

	// 控制组：同结构但 BackupType 空（正常服务端形态）必须放行到后续校验（不误伤）。
	man.BackupType = ""
	mb, _ = json.MarshalIndent(man, "", "  ")
	payload, err = backup.BuildArchive(backup.ArchiveInput{ManifestBytes: mb, DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readMemberManifest(payload, backup.Env{}); err != nil || got.BackupType != "" {
		t.Fatalf("server-type manifest must pass the guard: %v", err)
	}
}
