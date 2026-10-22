package main

import (
	"flag"
	"os"
	"strings"

	"stickynote/cmd"
)

var (
	Version   = "v1.0.0"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func main() {
	cmd.SetVersionInfo(Version, BuildTime, GitCommit)

	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		cmd.ExecuteCLI(os.Args[1:])
		return
	}

	port := flag.Int("port", 0, "服务端口 (默认 13389)")
	dataDir := flag.String("data-dir", "", "数据目录 (默认 ./data)")
	webDir := flag.String("web-dir", "", "前端静态文件目录")
	uploadDir := flag.String("upload-dir", "", "上传文件目录")
	shareDirs := flag.String("share-dirs", "", "共享目录列表，冒号分隔")
	// 统一网关模式（v2.2.0）：默认取 TRIM_APPDEST/app.sock（fnOS 注入）；空串=禁用 socket 监听。
	// 手动裸跑（无 TRIM_APPDEST）时须显式传 -socket=<path> 启用网关通道；TCP 端口恒可用（双轨并存）。
	socketPath := flag.String("socket", cmd.DefaultSocketPath(), "unix socket 监听路径 (空=禁用；默认 TRIM_APPDEST/app.sock)")
	flag.Parse()

	cmd.StartServer(*port, *dataDir, *webDir, *uploadDir, *shareDirs, *socketPath)
}
