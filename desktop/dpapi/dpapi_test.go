package dpapi

import (
	"errors"
	"runtime"
	"testing"
)

// 跨平台契约：非 Windows 恒 ErrUnsupported（明确失败面）；Windows 空输入拒绝。
func TestPlatformContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := Protect(nil); err == nil || errors.Is(err, ErrUnsupported) {
			t.Fatalf("on windows Protect(nil) must be a real error, got %v", err)
		}
		return
	}
	if _, err := Protect([]byte("x")); err != ErrUnsupported {
		t.Fatalf("non-windows Protect must return ErrUnsupported, got %v", err)
	}
	if _, err := Unprotect([]byte("x")); err != ErrUnsupported {
		t.Fatalf("non-windows Unprotect must return ErrUnsupported, got %v", err)
	}
}
