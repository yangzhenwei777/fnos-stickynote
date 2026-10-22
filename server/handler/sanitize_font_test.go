package handler

import (
	"fmt"
	"strings"
	"testing"
)

// v21.88 回归：改字体后 style 必须存活（允许实体编码差异 &quot; vs &#34;）。
// 同时验证危险构造被剥。
func TestFontFamilySanitizeRepro(t *testing.T) {
	keepCases := []string{
		`<p><span style="font-family: &quot;Microsoft YaHei&quot;, 微软雅黑, sans-serif">文字</span></p>`,
		`<p><span style="font-family: SimSun, 宋体, serif">文字</span></p>`,
		`<p><span style="font-size: 24px">文字</span></p>`,
		`<p><span style="font-family: SimSun, 宋体, serif; font-size: 24px">文字</span></p>`,
		`<p><span style="font-family: 'Courier New', Courier, monospace">文字</span></p>`,
	}
	for i, in := range keepCases {
		out := sanitizeNoteContent(in)
		if !strings.Contains(out, "font-family") && strings.Contains(in, "font-family") {
			t.Errorf("keep case %d: font-family 被剥\n  IN : %s\n  OUT: %s", i, in, out)
		}
		if strings.Contains(in, "font-size") && !strings.Contains(out, "font-size") {
			t.Errorf("keep case %d: font-size 被剥\n  IN : %s\n  OUT: %s", i, in, out)
		}
		fmt.Printf("keep case %d PASS\n  OUT: %s\n", i, out)
	}

	stripCases := []string{
		`<p><span style="font-family: expression(alert(1))">x</span></p>`,
		`<p><span style="font-family: url(javascript:alert(1))">x</span></p>`,
		`<p><span style="font-family: red; color: expression(alert(1))">x</span></p>`,
	}
	for i, in := range stripCases {
		out := sanitizeNoteContent(in)
		low := strings.ToLower(out)
		if strings.Contains(low, "expression(") || strings.Contains(low, "javascript:") {
			t.Errorf("strip case %d: 危险构造未被剥\n  IN : %s\n  OUT: %s", i, in, out)
		}
		fmt.Printf("strip case %d PASS\n  OUT: %s\n", i, out)
	}
}
