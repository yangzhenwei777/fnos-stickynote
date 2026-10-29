package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stickynote/model"
)

// v2.2.2：MD 导出图片 base64 嵌入的 resolver 安全与回退行为（包内直测私有函数）

func TestNewMDImageResolver(t *testing.T) {
	// 临时 uploadDir：假 PNG（魔数头）+ 假 svg
	tmp := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A}
	if err := os.WriteFile(filepath.Join(tmp, "a.png"), png, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "b.svg"), []byte("<svg/>"), 0644); err != nil {
		t.Fatal(err)
	}
	oldDir := uploadDir
	SetUploadDir(tmp)
	defer SetUploadDir(oldDir)

	resolve := newMDImageResolver()

	t.Run("命中：读取文件输出 mime+data", func(t *testing.T) {
		mime, data, ok := resolve("/uploads/a.png")
		if !ok || mime != "image/png" || string(data) != string(png) {
			t.Errorf("mime=%q ok=%v len=%d", mime, ok, len(data))
		}
	})

	t.Run("缓存：同 src 二次命中不重复解析", func(t *testing.T) {
		_, d1, _ := resolve("/uploads/a.png")
		mime, d2, ok := resolve("/uploads/a.png")
		if !ok || mime != "image/png" || &d1[0] != &d2[0] {
			t.Errorf("缓存应返回同一 data 切片")
		}
	})

	t.Run("路径穿越拒绝", func(t *testing.T) {
		for _, src := range []string{
			"/uploads/../secret.txt",
			"/uploads/sub/dir.png",
			"/uploads/a..png/../x.png",
			"/uploads/..%2Fx.png",
		} {
			if _, _, ok := resolve(src); ok {
				t.Errorf("src %q 应被拒绝", src)
			}
		}
	})

	t.Run("非 uploads 前缀拒绝", func(t *testing.T) {
		for _, src := range []string{"https://evil.example.com/x.png", "data:image/png;base64,AAA", "/static/a.png"} {
			if _, _, ok := resolve(src); ok {
				t.Errorf("src %q 应被拒绝", src)
			}
		}
	})

	t.Run("扩展名不识别拒绝", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(tmp, "c.exe"), []byte("MZ"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, _, ok := resolve("/uploads/c.exe"); ok {
			t.Errorf(".exe 应被拒绝")
		}
	})

	t.Run("文件缺失回退", func(t *testing.T) {
		if _, _, ok := resolve("/uploads/ghost.png"); ok {
			t.Errorf("缺失文件应 ok=false")
		}
	})

	t.Run("大小写扩展名", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(tmp, "d.PNG"), png, 0644); err != nil {
			t.Fatal(err)
		}
		mime, _, ok := resolve("/uploads/d.PNG")
		if !ok || mime != "image/png" {
			t.Errorf("大写 .PNG 应命中 mime=%q ok=%v", mime, ok)
		}
	})
}

// buildNoteMD 端到端：含图笔记导出后图片以内联 data URI 呈现
func TestBuildNoteMDImageEmbedding(t *testing.T) {
	tmp := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G'}
	if err := os.WriteFile(filepath.Join(tmp, "img.png"), png, 0644); err != nil {
		t.Fatal(err)
	}
	oldDir := uploadDir
	SetUploadDir(tmp)
	defer SetUploadDir(oldDir)

	n := model.Note{Title: "T", Content: `<p>前文</p><img src="/uploads/img.png" alt="图"><p>后文</p>`}
	out := buildNoteMD(&n, newMDImageResolver())

	if !strings.Contains(out, "![图](data:image/png;base64,") {
		t.Errorf("应内联 base64\n输出: %s", out)
	}
	if strings.Contains(out, "/uploads/img.png") {
		t.Errorf("不应保留原引用\n输出: %s", out)
	}
}

// v2.2.2 R2（用户实测：拖宽下载 md 丢列宽）：buildNoteMD 端到端——
// ① 带列宽表格以原生 HTML 块导出，colgroup/colwidth 两通道宽度信息保留；
// ② 导出前统一消毒：未被 Web 通道消毒的注入构造（模拟同步 push 直入内容）不得随 .md 带出。
func TestBuildNoteMDTableColWidth(t *testing.T) {
	t.Run("列宽表格→HTML块", func(t *testing.T) {
		n := model.Note{Title: "T", Content: `<p>表</p><table><colgroup><col style="width: 180px;"><col style="min-width: 25px;"></colgroup><tbody><tr><th colwidth="180"><p>A</p></th><th><p>B</p></th></tr><tr><td colwidth="180"><p>1</p></td><td><p>2</p></td></tr></tbody></table>`}
		out := buildNoteMD(&n, nil)
		// v2.2.2 R4：列宽按百分比导出（未拖列按已拖列均值补齐 → 180/180 = 50%/50%），
		// 表格 fixed + 100% 宽，保证 md 在任意查看器窗口下与预览同比例。
		for _, want := range []string{
			`table-layout: fixed; width: 100%`,
			`<col style="width: 50%"/>`,
			`style="width: 50%"`,
			`colwidth="180"`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("列宽信息应保留 %q\n输出: %s", want, out)
			}
		}
		if strings.Contains(out, "| A |") {
			t.Errorf("带列宽表格不应退化为 pipe 表\n输出: %s", out)
		}
		if strings.Contains(out, "width: 180px") {
			t.Errorf("不应残留 px 列宽（会重新引入容器宽度依赖）\n输出: %s", out)
		}
	})

	t.Run("导出前消毒（fail-closed）", func(t *testing.T) {
		// 模拟同步 push 直入的未消毒内容：script/事件属性必须在导出层被剥
		n := model.Note{Title: "T", Content: `<p onclick="evil()">x</p><script>alert(1)</script>`}
		out := buildNoteMD(&n, nil)
		if strings.Contains(out, "evil()") || strings.Contains(out, "<script") || strings.Contains(out, "alert(1)") {
			t.Errorf("注入构造不得随导出带出\n输出: %s", out)
		}
		if !strings.Contains(out, "x") {
			t.Errorf("正文文本应保留\n输出: %s", out)
		}
	})
}
