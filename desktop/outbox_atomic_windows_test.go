//go:build windows

package main

import (
	"testing"

	"stickynote/database"
)

func TestOutboxWriteFailureRollsBackLocalNoteUpdate(t *testing.T) {
	ls, _ := bootWithLogin(t)
	mountAccountingWorker(t)
	n, err := ls.CreateNote("original", "<p>v1</p>", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 用真实 SQLite 故障让 Outbox 写入失败；业务更新必须随之回滚。
	if _, err := database.DB.Exec(`DROP TABLE sync_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := ls.UpdateNote(n.ID, "must-not-commit", "<p>v2</p>", "", n.Version); err == nil {
		t.Fatal("Outbox 表缺失时更新必须失败，不能假装保存成功")
	}
	var title, content string
	if err := database.DB.QueryRow(`SELECT title, content FROM notes WHERE id = ?`, n.ID).Scan(&title, &content); err != nil {
		t.Fatal(err)
	}
	if title != "original" || content != "<p>v1</p>" {
		t.Fatalf("Outbox 失败后业务写被部分提交: title=%q content=%q", title, content)
	}
}
