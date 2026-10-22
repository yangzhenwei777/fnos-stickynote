package handler

import (
	"strings"
	"testing"
)

// 图片禁外链策略（v18.7）：默认策略本机图保留、外链/data: 图剥除
func TestNoteContentPolicyLocalImg(t *testing.T) {
	p := buildNoteContentPolicy(false)
	out := p.Sanitize(`<p>本</p><img src="/uploads/a.png" width="320" alt="图" /><img src="https://evil.example.com/x.png" /><img src="data:image/png;base64,iVBOR" /><script>alert(1)</script>`)

	if !strings.Contains(out, `src="/uploads/a.png"`) {
		t.Errorf("本机 /uploads 图应保留: %s", out)
	}
	if strings.Contains(out, "evil.example.com") {
		t.Errorf("外链图应被剥除: %s", out)
	}
	if strings.Contains(out, "data:image") {
		t.Errorf("data: 内嵌图应被剥除（与历史行为一致）: %s", out)
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("script 应被剥除: %s", out)
	}
	if !strings.Contains(out, `width="320"`) {
		t.Errorf("图片 width 属性应保留: %s", out)
	}
}

// 开关放行策略（后悔药）：allowExternalImg=true 时外链图保留
func TestNoteContentPolicyAllowExternal(t *testing.T) {
	p := buildNoteContentPolicy(true)
	out := p.Sanitize(`<img src="https://evil.example.com/x.png" /><img src="/uploads/a.png" />`)
	if !strings.Contains(out, "evil.example.com") {
		t.Errorf("开关放行时外链图应保留: %s", out)
	}
	if !strings.Contains(out, `src="/uploads/a.png"`) {
		t.Errorf("开关放行时本机图同样保留: %s", out)
	}
}

// 白名单基础面：排版标签与安全 style 保留，事件属性剥除（两策略共用底座）
// 注：bluemonday 会把 CSS 值规范化为 "color: red"（冒号后空格），比对需去空格。
func TestNoteContentPolicyBase(t *testing.T) {
	for _, allow := range []bool{true, false} {
		p := buildNoteContentPolicy(allow)
		out := p.Sanitize(`<span style="color:red" onclick="x()">字色</span><p style="text-align:center">对齐</p><table><tr><td>表格</td></tr></table><article>块</article>`)
		compact := strings.ReplaceAll(out, " ", "")
		if !strings.Contains(compact, `style="color:red"`) {
			t.Errorf("allow=%v: span 字色 style 应保留: %s", allow, out)
		}
		if !strings.Contains(compact, `style="text-align:center"`) {
			t.Errorf("allow=%v: p 对齐 style 应保留: %s", allow, out)
		}
		if strings.Contains(out, "onclick") {
			t.Errorf("allow=%v: 事件属性应剥除: %s", allow, out)
		}
		// v20.1 起 table 系列标签入白名单（编辑器表格功能），应保留
		if !strings.Contains(out, "<table") {
			t.Errorf("allow=%v: 表格标签应保留（v20.1 白名单）: %s", allow, out)
		}
		if strings.Contains(out, "<div") {
			t.Errorf("allow=%v: 非白名单标签应剥除: %s", allow, out)
		}
	}
}

// v21.82：任务清单 / 字体字号 / 背景色 / 段落缩进 白名单放行，
// 同时确认 input 仅 checkbox、事件属性与危险样式仍被剥除
func TestNoteContentPolicyV122(t *testing.T) {
	p := buildNoteContentPolicy(false)
	in := `<ul data-type="taskList"><li data-type="taskItem" data-checked="true"><label><input type="checkbox" checked onclick="x()"></label><div><p>已办</p></div></li></ul>` +
		`<span style="font-family: SimSun; font-size: 24px; color: red">宋体大字</span>` +
		`<mark data-color="#fde047" style="background-color: #fde047">高亮</mark>` +
		`<p data-indent="2" style="margin-left: 4em">缩进</p>` +
		`<input type="text" value="x">`
	out := p.Sanitize(in)

	mustContain := []string{
		`data-type="taskList"`,
		`data-type="taskItem"`,
		`data-checked="true"`,
		`type="checkbox"`,
		"checked",
		"已办",
		"font-family",
		"font-size",
		"background-color",
		`data-indent="2"`,
		"margin-left",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("应保留 %q: %s", s, out)
		}
	}
	mustNotContain := []string{"onclick", `type="text"`, "<article", "expression("}
	for _, s := range mustNotContain {
		if strings.Contains(out, s) {
			t.Errorf("应剥除 %q: %s", s, out)
		}
	}
}

// v21.93 P3：TestFakeSecurityQuestion 随「安全问题统一话术」一并移除——
// 确定性派生的假问题本身是账号存在性预言机（攻击者可本地复算比对），
// 相关实现 fakeSecurityQuestions / fakeSecurityQuestion 已按 YAGNI 删除。
// 枚举防护改由「统一话术 + loginGuard 限流」承担，见
// TestGetSecurityQuestionHandler 中的统一话术断言。

// v21.105：标题消毒语义变更——不再做 HTML 实体转义，仅剥离控制字符、落库保持原文。
// 背景：v21.79 的 EscapeString 落库转义 + 前端 v-model 原样回传 = 每次保存追加一层
// 转义（"Q&A"→"Q&amp;A"→"Q&amp;amp;A"…，v1.0.144 操作逻辑审查 P0-1 实测复现）。
// XSS 防线移至消费端：前端 {{}} 纯文本插值自动转义、导出仅 .md 纯文本。
// （开发纪律 4.5：断言随产品行为更新，版本依据 v21.105）
func TestSanitizeNoteTitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// 原文保留：特殊字符不再转义（P0-1 核心回归点）
		{`Q&A`, `Q&A`},
		{`a<b && c>d`, `a<b && c>d`},
		{`Tom & Jerry`, `Tom & Jerry`},
		{`"><svg/onload=alert(1)>`, `"><svg/onload=alert(1)>`}, // 纯文本语义：存什么显什么，前端插值转义
		{`普通标题`, "普通标题"},
		{`emoji😀中文「引號」&amp;`, "emoji😀中文「引號」&amp;"}, // 用户刻意输入的实体文本也原样保留
	}
	for _, tc := range cases {
		if out := sanitizeNoteTitle(tc.in); out != tc.want {
			t.Errorf("sanitizeNoteTitle(%q) = %q, 期望 %q", tc.in, out, tc.want)
		}
	}
	// 控制字符处理：\n/\r/\t 折叠为空格，其余控制字符（含 NUL、DEL）剥除
	if out := sanitizeNoteTitle("a\nb\r\tc\x00d\x7f"); out != "a b  cd" {
		t.Errorf("控制字符处理不符: %q", out)
	}
	// 幂等性：消毒后的标题再次消毒结果不变（防再次引入累积变换）
	once := sanitizeNoteTitle("Q&A <测试>")
	if twice := sanitizeNoteTitle(once); twice != once {
		t.Errorf("sanitizeNoteTitle 应幂等: %q -> %q", once, twice)
	}
}

// v21.110 tiptap 3.x：任务清单条目的 a11y 隐藏 span（sr-only）依赖
// position/clip/overflow 等内联样式保持不可见——sanitize 若剥掉这些属性，
// "Task item checkbox for …" 辅助文本会以可见正文暴露（真机走查实测缺陷）。
// 白名单放行 sr-only 所需属性的同时，url()/expression() 等危险值仍必须被剥。
func TestSanitizeSROnlyStyle(t *testing.T) {
	in := `<ul data-type="taskList"><li data-checked="true"><label contenteditable="false"><input type="checkbox" checked><span style="position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0px, 0px, 0px, 0px); white-space: nowrap; border: 0px;">Task item checkbox for 项</span></label><div><p>项</p></div></li></ul>`
	out := sanitizeNoteContent(in)
	for _, want := range []string{"position:", "clip:", "overflow:", "data-checked", `type="checkbox"`} {
		if !strings.Contains(out, want) {
			t.Errorf("sr-only 链路属性丢失 %q: %s", want, out)
		}
	}
	evil := sanitizeNoteContent(`<span style="position: absolute; background: url(javascript:alert(1)); width: expression(alert(2))">x</span>`)
	if strings.Contains(evil, "url(") || strings.Contains(evil, "expression(") {
		t.Errorf("危险 CSS 值未剥除: %s", evil)
	}
}
