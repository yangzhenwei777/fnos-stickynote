module stickynote

// 2026-10-09：1.26.7 → 1.26.9（Go 官方安全发布，13 条 net/http·html/template·os·crypto/tls 公告）
//   GO-2026-6599/6600/6604/6605/6607/6608/6609/6613 及 net/http 家族，修复版本均为 1.26.9。
//   本项目为 HTTP 服务端（HTTP/2 崩溃 / Range 头无限大 / MIME 头内存绕过 真实暴露），不可挂账。
//   同步 golang.org/x/net v0.57.0 → v0.60.0（OSV 指定 x/net 侧修复版本，本项目仅用 x/net/html）。
// 2026-10-21：1.26.9 → 1.26.10（GO-2026-6809 标准库 crypto/x509：不信任被攻陷中间证书，
//   修复版本 1.26.10；本项目真实暴露 = 备份目标 https:// WebDAV 的 x509.Certificate.Verify
//   出站验证路径；零代码逻辑改动，工具链补丁升级 + 产物同名重出）
// 2026-10-29：golang.org/x/net v0.60.0 → v0.62.0（GO-2026-6860 html 文本节点合并 O(n²) /
//   GO-2026-6866 html 渲染祖先命名空间校验；本项目导入与消毒链经 x/net/html 解析用户内容，
//   真实暴露，真修复不挂账；连带 x/crypto 0.58.0 / x/sys 0.49.0 / x/text 0.43.0）
go 1.26.10

require (
	github.com/gin-contrib/cors v1.7.3
	github.com/gin-contrib/gzip v1.1.0
	github.com/gin-gonic/gin v1.10.1
	github.com/go-playground/validator/v10 v10.30.1
	github.com/golang-jwt/jwt/v5 v5.2.2
	github.com/gomarkdown/markdown v0.0.0-20260725000948-8435af3f5984
	github.com/google/uuid v1.6.0
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/pquerna/otp v1.5.0
	github.com/studio-b12/gowebdav v0.13.0
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	modernc.org/sqlite v1.34.5
)

require (
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/gin-contrib/sse v1.1.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.1 // indirect
	golang.org/x/arch v0.22.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
)
