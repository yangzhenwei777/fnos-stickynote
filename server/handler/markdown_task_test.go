package handler

import (
	"strings"
	"testing"
)

// v21.111（挂账①）：GFM 任务清单导入还原——gomarkdown 无 GFM task-list 扩展，
// 导入前置直构为 tiptap 同构 taskList HTML（v3 序列化形态），打通导出↔导入回环。

func TestGFMTaskListImportFlat(t *testing.T) {
	md := "- [x] 已完成任务\n- [ ] 未完成任务\n"
	out := mdToHTML(md)
	if !strings.Contains(out, `data-type="taskList"`) {
		t.Errorf("应产出 taskList 结构: %s", out)
	}
	// v3 parseHTML gate = li[data-type="taskItem"]，缺属性编辑器载入即降级普通列表
	if !strings.Contains(out, `<li data-type="taskItem" data-checked="true"`) {
		t.Errorf("li 应带 data-type=taskItem 与勾选态: %s", out)
	}
	if !strings.Contains(out, "已完成") || !strings.Contains(out, "未完成任务") {
		t.Errorf("任务文本应保留: %s", out)
	}
	// 不得混入普通列表文本形态（gomarkdown 对 `- [x]` 的裸文本处理）
	if strings.Contains(out, "<li>[x]") || strings.Contains(out, "<li>[ ]") {
		t.Errorf("不应退化为普通列表文本: %s", out)
	}
}

// v3 编辑器「保存→重开」循环的序列化前提：li 的 data-type/data-checked 必须过 sanitize
// 保留（149 升级轮未覆盖的回归面，UI 探针实证缺失即任务清单重开降级）。
func TestGFMTaskListImportV3SerializationKept(t *testing.T) {
	in := `<ul data-type="taskList"><li data-type="taskItem" data-checked="true"><div><p>已做</p></div></li>` +
		`<li data-type="taskItem" data-checked="false"><div><p>没做</p></div></li></ul>`
	out := sanitizeNoteContent(in)
	if !strings.Contains(out, `<li data-type="taskItem" data-checked="true"`) ||
		!strings.Contains(out, `<li data-type="taskItem" data-checked="false"`) {
		t.Errorf("sanitize 后 v3 taskItem 属性应全保留: %s", out)
	}
}

func TestGFMTaskListImportNested(t *testing.T) {
	md := "- [x] 父任务\n  - [ ] 子任务甲\n  - [x] 子任务乙\n- [ ] 第二父任务\n"
	out := mdToHTML(md)
	// 两个嵌套层级：ul 出现在 li 内部
	ulCount := strings.Count(out, `<ul data-type="taskList">`)
	if ulCount < 2 {
		t.Errorf("嵌套应产生子 taskList（got %d）: %s", ulCount, out)
	}
	if !strings.Contains(out, "子任务甲") || !strings.Contains(out, "第二父任务") {
		t.Errorf("嵌套项文本应保留: %s", out)
	}
	// 标签配平
	if strings.Count(out, "<ul") != strings.Count(out, "</ul>") ||
		strings.Count(out, "<li") != strings.Count(out, "</li>") {
		t.Errorf("标签应配平: %s", out)
	}
}

func TestGFMTaskListImportFenceExempt(t *testing.T) {
	md := "```\n- [x] 代码块内不转换\n```\n\n- [ ] 真任务\n"
	out := mdToHTML(md)
	if !strings.Contains(out, "- [x] 代码块内不转换") {
		t.Errorf("围栏内任务行应原样保留: %s", out)
	}
	if strings.Count(out, `data-type="taskList"`) != 1 {
		t.Errorf("仅围栏外的组应转换: %s", out)
	}
}

func TestGFMTaskListImportPlainListUntouched(t *testing.T) {
	md := "- 普通项一\n- 普通项二\n\n1. 有序项\n"
	out := mdToHTML(md)
	if strings.Contains(out, `data-type="taskList"`) {
		t.Errorf("普通列表不应被误转: %s", out)
	}
	if !strings.Contains(out, "普通项一") {
		t.Errorf("普通列表内容应保留: %s", out)
	}
}

func TestGFMTaskListImportInlineFormat(t *testing.T) {
	md := "- [x] **粗体**与[链接](https://example.com)\n"
	out := mdToHTML(md)
	if !strings.Contains(out, "<strong>粗体</strong>") {
		t.Errorf("行内格式应渲染: %s", out)
	}
	if !strings.Contains(out, "https://example.com") {
		t.Errorf("链接应保留: %s", out)
	}
}

func TestGFMTaskListImportMarkersAndCase(t *testing.T) {
	md := "* [X] 星号大写\n+ [x] 加号\n"
	out := mdToHTML(md)
	if strings.Count(out, `data-checked="true"`) != 2 {
		t.Errorf("*/+ 标记与 [X] 大写均应识别: %s", out)
	}
}

func TestGFMTaskListImportCRLF(t *testing.T) {
	md := "- [x] 回车换行\r\n- [ ] 第二项\r\n"
	out := mdToHTML(md)
	if strings.Count(out, `data-checked="true"`) != 1 || strings.Count(out, `data-checked="false"`) != 1 {
		t.Errorf("CRLF 输入应正常解析: %s", out)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("不应残留 \\r: %s", out)
	}
}

func TestGFMTaskListImportMixedBreaksGroup(t *testing.T) {
	// 混合列表（任务行夹普通行）：普通行断组回退 gomarkdown；其后的任务组以空行分隔
	// 独立成块（防被 gomarkdown 并入普通列表 li）——同列表被拆分的偏差已在交付说明记录
	md := "- [x] 任务项\n- 普通项\n- [ ] 任务项二\n"
	out := mdToHTML(md)
	if strings.Count(out, `data-type="taskList"`) != 2 {
		t.Errorf("两个任务组应各自独立成块: %s", out)
	}
	// 普通项不得包含嵌套进来的任务组
	if strings.Contains(out, "普通项\n<ul") {
		t.Errorf("任务组不应并入普通列表 li 内部: %s", out)
	}
	if !strings.Contains(out, "普通项") {
		t.Errorf("普通行应回退 gomarkdown 渲染: %s", out)
	}
}

func TestGFMTaskListImportFullRoundTrip(t *testing.T) {
	// 全链路：mdToHTML → sanitizeNoteContent 后 taskList 结构与勾选态保留
	// （sanitize 白名单对 data-type/data-checked 放行，v21.82 起实证）
	md := "# 项目周报\n\n- [x] 完成评审\n- [ ] 写周报\n"
	out := sanitizeNoteContent(mdToHTML(md))
	if !strings.Contains(out, `data-type="taskList"`) || !strings.Contains(out, `data-checked="true"`) {
		t.Errorf("sanitize 后任务清单结构应保留: %s", out)
	}
	if !strings.Contains(out, "<h1>") || !strings.Contains(out, "项目周报") {
		t.Errorf("标题应正常渲染: %s", out)
	}
}

func TestGFMTaskListImportEmptyAndBlank(t *testing.T) {
	// 空输入维持 gomarkdown 既有行为（返回空串；上层 ImportFile 有兜底）
	if out := mdToHTML(""); out != "" {
		t.Errorf("空输入应维持既有空串行为: %q", out)
	}
	// 空内容任务行（行尾即 [x]，GFM 允许）
	out := mdToHTML("- [x]\n")
	if !strings.Contains(out, `data-checked="true"`) {
		t.Errorf("行尾 [x]（无内容）应识别: %s", out)
	}
}
