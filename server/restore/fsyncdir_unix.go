//go:build !windows

package restore

import "fmt"
import "os"

// fsyncDir 对目录 fsync，确保 rename 产生的目录项在断电后仍持久（fnOS/Linux 真目标强制）。
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
