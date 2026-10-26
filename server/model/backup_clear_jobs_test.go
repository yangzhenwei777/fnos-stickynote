package model

import (
	"testing"
)

// v2.2.1 ClearBackupJobs 单测（纪律：正常/边界/错误三态 + 反向对照）。
// 回针：终态记录（SUCCESS/FAILED/INTERRUPTED）全清、返回删除数。
// 反向：运行中记录（STARTING/RUNNING）必须保留——删除会导致前端轮询 404。

func TestClearBackupJobs(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	// 造 5 条记录：3 终态 + 2 运行中（STARTING 为 CreateBackupJob 初始态，另一条推到 RUNNING）
	var runningID int64
	for _, status := range []string{"SUCCESS", "FAILED", "INTERRUPTED", "RUNNING", "STARTING"} {
		id, err := CreateBackupJob("backup", 0, "")
		if err != nil {
			t.Fatal(err)
		}
		if status == "RUNNING" {
			runningID = id
		}
		if status != "STARTING" && status != "RUNNING" {
			if err := FinishJob(id, status, "", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := UpdateJobPhase(runningID, "RUNNING", "SNAPSHOT"); err != nil {
		t.Fatal(err)
	}

	deleted, err := ClearBackupJobs()
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 {
		t.Fatalf("应清除 3 条终态记录, got %d", deleted)
	}

	jobs, err := ListBackupJobs(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("运行中记录应保留 2 条, got %d: %+v", len(jobs), jobs)
	}
	for _, j := range jobs {
		if j.Status != "STARTING" && j.Status != "RUNNING" {
			t.Fatalf("保留了非运行中记录: %+v", j)
		}
	}

	// 幂等：再清一次无终态记录可删
	deleted2, err := ClearBackupJobs()
	if err != nil {
		t.Fatal(err)
	}
	if deleted2 != 0 {
		t.Fatalf("二次清除应删 0 条, got %d", deleted2)
	}
}
