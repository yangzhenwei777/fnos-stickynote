package htmltext

import (
	"os"
	"path/filepath"
	"strings"
)

// imageresolver.go —— MD 导出的图片 base64 内联解析器（v2.2.2 引入，R2 自
// handler/note_export.go 下沉：桌面导出绑定与服务端 handler 必须**同一实现**，
// 留在 handler 会拖 gin 栈进桌面、复制则会漂移——同 notetext 下沉纪律）。

// mdImageMIME 上传图片扩展名 → data URI MIME（放行常见位图与 svg）
var mdImageMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".svg":  "image/svg+xml",
}

// mdImageResolverMaxBytes 单图嵌入上限（防御性；上传侧本有单文件限制）
const mdImageResolverMaxBytes = 32 << 20 // 32MB

// NewFileImageResolver 构造带篇内缓存的图片解析器（同一图多次引用只读盘+编码一次）。
// zip 批量导出时在循环外建一个共享实例，跨篇同图同样只编码一次。
// 安全：文件名白名单校验（仅 [A-Za-z0-9._-]，拒绝路径分隔符与 ..）杜绝路径穿越；
// 读取失败/超限/扩展名不识别一律 ok=false → 保持原样引用，不阻断导出。
// uploadDir 为上传文件目录（服务端 handler 与桌面档案 uploads 目录同口径）。
func NewFileImageResolver(uploadDir string) ImageResolver {
	type mdImageHit struct {
		mime string
		data []byte
	}
	cache := make(map[string]mdImageHit) // src → 解析结果（data=nil 即未命中）
	return func(src string) (string, []byte, bool) {
		if hit, ok := cache[src]; ok {
			return hit.mime, hit.data, hit.data != nil
		}
		// 存储态 /uploads/<name>；服务端存储不存在展示态（挂载前缀是前端渲染层概念）
		name := strings.TrimPrefix(src, "/uploads/")
		if name == "" || name == src {
			cache[src] = mdImageHit{}
			return "", nil, false
		}
		// 文件名字符集白名单（防穿越：拒绝任何路径分隔符与 ..）
		blocked := strings.ContainsAny(name, "/\\") || strings.Contains(name, "..")
		if !blocked {
			for _, r := range name {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
					blocked = true
					break
				}
			}
		}
		var hit mdImageHit
		if !blocked {
			ext := strings.ToLower(filepath.Ext(name))
			if m, ok := mdImageMIME[ext]; ok {
				d, err := os.ReadFile(filepath.Join(uploadDir, name))
				if err == nil && len(d) > 0 && len(d) <= mdImageResolverMaxBytes {
					hit = mdImageHit{mime: m, data: d}
				}
			}
		}
		cache[src] = hit
		return hit.mime, hit.data, hit.data != nil
	}
}
