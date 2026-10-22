package bootstrap

// concurrency_pool_test.go —— v21.203 批次 4 回归：outbox 搬运的连接占用。
//
// 定罪：原实现在 `for rows.Next()` 循环**内部**又发查询（`carryEntityRow` 对
// oldDB 的 SELECT、对 newDB 的 SELECT/INSERT）。而 `OpenGenDB` 的
// `MaxOpenConns(2)`（§93 纪律）：
//   - `rows` 占住 oldDB 的一条连接不放；
//   - 循环内对 oldDB 的 SELECT 要第二条 —— 池刚好 2 条，任何一侧再触发隐式
//     取连接就会**自锁等待**（自己等自己释放的连接）。
// 症状：换代期间诊断包 4 个 db.* 计数全部 timeout（业务连接被占满）。
//
// 本文件用「池大小 = 2 的真实约束」把这条钉死：搬运完成后 oldDB 侧必须
// **立刻可用**（不留占住的游标），否则诊断包/同步就抢不到连接。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"stickynote-desktop/localstore"
)

// openPooledDB 开一个与生产同规格的库（MaxOpenConns=2，§93）。
func openPooledDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := localstore.OpenGenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2) // 与生产一致（本测试的核心约束）
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

// TestCarryOverOutboxLeavesNoOccupiedConnection —— ⭐ 核心钉子。
//
// 搬运 N 条 op（远超池容量）后，oldDB 必须**立刻**能响应新查询 —— 若实现仍在
// 循环里持有游标，这里会因池耗尽而超时。
func TestCarryOverOutboxLeavesNoOccupiedConnection(t *testing.T) {
	dir := t.TempDir()
	oldDB := openPooledDB(t, filepath.Join(dir, "old.db"))
	defer oldDB.Close()
	newDB := openPooledDB(t, filepath.Join(dir, "new.db"))
	defer newDB.Close()

	mustExec(t, oldDB, `CREATE TABLE IF NOT EXISTS sync_outbox (
		op_id TEXT PRIMARY KEY, entity_type TEXT, entity_sync_id TEXT, operation TEXT,
		base_version INTEGER, payload TEXT, status TEXT, retry_count INTEGER DEFAULT 0)`)
	// notes 列集必须与 carryEntityRow 的显式列清单一致（schema 演进时同改）
	mustExec(t, oldDB, `CREATE TABLE IF NOT EXISTS notes (
		id INTEGER PRIMARY KEY, user_id INTEGER, title TEXT, content TEXT, content_text TEXT,
		tags TEXT, is_encrypted INTEGER, encrypt_password_hash TEXT, encrypted_content TEXT,
		created_at TIMESTAMP, updated_at TIMESTAMP, deleted_at TIMESTAMP, version INTEGER,
		folder_id INTEGER, sync_id TEXT, trash_upload_bytes INTEGER)`)
	mustExec(t, newDB, `CREATE TABLE IF NOT EXISTS sync_outbox (
		op_id TEXT PRIMARY KEY, entity_type TEXT, entity_sync_id TEXT, operation TEXT,
		base_version INTEGER, payload TEXT, status TEXT, retry_count INTEGER DEFAULT 0)`)
	mustExec(t, newDB, `CREATE TABLE IF NOT EXISTS notes (
		id INTEGER PRIMARY KEY, user_id INTEGER, title TEXT, content TEXT, content_text TEXT,
		tags TEXT, is_encrypted INTEGER, encrypt_password_hash TEXT, encrypted_content TEXT,
		created_at TIMESTAMP, updated_at TIMESTAMP, deleted_at TIMESTAMP, version INTEGER,
		folder_id INTEGER, sync_id TEXT, trash_upload_bytes INTEGER)`)

	// 20 条 UPDATE op（走 carryEntityRow 的"新代查版本"分支）+ 5 条 CREATE
	for i := 0; i < 20; i++ {
		mustExec(t, oldDB, `INSERT INTO sync_outbox
			(op_id,entity_type,entity_sync_id,operation,base_version,payload,status,retry_count)
			VALUES (?,?,?,?,?,?,?,0)`, "op-u-"+strconv.Itoa(i), "NOTE", "sid-u-"+strconv.Itoa(i),
			"UPDATE", 1, "{}", "PENDING")
	}
	for i := 0; i < 5; i++ {
		mustExec(t, oldDB, `INSERT INTO sync_outbox
			(op_id,entity_type,entity_sync_id,operation,base_version,payload,status,retry_count)
			VALUES (?,?,?,?,?,?,?,0)`, "op-c-"+strconv.Itoa(i), "NOTE", "sid-c-"+strconv.Itoa(i),
			"CREATE", nil, "{}", "PENDING")
		mustExec(t, oldDB, `INSERT INTO notes (id,user_id,sync_id,version,title,created_at,updated_at) VALUES (?,1,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, i+1, "sid-c-"+strconv.Itoa(i), 1, "t")
	}

	carried, conflicts, dropped, err := carryOverOutbox(oldDB, newDB)
	if err != nil {
		t.Fatalf("搬运失败: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("无 FOLDER_ORDER 时 dropped 应为 0，实得 %d", dropped)
	}
	// 20 条 UPDATE 引用的实体在新代不存在（未随 CREATE 搬入）⇒ 全部转 CONFLICT
	if conflicts != 20 {
		t.Fatalf("新代无实体的 UPDATE 应转 CONFLICT = 20，实得 %d", conflicts)
	}
	// 5 条 CREATE 连同实体行一起搬入 ⇒ 计入 carried
	if carried != 5 {
		t.Fatalf("CREATE 应全部搬运 = 5，实得 %d", carried)
	}
	// 实体行确已搬入（"账在物在" 不变式）
	var ent int
	if err := newDB.QueryRow(`SELECT COUNT(*) FROM notes WHERE sync_id LIKE 'sid-c-%'`).Scan(&ent); err != nil {
		t.Fatal(err)
	}
	if ent != 5 {
		t.Fatalf("CREATE 的实体行应一并搬入 5 条，实得 %d", ent)
	}

	// ⭐ 关键断言：搬运结束后 oldDB **必须立刻可用**（没有游标占着连接）。
	// 用带超时的查询证明（超时 = 池被占满 = 旧实现会失败）。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int
	if err := oldDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_outbox`).Scan(&n); err != nil {
		t.Fatalf("搬运后 oldDB 不可用（游标未释放 / 连接被占满）: %v", err)
	}
	if n != 25 {
		t.Fatalf("oldDB 应仍有 25 条，实得 %d", n)
	}
	// 连续 3 次：证明不是侥幸（池复用后仍可用）
	for i := 0; i < 3; i++ {
		if err := oldDB.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n); err != nil {
			t.Fatalf("第 %d 次复查失败: %v", i+1, err)
		}
	}
}
