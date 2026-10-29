package notetext

import (
	"strings"
	"testing"
)

// v2.2.2 R3（R2 缺陷回归钉）：R2 的「载入前正则改写列宽」会在表格前遗留
// `<p>&gt;&gt;&gt;</p>` 段落，且**每点一次编辑就多一行**（用户 R2 fpk 实测）。
// 清理必须：①连续多行全剥；②只剥紧邻表格的纯 '>' 段落；③幂等。
func TestStripLegacyColWidthArtifacts(t *testing.T) {
	const tbl = `<table><colgroup><col style="width: 180px"><col></colgroup><tbody><tr><td>A</td><td>B</td></tr></tbody></table>`

	t.Run("连续多行全剥且保留正文与表格", func(t *testing.T) {
		in := `<p>正文</p><p>&gt;&gt;&gt;</p><p>&gt;&gt;</p>` + tbl + `<p>尾</p>`
		out := StripLegacyColWidthArtifacts(in)
		if strings.Contains(out, "&gt;") {
			t.Errorf("伪影应清空\n输出: %s", out)
		}
		for _, want := range []string{"<p>正文</p>", "<p>尾</p>", "<table>", "<colgroup>"} {
			if !strings.Contains(out, want) {
				t.Errorf("应保留 %q\n输出: %s", want, out)
			}
		}
	})

	t.Run("真实正文里的 > 不误删", func(t *testing.T) {
		in := `<p>&gt; 引用式文本</p><p>段落</p>`
		if out := StripLegacyColWidthArtifacts(in); out != in {
			t.Errorf("非紧邻表格的 > 段落不应改动\n输入: %s\n输出: %s", in, out)
		}
		// 表格前但中间隔了其他段落：不属于缺陷形态，不剥
		far := `<p>&gt;</p><p>中间段</p>` + tbl
		if out := StripLegacyColWidthArtifacts(far); out != far {
			t.Errorf("非紧邻表格的 > 段落不应改动\n输出: %s", out)
		}
	})

	t.Run("幂等", func(t *testing.T) {
		clean := `<p>x</p>` + tbl
		if out := StripLegacyColWidthArtifacts(clean); out != clean {
			t.Errorf("已干净内容不应变化\n输出: %s", out)
		}
	})

	t.Run("无 colgroup 的内容直通（零开销守卫）", func(t *testing.T) {
		in := `<p>&gt;&gt;</p><table><tbody><tr><td>x</td></tr></tbody></table>`
		if out := StripLegacyColWidthArtifacts(in); out != in {
			t.Errorf("无 colgroup 应直通\n输出: %s", out)
		}
	})
}

// Content 入口：清理与消毒串联后，入库/导出两条路径都拿不到伪影
func TestContentStripsColWidthArtifacts(t *testing.T) {
	in := `<p>&gt;&gt;&gt;</p><table><colgroup><col style="width: 180px"><col></colgroup><tbody><tr><td>A</td><td>B</td></tr></tbody></table>`
	out := Content(in)
	if strings.Contains(out, "&gt;") {
		t.Errorf("Content 输出不应含伪影\n输出: %s", out)
	}
	if !strings.Contains(out, "colgroup") || !strings.Contains(out, "width: 180px") {
		t.Errorf("列宽信息应保留\n输出: %s", out)
	}
}
