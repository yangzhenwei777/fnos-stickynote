//go:build !windows

package dpapi

// 非 Windows 平台：恒明确失败。本地开发/CI(Linux) 上依赖 DPAPI 的路径必须显式跳过
// （测试 t.Skip + 原因），严禁静默降级为明文存储或假实现。

func Protect([]byte) ([]byte, error)   { return nil, ErrUnsupported }
func Unprotect([]byte) ([]byte, error) { return nil, ErrUnsupported }
