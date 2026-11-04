package model

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"stickynote/database"
)

func TestParseSearchSyntax(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want searchSyntax
	}{
		{"空串", "", searchSyntax{}},
		{"纯关键词", "会议记录", searchSyntax{Keyword: "会议记录"}},
		{"单标签", "tag:工作 会议", searchSyntax{Keyword: "会议", Tags: []string{"工作"}}},
		{"多标签去重", "tag:工作 tag:生活 tag:工作", searchSyntax{Tags: []string{"工作", "生活"}}},
		{"引号文件夹", `folder:"项目 资料" 预算`, searchSyntax{Keyword: "预算", Folder: "项目 资料"}},
		{"排序别名", "sort:title 报告", searchSyntax{Keyword: "报告", SortBy: "title"}},
		{"非法排序回退关键词", "sort:oops", searchSyntax{Keyword: "sort:oops"}},
		{"日期", "since:2026-01-01 until:2026-12-31 tag:工作", searchSyntax{Tags: []string{"工作"}, Since: "2026-01-01", Until: "2026-12-31"}},
		{"非法日期回退", "since:2026-13-01", searchSyntax{Keyword: "since:2026-13-01"}},
		{"空值回退", "tag:", searchSyntax{Keyword: "tag:"}},
		{"URL 不误判", "http://example.com", searchSyntax{Keyword: "http://example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSearchSyntax(tt.raw); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSearchSyntax(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseSearchSyntaxTruncatesKeyword(t *testing.T) {
	got := parseSearchSyntax(strings.Repeat("字", 250))
	if n := utf8.RuneCountInString(got.Keyword); n != 200 {
		t.Fatalf("关键词应截断为 200 字, got %d", n)
	}
}

func TestListNotesSearchSyntaxFilters(t *testing.T) {
	cleanup := setupQuotaDB(t)
	defer cleanup()

	uid := createUserForTest(t, "search_syntax_user")
	folder, err := CreateFolder(uid, "项目 资料")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	alpha, err := CreateNote(uid, "Alpha", "<p>预算讨论</p>", "工作", 0)
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	beta, err := CreateNoteInFolder(uid, "Beta", "<p>预算与排期</p>", "工作,项目", 0, folder.ID)
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	if _, err := database.DB.Exec("UPDATE notes SET updated_at = ? WHERE id = ?", "2026-01-10 10:00:00", alpha.ID); err != nil {
		t.Fatalf("set alpha time: %v", err)
	}
	if _, err := database.DB.Exec("UPDATE notes SET updated_at = ? WHERE id = ?", "2026-03-10 10:00:00", beta.ID); err != nil {
		t.Fatalf("set beta time: %v", err)
	}

	cases := []struct {
		name string
		q    string
		want int
	}{
		{"标签", "tag:工作", 2},
		{"标签加关键词走 FTS", "tag:工作 预算讨论", 1},
		{"文件夹按名称", `folder:"项目 资料"`, 1},
		{"since", "since:2026-02-01", 1},
		{"until", "until:2026-01-31", 1},
		{"非法语法不报错", "sort:oops", 0},
	}
	for _, tc := range cases {
		res, err := ListNotes(uid, 1, 20, tc.q, "", "updated_at", nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Total != tc.want {
			t.Fatalf("%s: total=%d want %d", tc.name, res.Total, tc.want)
		}
	}

	res, err := ListNotes(uid, 1, 20, "sort:title", "", "updated_at", nil)
	if err != nil {
		t.Fatalf("sort:title: %v", err)
	}
	if len(res.Notes) != 2 || res.Notes[0].Title != "Alpha" {
		t.Fatalf("sort:title 应覆盖参数排序, got %+v", res.Notes)
	}
}
