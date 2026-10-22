package htmltext

import (
	"strings"
	"testing"
)

func TestToText(t *testing.T) {
	in := `<h2>标题二</h2><p>第一段<span style="color:red">红字</span>文本</p><p style="text-align:center">第二段</p><ul><li>项目一</li><li>项目二</li></ul><p>含图<img src="/uploads/a.png" width="320" alt="图片" />结尾</p><p><a href="https://example.com">链接文字</a></p><hr><p>末段<br>换行</p><script>alert(1)</script>`
	out := ToText(in)
	t.Logf("输出:\n%s", out)

	mustContain := []string{"标题二", "第一段红字文本", "第二段", "- 项目一", "- 项目二", "含图[图片]结尾", "链接文字", "---", "末段\n换行"}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("缺少期望内容: %q", s)
		}
	}
	mustNotContain := []string{"<p>", "<span", "style", "alert", "example.com", "width"}
	for _, s := range mustNotContain {
		if strings.Contains(out, s) {
			t.Errorf("不应包含: %q", s)
		}
	}
	if strings.Contains(out, "\n\n\n") {
		t.Error("存在三连换行")
	}
}
