package notetext

import (
	"strings"
	"testing"
)

// v2.2.2 表格列宽：编辑器 Table resizable 产出的 colgroup/col 必须原样入库，
// 此前白名单缺这两个标签，拖宽保存即丢（预览/导出列宽全失效的根因）

func TestContentPolicyTableColWidth(t *testing.T) {
	dragged := `<table><colgroup><col style="width: 180px;"><col style="width: 120px;"></colgroup><tbody><tr><td>A</td><td>B</td></tr></tbody></table>`
	out := Content(dragged)

	if !strings.Contains(out, "<colgroup>") {
		t.Errorf("colgroup 应保留\n输出: %s", out)
	}
	if !strings.Contains(out, `width: 180px`) {
		t.Errorf("列宽 style 应保留\n输出: %s", out)
	}
	if !strings.Contains(out, "data-colwidth") && strings.Contains(out, "<col") {
		// col 标签应在（无论以何种属性形态）
		if !strings.Contains(out, "<col") {
			t.Errorf("col 标签应保留\n输出: %s", out)
		}
	}
	if strings.Contains(out, "onclick") || strings.Contains(out, "expression") {
		t.Errorf("事件/危险构造应被剥\n输出: %s", out)
	}

	// 恶意形态：col 注入事件与危险 CSS
	evil := `<table><colgroup><col style="width:expression(alert(1))" onclick="x()"></colgroup><tbody><tr><td>A</td></tr></tbody></table>`
	outEvil := Content(evil)
	if strings.Contains(outEvil, "expression") || strings.Contains(outEvil, "onclick") {
		t.Errorf("危险构造应被剥\n输出: %s", outEvil)
	}

	// colgroup 无属性形态（Tiptap 恒如此）不得被剥壳
	if !strings.Contains(outEvil, "<colgroup>") {
		t.Errorf("无属性 colgroup 应保留（allowNoAttrs）\n输出: %s", outEvil)
	}
}

// v2.2.2 R2（用户实测：拖宽保存成功、重新编辑回到初始宽度）：列宽持久化第二通道——
// 单元格 colwidth 属性（Tiptap 重解析首选通道）与 col 的 width 属性（回退通道，
// 只认属性不认 style）必须在白名单内原样入库；非数字值域一律剥。
func TestContentPolicyColWidthAttrs(t *testing.T) {
	in := `<table><colgroup><col width="180" style="width: 180px;"><col></colgroup><tbody><tr><th colspan="1" rowspan="1" colwidth="180"><p>A</p></th><th><p>B</p></th></tr><tr><td colwidth="180,120"><p>1</p></td><td><p>2</p></td></tr></tbody></table>`
	out := Content(in)

	for _, want := range []string{`colwidth="180"`, `colwidth="180,120"`, `<col width="180"`} {
		if !strings.Contains(out, want) {
			t.Errorf("列宽属性应保留 %q\n输出: %s", want, out)
		}
	}
	// 反向对照：非法值域必须剥（数字/逗号之外的任何构造都不放行）
	evil := `<table><tbody><tr><th colwidth="180" onclick="x()" style="width:expression(alert(1))"><p>A</p></th></tr></tbody></table>`
	outEvil := Content(evil)
	if strings.Contains(outEvil, "onclick") || strings.Contains(outEvil, "expression") {
		t.Errorf("事件/危险构造应被剥\n输出: %s", outEvil)
	}
	if strings.Contains(outEvil, `colwidth="180 abc"`) || strings.Contains(outEvil, "colwidth=\";x") {
		t.Errorf("非法 colwidth 值应被剥\n输出: %s", outEvil)
	}
	// col 的 width 属性值域同理：非纯数字剥
	outCol := Content(`<table><colgroup><col width="180px" style="width: 180px;"></colgroup><tbody><tr><td><p>x</p></td></tr></tbody></table>`)
	if strings.Contains(outCol, `width="180px"`) {
		t.Errorf("col width 非纯数字应被剥\n输出: %s", outCol)
	}
	if !strings.Contains(outCol, `width: 180px`) {
		t.Errorf("col style 宽度应保留\n输出: %s", outCol)
	}
}
