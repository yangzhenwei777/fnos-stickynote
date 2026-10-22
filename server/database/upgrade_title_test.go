package database

import "testing"

// v21.105 P0-1：存量标题实体反解逻辑单测（upgrade.go unescapeToFixpoint）。
// v21.79~v21.104 落库转义 + 前端原样回传导致逐次累积转义，本函数负责恢复原文。
func TestUnescapeToFixpoint(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// 无实体：原样返回（幂等，绝大多数标题走此路径零开销）
		{"普通标题", "普通标题"},
		{"Q&A", "Q&A"},
		// 单层转义（保存过一次的 "&"）
		{"Q&amp;A", "Q&A"},
		// 累积转义（v1.0.144 缺陷产物：保存两次/三次的 "&"）
		{"Q&amp;amp;A", "Q&A"},
		{"Q&amp;amp;amp;A", "Q&A"},
		// 混合实体（v21.78 语义会转义 <>&"' 五字符）
		{"a&lt;b &amp;&amp; c&gt;d &#34;e&#39;", "a<b && c>d \"e'"},
		// 用户刻意输入的实体文本：会被一并反解——已知可接受取舍（见 v21.105 交付说明）
		{"&amp;amp;", "&"},
	}
	for _, tc := range cases {
		if got := unescapeToFixpoint(tc.in); got != tc.want {
			t.Errorf("unescapeToFixpoint(%q) = %q, 期望 %q", tc.in, got, tc.want)
		}
	}
	// 幂等：反解结果再反解不变
	once := unescapeToFixpoint("Q&amp;amp;A")
	if twice := unescapeToFixpoint(once); twice != once {
		t.Errorf("unescapeToFixpoint 应幂等: %q -> %q", once, twice)
	}
}
