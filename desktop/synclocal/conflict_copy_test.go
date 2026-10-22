package synclocal

// conflict_copy_test.go —— 「另存副本」载荷解析（1.0.218）回归钉。
// 钉住：放行件带出 folder_sync_id（落回原文件夹的依据）；加密件/文件夹件/排序件/坏载荷
// 一律拒绝且给上屏原因——防"另存出一条空笔记"（既有纪律）。

import (
	"strings"
	"testing"
)

func TestPlanConflictCopy(t *testing.T) {
	ok := PlanConflictCopy(`{"sync_id":"s1","title":"标题","content":"<p>正文</p>","tags":"a,b","folder_sync_id":"f-1"}`)
	if !ok.OK || ok.Title != "标题" || ok.Content != "<p>正文</p>" || ok.Tags != "a,b" || ok.FolderSyncID != "f-1" {
		t.Fatalf("正常笔记应放行并带出 folder_sync_id，得 %+v", ok)
	}
	noFolder := PlanConflictCopy(`{"sync_id":"s2","title":"t","content":"c"}`)
	if !noFolder.OK || noFolder.FolderSyncID != "" {
		t.Fatalf("无 folder_sync_id 应落「我的笔记」（空值），得 %+v", noFolder)
	}
	for _, tc := range []struct{ name, raw, kw string }{
		{"空载荷", "", "本机内容缺失"},
		{"非 JSON", "{", "不是笔记载荷"},
		{"文件夹载荷", `{"sync_id":"f","name":"夹","version":1}`, "仅笔记支持"},
		{"排序载荷", `{"order":["a","b"],"bases":{}}`, "仅笔记支持"},
		{"加密笔记", `{"sync_id":"s3","title":"x","content":"y","is_encrypted":true}`, "加密笔记"},
	} {
		r := PlanConflictCopy(tc.raw)
		if r.OK || r.Reason == "" {
			t.Fatalf("%s：应拒绝并给原因，得 %+v", tc.name, r)
		}
		if !strings.Contains(r.Reason, tc.kw) {
			t.Fatalf("%s：原因应含 %q，得 %q", tc.name, tc.kw, r.Reason)
		}
	}
}
