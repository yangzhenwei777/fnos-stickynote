package restore

import (
	"fmt"
	"os"
	"path/filepath"
)

// durableWrite 原子写：同目录临时文件 → fsync(file) → rename → fsync(parent dir)。
// 用于写前日志本身（其可靠性决定崩溃能否恢复）。
func durableWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open journal tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write journal tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync journal: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close journal tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename journal: %w", err)
	}
	return fsyncDir(dir)
}

// syncPath 对已存在文件 fsync（尽力而为；文件不存在/平台不支持时忽略）。
func syncPath(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
