//go:build windows

package localstore

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// freeBytes 磁盘可用字节（§52 Bootstrap 空间检查 / §88 ERROR_DISK_FULL 预检）。
func freeBytes(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("encode path %s: %w", path, err)
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeAvail, &total, &totalFree); err != nil {
		return 0, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", path, err)
	}
	return freeAvail, nil
}
