package localauth

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stickynote-desktop/localstore"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := localstore.OpenGenDB(filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := EnsureTables(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLoadNoProfile(t *testing.T) {
	db := openTestDB(t)
	if _, err := Load(db); err != ErrNoProfile {
		t.Fatalf("want ErrNoProfile, got %v", err)
	}
}

func TestTouchRevalidateWithoutProfile(t *testing.T) {
	db := openTestDB(t)
	if err := TouchRevalidate(db, time.Now()); err != ErrNoProfile {
		t.Fatalf("want ErrNoProfile, got %v", err)
	}
}

func TestEnsureTablesIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := EnsureTables(db); err != nil {
		t.Fatalf("second EnsureTables: %v", err)
	}
}

func TestImportLegacyDeviceID(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "device_id")

	// 无文件：no-op。
	if did, err := ImportLegacyDeviceID(db, dir); err != nil || did {
		t.Fatalf("no-file: want (false,nil), got (%v,%v)", did, err)
	}
	// 写旧文件后导入。
	if err := os.WriteFile(file, []byte("deadbeefcafe1234\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if did, err := ImportLegacyDeviceID(db, dir); err != nil || !did {
		t.Fatalf("first import: want (true,nil), got (%v,%v)", did, err)
	}
	p, err := Load(db)
	if err != nil || p.DeviceID != "deadbeefcafe1234" {
		t.Fatalf("loaded=%+v err=%v", p, err)
	}
	// 幂等：二次导入跳过；文件原位保留（灾备）。
	if did, err := ImportLegacyDeviceID(db, dir); err != nil || did {
		t.Fatalf("second import must skip: got (%v,%v)", did, err)
	}
	// 已有非空 device_id 时不得被旧文件覆盖。
	if err := os.WriteFile(file, []byte("other1234567890a"), 0600); err != nil {
		t.Fatal(err)
	}
	if did, err := ImportLegacyDeviceID(db, dir); err != nil || did {
		t.Fatalf("must not overwrite existing: got (%v,%v)", did, err)
	}
}

// SignIn 不得冲掉已导入的设备号（跨登录身份稳定的表内半边；封存量在 Windows 测覆盖）。
func TestSignInKeepsImportedDeviceID(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "device_id"), []byte("imported-device-1"), 0600); err != nil {
		t.Fatal(err)
	}
	if did, err := ImportLegacyDeviceID(db, dir); err != nil || !did {
		t.Fatalf("import: %v %v", did, err)
	}
	var id string
	if err := db.QueryRow(`SELECT device_id FROM client_profile WHERE id=1`).Scan(&id); err != nil || id != "imported-device-1" {
		t.Fatalf("precondition: %q %v", id, err)
	}
	// 用直插语义验证 CASE 守卫（不触 DPAPI：SignIn 主体在 windows 测；这里验证 SQL 分支）。
	if _, err := db.Exec(`INSERT INTO client_profile (id, device_id, server_url, username, last_login_at, refresh_sealed, secret_sealed)
		VALUES (1, 'fresh-from-signin', 'http://x/', 'u', '', NULL, NULL)
		ON CONFLICT(id) DO UPDATE SET server_url=excluded.server_url, username=excluded.username,
			last_login_at=excluded.last_login_at, refresh_sealed=excluded.refresh_sealed, secret_sealed=excluded.secret_sealed,
			device_id=CASE WHEN COALESCE(client_profile.device_id,'')='' THEN excluded.device_id ELSE client_profile.device_id END`,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT device_id FROM client_profile WHERE id=1`).Scan(&id); err != nil || id != "imported-device-1" {
		t.Fatalf("SignIn-style upsert must keep imported device_id, got %q (%v)", id, err)
	}
}

func TestDropRefreshSealedKeepsRow(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO client_profile (id, device_id, refresh_sealed, secret_sealed)
		VALUES (1,'d1', X'0102', X'0304')`); err != nil {
		t.Fatal(err)
	}
	if err := DropRefreshSealed(db); err != nil {
		t.Fatal(err)
	}
	p, err := Load(db)
	if err != nil || p.RefreshSealed != nil || p.SecretSealed == nil {
		t.Fatalf("refresh must be cleared while secret kept: %+v %v", p, err)
	}
}
