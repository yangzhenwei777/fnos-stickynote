package sync

import (
	"os"
	"path/filepath"
	"strings"
)

// configure.go —— 实例身份来源：server_instance_id 继续以 DataDir/.instance_id
// 为唯一权威（v20 §22，不复制第二份；§91 与 epoch 职责区分）。
var dataDir string

// Configure 由 cmd/server 在 database.Init 后注入；未注入时 instance 返回空串
// （客户端据此判定服务端未就绪，§74 语义）。
func Configure(dir string) { dataDir = dir }

func instanceID() string {
	if dataDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dataDir, ".instance_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
