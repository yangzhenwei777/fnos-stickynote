package sync

// note_move_push_test.go —— v21.169 回归钉（用户实机缺陷）：
// 「将笔记移动到其他文件夹」双端均不同步。
//
// 根因：opCreate 会写 notes.folder_id，而 opCasUpdate 的 UPDATE 语句**漏了 folder_id**
// ⇒ 客户端把 op 推上来、服务端也 ACK，但文件夹没变（静默失效，最难查的一类）。
//
// 本文件钉住三态：移到自定义文件夹 / 移回默认文件夹（NULL）/ 未知 folder_sync_id 被拒。

import (
	"database/sql"
	"testing"

	"stickynote/database"
)

// newFolder 建一个自定义文件夹，返回其 id 与 sync_id。
func newFolder(t *testing.T, name, syncID string) int64 {
	t.Helper()
	res, err := database.DB.Exec(`INSERT INTO note_folders (user_id, name, sync_id, sort_order, version)
		VALUES (1, ?, ?, 0, 1)`, name, syncID)
	if err != nil {
		t.Fatalf("建文件夹 %s: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// folderOf 读笔记当前 folder_id（sql.NullInt64：NULL = 默认「我的笔记」）。
func folderOf(t *testing.T, noteSyncID string) sql.NullInt64 {
	t.Helper()
	var fn sql.NullInt64
	var fid sql.NullInt64
	err := database.DB.QueryRow(`SELECT n.folder_id, f.id FROM notes n
		LEFT JOIN note_folders f ON f.id = n.folder_id AND f.user_id = n.user_id
		WHERE n.sync_id = ?`, noteSyncID).Scan(&fid, &fn)
	if err != nil {
		t.Fatalf("读笔记 %s 的 folder: %v", noteSyncID, err)
	}
	return fn
}

// moveOp 构造「移动到某文件夹」的 UPDATE op（payload 带 folder_sync_id）。
func moveOp(opID, noteSyncID, folderSyncID string, base int64) PushOp {
	b := base
	return PushOp{OpID: opID, EntityType: "NOTE", EntitySyncID: noteSyncID, Operation: "UPDATE", BaseVersion: &b,
		Payload: mustJSON(noteP{"sync_id": noteSyncID, "title": "t", "content": "<p>c</p>", "tags": "",
			"folder_sync_id": folderSyncID})}
}

func TestNoteMoveSyncsToFolder(t *testing.T) {
	r := setupSyncDB(t)
	fid := newFolder(t, "工作", "f-work")

	if res := doPush(t, r, "d1", createOp("op-c1", "s-move", "待移动")); res[0].Code != CodeAcked {
		t.Fatalf("create: %+v", res)
	}
	// 起点必须是默认文件夹（NULL），否则本用例无意义
	if got := folderOf(t, "s-move"); got.Valid {
		t.Fatalf("前置：新笔记应挂在默认文件夹（NULL），实得 id=%d", got.Int64)
	}

	var base int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE sync_id='s-move'`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	res := doPush(t, r, "d1", moveOp("op-m1", "s-move", "f-work", base))
	if res[0].Code != CodeAcked {
		t.Fatalf("移动应 ACKED，得 %+v", res[0])
	}

	got := folderOf(t, "s-move")
	if !got.Valid || got.Int64 != fid {
		t.Fatalf("v21.169：移动后服务端 folder_id 应为 %d，实得 %+v（修复前此断言必红 = 缺陷复现）", fid, got)
	}
}

func TestNoteMoveBackToDefaultFolder(t *testing.T) {
	r := setupSyncDB(t)
	fid := newFolder(t, "工作", "f-work2")

	// 先建在自定义文件夹（CREATE 路径本来就支持 folder_id）
	sid := "f-work2"
	res := doPush(t, r, "d1", PushOp{OpID: "op-c1", EntityType: "NOTE", EntitySyncID: "s-back", Operation: "CREATE",
		BaseVersion: ptrI(0),
		Payload:     mustJSON(noteP{"sync_id": "s-back", "title": "t", "content": "<p>c</p>", "tags": "", "folder_sync_id": &sid})})
	if res[0].Code != CodeAcked {
		t.Fatalf("create in folder: %+v", res)
	}
	if got := folderOf(t, "s-back"); !got.Valid || got.Int64 != fid {
		t.Fatalf("前置：应挂在 f-work2（id=%d），实得 %+v", fid, got)
	}

	var base int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE sync_id='s-back'`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	// folder_sync_id 为空串 ⇒ 回默认文件夹（NULL）
	res = doPush(t, r, "d1", moveOp("op-m1", "s-back", "", base))
	if res[0].Code != CodeAcked {
		t.Fatalf("移回默认应 ACKED，得 %+v", res[0])
	}
	if got := folderOf(t, "s-back"); got.Valid {
		t.Fatalf("v21.169：移回默认文件夹后 folder_id 应为 NULL，实得 id=%d", got.Int64)
	}
}

func TestNoteMoveRejectsUnknownFolder(t *testing.T) {
	r := setupSyncDB(t)
	if res := doPush(t, r, "d1", createOp("op-c1", "s-unk", "x")); res[0].Code != CodeAcked {
		t.Fatalf("create: %+v", res)
	}
	var base int64
	if err := database.DB.QueryRow(`SELECT version FROM notes WHERE sync_id='s-unk'`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	// 未知 folder_sync_id 必须被拒（fail-closed，不得静默把笔记丢进默认文件夹）
	res := doPush(t, r, "d1", moveOp("op-m1", "s-unk", "no-such-folder", base))
	if res[0].Code == CodeAcked {
		t.Fatalf("v21.169：未知 folder_sync_id 必须拒绝（fail-closed），实得 ACKED %+v", res[0])
	}
	if got := folderOf(t, "s-unk"); got.Valid {
		t.Fatalf("被拒的移动不得改动词所属文件夹，实得 id=%d", got.Int64)
	}
}

func ptrI(v int64) *int64 { return &v }
