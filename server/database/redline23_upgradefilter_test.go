package database

// redline23_upgradefilter_test.go —— 红线 23「Desktop 误执行 Server-only Migration」的**负向断言**。
//
// 台账原缺口：桌面**有意**跑服务端迁移栈且钉版 localstoreAppVersion="1.0.174"
// （desktop/startup_localstore.go:32,93），升级防降级与超版本 skip 均存在
// （database/upgrade.go:47-64），但**全部只有正例、无一条负向断言** —— 坑仅记于 README。
// 也就是说：若有人把 `u.version > appVersion` 的 skip 改成 `<`，测试**全绿**；
// 若把防降级检查删掉，测试**也全绿**。这是「护栏没有牙齿」。
//
// 本文件用**可注入的假升级栈**把三条过滤规则逐条钉死（全部真跑真断言，反向用例必红）：
//   ① 已执行过（u.version ≤ dbVersion）→ 跳过，不重复跑；
//   ② 超出当前二进制版本（u.version > appVersion）→ 跳过，**不得**记录进 upgrade_records；
//   ③ 防降级（dbVersion > appVersion）→ 直接报错，**不得**执行任何迁移。
// 外加 ④ 幂等：同版本重复 runUpgrades 不新增记录。

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeUpgrades 临时替换包级 upgrades 栈（测试与runUpgrades 同包，可直接赋值）。
func withFakeUpgrades(t *testing.T, fake []upgrade) {
	t.Helper()
	saved := upgrades
	upgrades = fake
	t.Cleanup(func() { upgrades = saved })
}

// appliedVersions 读 upgrade_records 里已记录的版本（升序）。
func appliedVersions(t *testing.T) []string {
	t.Helper()
	rows, err := DB.Query(`SELECT version FROM upgrade_records ORDER BY version ASC`)
	if err != nil {
		t.Fatalf("读 upgrade_records: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func hasVersion(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestRedline23UpgradeVersionFilter —— 三条过滤规则的负向钉测。
func TestRedline23UpgradeVersionFilter(t *testing.T) {
	t.Run("①已执行过与②超版本_都跳过且不落记录", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "r23a.db")
		if err := Init(dbPath, "1.0.0"); err != nil {
			t.Fatalf("db init: %v", err)
		}
		defer func() { DB.Close(); DB = nil }()

		var ran []string
		record := func(v string) upgrade {
			return upgrade{version: v, fn: func(tx *sql.Tx) error {
				ran = append(ran, v)
				// 表名去掉版本里的点（"probe_1.0.1" 不是合法标识符）
				_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS probe_` + strings.ReplaceAll(v, ".", "_") + ` (x INTEGER)`)
				return err
			}}
		}
		withFakeUpgrades(t, []upgrade{record("1.0.0"), record("1.0.1"), record("1.0.2"), record("1.9.9")})

		// 二进制版本 = 1.0.2 ⇒ 应跑 1.0.1 与 1.0.2（1.0.0 首装已执行、1.9.9 超版本）
		if err := runUpgrades("1.0.2"); err != nil {
			t.Fatalf("runUpgrades: %v", err)
		}
		if len(ran) != 2 || ran[0] != "1.0.1" || ran[1] != "1.0.2" {
			t.Fatalf("红线23：应执行 1.0.1 与 1.0.2（1.0.0 已执行 / 1.9.9 超版本），实得 %v", ran)
		}
		if hasVersion(ran, "1.9.9") {
			t.Fatal("红线23：超出版本 1.9.9 被执行了 —— 版本过滤失效")
		}
		if hasVersion(ran, "1.0.0") {
			t.Fatal("红线23：已执行的 1.0.0 被重复执行 —— 过滤失效")
		}

		// 关键负向断言：跳过的版本**不得**进 upgrade_records
		// （若落了记录，将来二进制升到 1.9.9 时该迁移会被误判为「已执行」而永久跳过）
		got := appliedVersions(t)
		if hasVersion(got, "1.9.9") {
			t.Fatalf("红线23：超版本 1.9.9 被记入 upgrade_records（将来会永久跳过该迁移）：%v", got)
		}
		if !hasVersion(got, "1.0.0") {
			t.Fatalf("1.0.0 应已记录（首装跑过）：%v", got)
		}
		if !hasVersion(got, "1.0.1") || !hasVersion(got, "1.0.2") {
			t.Fatalf("1.0.1/1.0.2 应已记录：%v", got)
		}

		// 幂等：同版本再跑一次不得重复执行
		ran = nil
		if err := runUpgrades("1.0.2"); err != nil {
			t.Fatalf("复跑 runUpgrades: %v", err)
		}
		if len(ran) != 0 {
			t.Fatalf("同版本复跑不得再执行迁移，实得 %v", ran)
		}
	})

	t.Run("②负向_二进制升版后超版本迁移必须被执行一次", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "r23b.db")
		if err := Init(dbPath, "1.0.0"); err != nil {
			t.Fatalf("db init: %v", err)
		}
		defer func() { DB.Close(); DB = nil }()

		var ran []string
		mk := func(v string) upgrade {
			return upgrade{version: v, fn: func(tx *sql.Tx) error {
				ran = append(ran, v)
				return nil
			}}
		}
		withFakeUpgrades(t, []upgrade{mk("1.0.0"), mk("1.9.9")})

		if err := runUpgrades("1.0.2"); err != nil { // 1.9.9 超版本
			t.Fatalf("runUpgrades: %v", err)
		}
		if hasVersion(ran, "1.9.9") {
			t.Fatal("1.9.9 不该在 1.0.2 上执行")
		}
		if hasVersion(appliedVersions(t), "1.9.9") {
			t.Fatal("1.9.9 不该被记录")
		}

		// 升级二进制到 1.9.9 ⇒ 该迁移必须补跑（证明上一步的「跳过 + 不记录」是正确的，
		// 否则这里会因被误判已执行而**永不执行** —— 那才是真缺陷）
		ran = nil
		if err := runUpgrades("1.9.9"); err != nil {
			t.Fatalf("升版 runUpgrades: %v", err)
		}
		if len(ran) != 1 || ran[0] != "1.9.9" {
			t.Fatalf("二进制升到 1.9.9 后该迁移必须补跑一次，实得 %v", ran)
		}
	})

	t.Run("③防降级_库版本更高时拒绝且不执行任何迁移", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "r23c.db")
		if err := Init(dbPath, "1.0.0"); err != nil {
			t.Fatalf("db init: %v", err)
		}
		defer func() { DB.Close(); DB = nil }()

		var ran []string
		withFakeUpgrades(t, []upgrade{{version: "2.0.0", fn: func(tx *sql.Tx) error {
			ran = append(ran, "2.0.0")
			return nil
		}}})
		if err := runUpgrades("1.0.0"); err != nil {
			t.Fatalf("基线 runUpgrades: %v", err)
		}
		// 人为把库版本抬到 2.0.0（模拟「用户拿新版本建库、又用旧二进制打开」）
		if _, err := DB.Exec(`INSERT INTO upgrade_records (version) VALUES ('2.0.0')`); err != nil {
			t.Fatal(err)
		}

		ran = nil
		err := runUpgrades("1.0.0") // 二进制 1.0.0 < 库 2.0.0 ⇒ 必须拒绝
		if err == nil {
			t.Fatal("红线23：库版本高于二进制时必须拒绝启动（防降级），实得 nil")
		}
		if len(ran) != 0 {
			t.Fatalf("拒绝路径不得执行任何迁移，实得 %v", ran)
		}
	})

	t.Run("④版本号格式容错", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "r23d.db")
		if err := Init(dbPath, "v1.0.0"); err != nil {
			t.Fatalf("db init: %v", err)
		}
		defer func() { DB.Close(); DB = nil }()
		// "v1.0.0" 应被 normalize 成 "1.0.0"，否则 compareVersions 恒判不等 → 每次都重跑迁移
		var ran []string
		withFakeUpgrades(t, []upgrade{{version: "1.0.1", fn: func(tx *sql.Tx) error {
			ran = append(ran, "1.0.1")
			return nil
		}}})
		if err := runUpgrades("v1.0.1"); err != nil {
			t.Fatalf("runUpgrades(v1.0.1): %v", err)
		}
		if len(ran) != 1 {
			t.Fatalf("v 前缀版本应被规范化后正常执行一次，实得 %v", ran)
		}
		ran = nil
		if err := runUpgrades("1.0.1"); err != nil {
			t.Fatalf("复跑: %v", err)
		}
		if len(ran) != 0 {
			t.Fatalf("规范化后同版本复跑不得重跑，实得 %v", ran)
		}
	})
}
