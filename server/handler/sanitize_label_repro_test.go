package handler

import (
	"strings"
	"testing"
)

// v21.116 回归：任务清单 label 必须在 sanitize 后存活。
//
// 根因：bluemonday 对「无属性开标签」另设 allowNoAttrs 门（内置无属性清单不含
// label），AllowElements("label") 只覆盖有属性分支——Tiptap TaskItem 序列化的
// label 恒无属性，被剥壳后阅读态复选框裸挂 li 下顶对齐（≈6.4px）。
// 修复：p.AllowNoAttrs().OnElements("label")。
//
// 用例 1 即修复前实测失败形态（API 仲裁 + 本地最小复现双重取证）。
func TestSanitizeTaskItemLabelKept(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"无属性label最小", `<label>x</label>`},
		{"无勾选taskItem", `<ul data-type="taskList"><li data-checked="false" data-type="taskItem"><label><input type="checkbox"><span></span></label><div><p>对齐项甲</p></div></li></ul>`},
		{"已勾选taskItem", `<ul data-type="taskList"><li data-checked="true" data-type="taskItem"><label><input type="checkbox" checked><span></span></label><div><p>对齐项乙</p></div></li></ul>`},
	}
	for _, c := range cases {
		out := sanitizeNoteContent(c.in)
		if !strings.Contains(out, "<label>") {
			t.Errorf("%s：label 被剥 out=%s", c.name, out)
			continue
		}
		// 结构断言仅对 taskItem 用例生效（最小用例 <label>x</label> 本无 input）
		if strings.Contains(c.in, "taskItem") && !strings.Contains(out, "<label><input type=\"checkbox\"") {
			t.Errorf("%s：label 内 input 结构破坏 out=%s", c.name, out)
		}
	}

	// 已勾选条目 checked 属性仍须保留（勾选态回读契约）
	out := sanitizeNoteContent(cases[2].in)
	if !strings.Contains(out, " checked") {
		t.Errorf("已勾选taskItem：checked 属性丢失 out=%s", out)
	}

	// 恶意面不回归：label 上不可注入事件属性
	evil := sanitizeNoteContent(`<label onclick="alert(1)" style="position:fixed">x</label>`)
	if strings.Contains(evil, "onclick") || strings.Contains(evil, "position") {
		t.Errorf("label 属性放行越界 out=%s", evil)
	}
}
