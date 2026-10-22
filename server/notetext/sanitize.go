// Package notetext —— 笔记文本的**纯逻辑**转换与消毒（零 gin 依赖）。
//
// 为什么单独成包：同一套「文件 → 笔记(title, html)」管线要被**两个宿主**复用：
//   - 服务端在线导入：server/handler.ImportFile（HTTP multipart）
//   - 桌面离线导入：desktop.LocalService.ImportNote（Wails Binding）
//
// 放在 handler 里会让桌面为了两个纯函数拖入 gin/HTTP 栈；各自复制一份则必然漂移。
// 因此把与传输层无关的部分下沉到本包，handler 保留同名薄封装（既有调用点与测试零改动）。
//
// 内容纪律：本包代码自 server/handler/sanitize.go 与 markdown.go **原样搬移**，
// 语义与输出逐字节不变（搬移保真由 server/handler 既有测试继续把关）。
package notetext

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// 图片禁外链（v18.7，用户确认）：img src 仅允许本站 /uploads/。
// 注：data: base64 图同样被剥——并非本版收紧，全局协议白名单（http/https/mailto）
// 自始就不含 data:，bluemonday 的 AllowDataURIImages 与全局协议过滤冲突无法救回
// （已实测验证）。历史上海量版本同样是这个行为，且编辑器粘贴一律先走上传
// 落 /uploads/，正常路径无感知。
var imgLocalSrcRe = regexp.MustCompile(`^/uploads/`)

// v21.88 font-family 值白名单：字母/数字/中文/空格/单双引号/逗号/连字符/句点。
// bluemonday 内置 FontFamilyHandler（^('[a-z \-]+'|[a-z \-]+)$）不接受大写、
// 中文与引号字体名，导致 SimSun/宋体/"Microsoft YaHei" 全部被剥（v21.88 bug 根因）。
var fontFamilyRe = regexp.MustCompile(`^[\p{Han}A-Za-z0-9 '"_,\-.]+$`)

// BuildContentPolicy 构造笔记内容的 HTML 白名单策略。
//   - allowExternalImg=false（默认/唯一在役）：img src 仅 /uploads/
//   - allowExternalImg=true：img src 不限（历史行为，仅测试口径在用）
//
// 白名单覆盖 tiptap 编辑器的全部产出（StarterKit / Underline /
// TextStyle+Color / TextAlign / Image），其余标签、事件属性、
// 危险协议一律剥除，防御存储型 XSS。
func BuildContentPolicy(allowExternalImg bool) *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	// 结构与排版标签
	// v21.82：div/label/input 为任务清单（TaskList/TaskItem）渲染所需
	p.AllowElements("p", "br", "hr",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"strong", "b", "em", "i", "u", "s", "del", "strike",
		"code", "pre", "blockquote",
		"ul", "ol", "li", "span", "mark",
		"table", "thead", "tbody", "tr", "th", "td",
		"div", "label", "input")

	// v21.82 任务清单：data-type 标识 taskList/taskItem，data-checked 保存勾选态；
	// input 仅允许 checkbox 类型与 checked 布尔属性（事件属性仍被全局剥除）
	p.AllowAttrs("data-type").OnElements("ul", "li")
	p.AllowAttrs("data-checked").OnElements("li")
	p.AllowAttrs("data-indent").OnElements("p")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")

	// v21.116 缺陷修复：AllowElements("label") 不完整——bluemonday 对「无属性开标签」
	// 另设一道 allowNoAttrs 门（内置无属性清单见 policy.go addDefaultElementsWithoutAttrs，
	// p/div/li/span 等约 90 个元素在内，但 label 不在）。Tiptap TaskItem 序列化的
	// label 恒无属性（<label><input type="checkbox"><span></span></label>），被该门
	// 剥壳、子元素保留，致阅读态复选框裸挂 li 下顶对齐（与首行文字中线差 ≈6.4px）。
	// AllowNoAttrs().OnElements("label") 把 label 加入无属性放行清单——label 本身
	// 无执行语义、其属性仍被全局白名单剥除，不扩大注入面。input 恒带 type 属性
	// 走有属性分支，无需处理。
	p.AllowNoAttrs().OnElements("label")

	// 链接：仅安全协议 + 站内相对路径
	p.AllowElements("a")
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)

	// 图片
	p.AllowElements("img")
	if allowExternalImg {
		p.AllowAttrs("src", "alt", "title", "width", "height").OnElements("img")
	} else {
		// src 仅 /uploads/（Matching 管路径形态；协议层由全局白名单兜底）
		p.AllowAttrs("src").Matching(imgLocalSrcRe).OnElements("img")
		p.AllowAttrs("alt", "title", "width", "height").OnElements("img")
	}

	// 样式：仅放行颜色/字体/字号与文本对齐/缩进（bluemonday 会安全解析 CSS 值，
	// expression()/url() 等危险构造会被剥离）
	// v21.82：span 增加 font-family/font-size（字体/字号下拉）；
	//         mark 增加 background-color（背景颜色）；p 增加 margin-left（段落缩进）
	// v21.88 修复「改字体保存丢失」：bluemonday 内置 FontFamilyHandler 的正则
	// ^('[a-z \-]+'|[a-z \-]+)$ 只接受小写英文字母/空格/连字符——SimSun（大写）、
	// 宋体（中文）、"Microsoft YaHei"（引号+大写）全部被剥（font-size 不受影响，
	// 故症状为"字号能存、字体一存就丢"）。改用显式白名单正则：
	// 仅允许 字母/数字/中文/空格/单双引号/逗号/连字符/句点 组成的字体列表，
	// 从字符层面排除 url()/expression()/javascript: 等注入构造。
	p.AllowStyles("color", "font-size").OnElements("span")
	p.AllowStyles("font-family").Matching(fontFamilyRe).OnElements("span")
	// v21.110 tiptap 3.x：任务清单条目的无障碍隐藏 span（sr-only，内嵌
	// "Task item checkbox for …" 辅助文本）依赖 position: absolute + clip: rect 等
	// 样式保持视觉不可见——这些属性若被剥掉，辅助文本会暴露为可见正文污染笔记
	// （真机走查实测发现）。仅补这组 sr-only 所需属性；值层面 url()/expression()
	// 仍由 bluemonday CSS 解析剥除，无注入面。
	p.AllowStyles("position", "width", "height", "padding", "margin", "overflow",
		"clip", "clip-path", "white-space", "border").OnElements("span")
	p.AllowStyles("background-color").OnElements("mark")
	p.AllowStyles("text-align").OnElements("p", "h1", "h2", "h3", "h4", "h5", "h6", "li")
	p.AllowStyles("margin-left").OnElements("p")
	// Highlight 扩展同时输出 data-color（与 style 等价的冗余属性，放行便于回读）
	p.AllowAttrs("data-color").OnElements("mark")

	return p
}

// v21.68 P2-05：外链图片功能已废弃（产品方向为永久关闭），恒定走 localImg 策略，
// 删除 allow_external_images 死配置分支。
var contentPolicyLocalImg = BuildContentPolicy(false)

// Content 按白名单消毒笔记 HTML 内容，入库前调用。
func Content(content string) string {
	if content == "" {
		return content
	}
	return contentPolicyLocalImg.Sanitize(content)
}

// v21.105：标题消毒策略变更——不再做 HTML 实体转义，改为仅剥离控制字符，落库保持原文。
//
// 背景：v21.79 为防标题存储型 XSS 引入 html.EscapeString 落库转义；但标题的
// 全部消费端均为纯文本语义（前端 {{}} 插值自动转义、导出仅 .md 纯文本），
// 转义反成缺陷：前端把读回的已转义标题经 v-model 原样回传，服务端每次保存
// 再转义一层，"Q&A" 逐次腐化为 "Q&amp;A"→"Q&amp;amp;A"→…（v1.0.144 操作
// 逻辑审查 P0-1，实测复现）。XSS 防线由各消费端承担：前端 Vue 纯文本插值
// 天然转义；导出链路为 .md 纯文本文件，无标签执行语义。
// 剥离控制字符防导入路径混入 \x00-\x1F 等不可见字符（\n 会被换成空格，
// 标题保持单行纯文本）。存量已腐化标题由 upgrade.go 一次性回溯清洗。
//
// Title 消毒笔记标题（仅剥离控制字符，保持原文）。
func Title(title string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == '\v' || r == '\f':
			return ' ' // 控制类空白折叠为空格，保持标题单行
		case r < 0x20 || r == 0x7f:
			return -1 // 其余控制字符（含 NUL）直接剥除
		default:
			return r
		}
	}, title)
}
