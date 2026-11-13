package app

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"stickynote/database"
)

func TestLocalOutboxFailureRollsBackNoteCreate(t *testing.T) {
	setupDB(t)
	sentinel := errors.New("outbox unavailable")
	svc := NewNoteService(100)
	svc.LocalHooks = &LocalSyncHooks{Note: func(*sql.Tx, int64, string) error { return sentinel }}

	if _, err := svc.CreateNote(1, "must rollback", "<p>x</p>", "", nil); !errors.Is(err, sentinel) {
		t.Fatalf("CreateNote error = %v, want sentinel", err)
	}
	var n int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("Outbox 失败后业务行仍被提交: count=%d", n)
	}
}

func TestLocalOutboxOperationsRunInsideBusinessTransaction(t *testing.T) {
	setupDB(t)
	var operations []string
	svc := NewNoteService(100)
	svc.LocalHooks = &LocalSyncHooks{Note: func(tx *sql.Tx, id int64, op string) error {
		// hook 必须能读到同事务中的业务行，证明不是提交后补记。
		var title string
		if err := tx.QueryRow(`SELECT title FROM notes WHERE id = ?`, id).Scan(&title); err != nil {
			return err
		}
		operations = append(operations, op+":"+title)
		return nil
	}}
	n, err := svc.CreateNote(1, "v1", "<p>x</p>", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateNote(n.ID, 1, "v2", "<p>y</p>", "", n.Version); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteNote(n.ID, 1); err != nil {
		t.Fatal(err)
	}
	want := []string{"CREATE:v1", "UPDATE:v2", "DELETE:v2"}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("operations = %v, want %v", operations, want)
	}
}

func TestEmptyTrashOutboxFailureKeepsTrashedRows(t *testing.T) {
	setupDB(t)
	svc := NewNoteService(100)
	n, err := svc.CreateNote(1, "keep me", "<p>x</p>", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteNote(n.ID, 1); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("purge enqueue failed")
	svc.LocalHooks = &LocalSyncHooks{Note: func(*sql.Tx, int64, string) error { return sentinel }}
	if _, _, err := svc.EmptyTrash(1); !errors.Is(err, sentinel) {
		t.Fatalf("EmptyTrash error = %v, want sentinel", err)
	}
	var nrows int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM notes WHERE id = ? AND deleted_at IS NOT NULL`, n.ID).Scan(&nrows); err != nil {
		t.Fatal(err)
	}
	if nrows != 1 {
		t.Fatal("PURGE 入队失败后回收站行被错误删除")
	}
}

func TestLocalFolderOutboxFailureRollsBackCreate(t *testing.T) {
	setupDB(t)
	sentinel := errors.New("folder outbox unavailable")
	svc := NewFolderService()
	svc.LocalHooks = &LocalSyncHooks{Folder: func(*sql.Tx, int64, string) error { return sentinel }}
	if _, err := svc.Create(1, "must rollback"); !errors.Is(err, sentinel) {
		t.Fatalf("Create folder error = %v, want sentinel", err)
	}
	var n int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM note_folders`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("文件夹 Outbox 失败后业务行仍被提交: count=%d", n)
	}
}
