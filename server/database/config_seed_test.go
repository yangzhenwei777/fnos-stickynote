package database

import (
	"path/filepath"
	"regexp"
	"testing"
)

func setupConfigSeedDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.db")
	if err := Init(path, "99.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() {
		if DB != nil {
			_ = DB.Close()
			DB = nil
		}
	})
	return path
}

func configValueForTest(t *testing.T, key string) string {
	t.Helper()
	var value string
	if err := DB.QueryRow("SELECT value FROM configs WHERE key = ?", key).Scan(&value); err != nil {
		t.Fatalf("read config %s: %v", key, err)
	}
	return value
}

func TestEnsureHistoryConfigsDefaults(t *testing.T) {
	t.Setenv("HISTORY_MAX_PER_NOTE", "")
	t.Setenv("HISTORY_RETENTION_DAYS", "")
	t.Setenv("HISTORY_MAX_TOTAL_MB", "")
	setupConfigSeedDB(t)

	for key, want := range map[string]string{
		"history_max_per_note":   "50",
		"history_retention_days": "180",
		"history_max_total_mb":   "2048",
	} {
		if got := configValueForTest(t, key); got != want {
			t.Errorf("%s=%q want %q", key, got, want)
		}
	}
}

func TestEnsureHistoryConfigsEnvSeed(t *testing.T) {
	t.Setenv("HISTORY_MAX_PER_NOTE", "20")
	t.Setenv("HISTORY_RETENTION_DAYS", "90")
	t.Setenv("HISTORY_MAX_TOTAL_MB", "512")
	setupConfigSeedDB(t)

	for key, want := range map[string]string{
		"history_max_per_note":   "20",
		"history_retention_days": "90",
		"history_max_total_mb":   "512",
	} {
		if got := configValueForTest(t, key); got != want {
			t.Errorf("%s=%q want %q", key, got, want)
		}
	}
}

func TestHistoryConfigEnvDoesNotOverrideExistingValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	if err := Init(path, "99.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	if _, err := DB.Exec("UPDATE configs SET value = '7' WHERE key = 'history_max_per_note'"); err != nil {
		t.Fatal(err)
	}
	if err := DB.Close(); err != nil {
		t.Fatal(err)
	}
	DB = nil

	t.Setenv("HISTORY_MAX_PER_NOTE", "20")
	if err := Init(path, "99.0.0"); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	t.Cleanup(func() {
		if DB != nil {
			_ = DB.Close()
			DB = nil
		}
	})
	if got := configValueForTest(t, "history_max_per_note"); got != "7" {
		t.Fatalf("existing value should be preserved, got %q", got)
	}
}

func TestMigrateLegacyConfigs(t *testing.T) {
	setupConfigSeedDB(t)

	if _, err := DB.Exec("INSERT OR REPLACE INTO configs (key, value, description) VALUES ('orphan_snapshots_purged_v170', 'true', 'old v21.130')"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec("INSERT OR REPLACE INTO configs (key, value, description) VALUES ('allow_external_images', 'true', 'old')"); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyConfigs(); err != nil {
		t.Fatal(err)
	}
	if got := configValueForTest(t, "orphan_snapshots_purged"); got != "true" {
		t.Fatalf("renamed flag value=%q want true", got)
	}
	var oldCount int
	if err := DB.QueryRow("SELECT COUNT(*) FROM configs WHERE key = 'orphan_snapshots_purged_v170'").Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatal("legacy orphan flag key should be renamed")
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM configs WHERE key = 'allow_external_images'").Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatal("legacy allow_external_images should be removed")
	}
}

func TestRefreshConfigDescriptions(t *testing.T) {
	setupConfigSeedDB(t)

	if _, err := DB.Exec("UPDATE configs SET description = '回收站自动清理天数（0=永久保留，P2-6）' WHERE key = 'trash_retention_days'"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec("UPDATE configs SET description = '允许创建分享链接（公开只读；2.6.0）' WHERE key = 'share_enabled'"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec("INSERT OR REPLACE INTO configs (key, value, description) VALUES ('uploads_backfilled', 'partial', 'old v18.5')"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec("INSERT OR REPLACE INTO configs (key, value, description) VALUES ('orphan_snapshots_purged', 'true', 'old v21.130')"); err != nil {
		t.Fatal(err)
	}
	if err := refreshConfigDescriptions(); err != nil {
		t.Fatal(err)
	}

	checks := map[string]string{
		"trash_retention_days":    ConfigDescription("trash_retention_days"),
		"share_enabled":           ConfigDescription("share_enabled"),
		"uploads_backfilled":      uploadsBackfilledPartialDesc,
		"orphan_snapshots_purged": ConfigDescription("orphan_snapshots_purged"),
	}
	for key, want := range checks {
		var got string
		if err := DB.QueryRow("SELECT description FROM configs WHERE key = ?", key).Scan(&got); err != nil {
			t.Fatalf("read description %s: %v", key, err)
		}
		if got != want {
			t.Errorf("%s description=%q want %q", key, got, want)
		}
	}
}

func TestConfigDescriptionsHaveNoVersionRefs(t *testing.T) {
	setupConfigSeedDB(t)
	re := regexp.MustCompile(`(?i)(\bv\d+(\.\d+)*\b|\bP\d+-\d+\b|\b\d+\.\d+\.\d+\b)`)
	rows, err := DB.Query("SELECT key, description FROM configs")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, desc string
		if err := rows.Scan(&key, &desc); err != nil {
			t.Fatal(err)
		}
		if re.MatchString(key + " " + desc) {
			t.Errorf("config %q description %q still contains version/ticket reference", key, desc)
		}
	}
}
