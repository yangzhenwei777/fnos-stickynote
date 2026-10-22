//go:build !windows

package backup

import (
	"fmt"
	"os"
)

// fsyncDir 对目录 fsync，确保 rename 产生的目录项在断电后仍持久（Linux/Unix，fnOS 真目标）。
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}
